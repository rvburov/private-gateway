package httpapi

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

const (
	performanceDownloadLimit = 4 << 20
	performanceUploadLimit   = 1 << 20
	performanceMinuteBudget  = 64 << 20
	performanceTimeout       = 10 * time.Second
)

// Один замер не должен занимать канал одновременно с другим. Общий бюджет ограничивает
// трафик всех авторизованных сеансов панели, включая загрузки с неизвестной длиной.
type performanceTransfers struct {
	mu          sync.Mutex
	active      bool
	windowStart time.Time
	used        int64
}

func (p *performanceTransfers) reserve(bytes int64) (func(int64), int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	if p.active {
		return nil, http.StatusConflict, 0
	}
	if p.windowStart.IsZero() || now.Sub(p.windowStart) >= time.Minute {
		p.windowStart, p.used = now, 0
	}
	if bytes > performanceMinuteBudget-p.used {
		retry := int(time.Until(p.windowStart.Add(time.Minute)).Seconds()) + 1
		return nil, http.StatusTooManyRequests, retry
	}
	p.active, p.used = true, p.used+bytes
	window := p.windowStart
	return func(transferred int64) {
		p.mu.Lock()
		defer p.mu.Unlock()
		if transferred < 0 {
			transferred = 0
		}
		if transferred > bytes {
			transferred = bytes
		}
		if p.windowStart.Equal(window) {
			p.used -= bytes - transferred
		}
		p.active = false
	}, 0, 0
}

func (s *Server) reservePerformance(w http.ResponseWriter, bytes int64) (func(int64), bool) {
	finish, status, retry := s.performance.reserve(bytes)
	if status == http.StatusConflict {
		fail(w, status, "a performance transfer is already running")
		return nil, false
	}
	if status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		fail(w, status, "performance transfer budget exhausted")
		return nil, false
	}
	return finish, true
}

func performanceHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Encoding", "identity")
}

func (s *Server) performancePing(w http.ResponseWriter, r *http.Request) {
	performanceHeaders(w)
	respond(w, http.StatusOK, map[string]bool{"ok": true})
}

func performanceDownloadSize(rawQuery string) (int64, int) {
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return 0, http.StatusBadRequest
	}
	values, found := query["bytes"]
	if !found {
		return performanceDownloadLimit, 0
	}
	if len(values) != 1 || values[0] == "" {
		return 0, http.StatusBadRequest
	}
	for _, digit := range values[0] {
		if digit < '0' || digit > '9' {
			return 0, http.StatusBadRequest
		}
	}
	bytes, err := strconv.ParseUint(values[0], 10, 64)
	if err != nil || bytes > performanceDownloadLimit {
		return 0, http.StatusRequestEntityTooLarge
	}
	if bytes == 0 {
		return 0, http.StatusBadRequest
	}
	return int64(bytes), 0
}

// ResponseController проходит через обёртки middleware. Отмена запроса прерывает
// блокирующее чтение/запись сетевым дедлайном; после замера дедлайны не остаются на keep-alive.
func performanceContext(w http.ResponseWriter, r *http.Request) (context.Context, func(), error) {
	ctx, cancel := context.WithTimeout(r.Context(), performanceTimeout)
	controller := http.NewResponseController(w)
	deadline, _ := ctx.Deadline()
	setDeadline := func(deadline time.Time) error {
		for _, err := range []error{controller.SetReadDeadline(deadline), controller.SetWriteDeadline(deadline)} {
			if err != nil && !errors.Is(err, http.ErrNotSupported) {
				return err
			}
		}
		return nil
	}
	if err := setDeadline(deadline); err != nil {
		cancel()
		_ = setDeadline(time.Time{})
		return nil, nil, err
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = setDeadline(time.Now())
		close(done)
	})
	return ctx, func() {
		if !stop() {
			<-done
		}
		cancel()
		_ = setDeadline(time.Time{})
	}, nil
}

func (s *Server) performanceDownload(w http.ResponseWriter, r *http.Request) {
	performanceHeaders(w)
	bytes, status := performanceDownloadSize(r.URL.RawQuery)
	if status != 0 {
		fail(w, status, "bytes must be one integer between 1 and 4194304")
		return
	}
	finish, ok := s.reservePerformance(w, bytes)
	if !ok {
		return
	}
	var transferred int64
	defer func() { finish(transferred) }()
	ctx, cleanup, err := performanceContext(w, r)
	if err != nil {
		fail(w, http.StatusInternalServerError, "transfer deadlines unavailable")
		return
	}
	defer cleanup()
	if ctx.Err() != nil {
		fail(w, http.StatusRequestTimeout, "performance transfer canceled")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(bytes, 10))
	buffer := make([]byte, 32<<10)
	for transferred < bytes && ctx.Err() == nil {
		size := min(int64(len(buffer)), bytes-transferred)
		// Новая случайная порция на каждой итерации исключает сжатие повторяющегося блока.
		if _, err := rand.Read(buffer[:size]); err != nil {
			return
		}
		n, err := w.Write(buffer[:size])
		transferred += int64(n)
		if err != nil || n != int(size) {
			return
		}
	}
}

func (s *Server) performanceUpload(w http.ResponseWriter, r *http.Request) {
	performanceHeaders(w)
	if r.ContentLength > performanceUploadLimit {
		r.Close = true
		fail(w, http.StatusRequestEntityTooLarge, "upload exceeds 1048576 bytes")
		return
	}
	if encoding := r.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
		r.Close = true
		fail(w, http.StatusUnsupportedMediaType, "upload must be uncompressed")
		return
	}
	// Один дополнительный байт позволяет отличить допустимое тело от превышения лимита
	// даже при chunked-передаче. Он тоже учитывается в бюджете.
	finish, ok := s.reservePerformance(w, performanceUploadLimit+1)
	if !ok {
		return
	}
	var transferred int64
	defer func() { finish(transferred) }()
	ctx, cleanup, err := performanceContext(w, r)
	if err != nil {
		fail(w, http.StatusInternalServerError, "transfer deadlines unavailable")
		return
	}
	defer cleanup()
	reader := http.MaxBytesReader(w, r.Body, performanceUploadLimit)
	buffer := make([]byte, 32<<10)
	for ctx.Err() == nil {
		n, readErr := reader.Read(buffer)
		transferred += int64(n)
		if ctx.Err() != nil {
			break
		}
		var tooLarge *http.MaxBytesError
		if errors.As(readErr, &tooLarge) {
			// MaxBytesReader прочитал лишний байт, но не возвращает его вызывающему.
			transferred++
			r.Close = true
			fail(w, http.StatusRequestEntityTooLarge, "upload exceeds 1048576 bytes")
			return
		}
		if readErr == io.EOF {
			respond(w, http.StatusOK, map[string]int64{"bytes": transferred})
			return
		}
		if readErr != nil {
			r.Close = true
			var timeout net.Error
			if errors.As(readErr, &timeout) && timeout.Timeout() {
				fail(w, http.StatusRequestTimeout, "performance transfer timed out")
				return
			}
			fail(w, http.StatusBadRequest, "upload could not be read")
			return
		}
	}
	r.Close = true
	fail(w, http.StatusRequestTimeout, "performance transfer canceled")
}

func (s *Server) performanceServer(w http.ResponseWriter, r *http.Request) {
	performanceHeaders(w)
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	respond(w, http.StatusOK, collectServerDiagnostics(ctx, s.options.Checks.XrayConfigPath))
}
