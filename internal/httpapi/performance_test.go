package httpapi

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const performanceTestToken = "performance-local-token-at-least-32-characters"

func performanceRequest(method, path string, body io.Reader) *http.Request {
	r := httptest.NewRequest(method, "/api/v1/checks/performance/"+path, body)
	r.RemoteAddr = "127.0.0.1:40000"
	r.Header.Set("Authorization", "Bearer "+performanceTestToken)
	return r
}

type unreadPerformanceBody struct{ reads int }

func (b *unreadPerformanceBody) Read([]byte) (int, error) {
	b.reads++
	return 0, io.EOF
}
func (b *unreadPerformanceBody) Close() error { return nil }

func TestPerformanceRoutesRequireAuthenticationBeforeTransfer(t *testing.T) {
	handler := New(nil, nil, nil, Options{Token: performanceTestToken})
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "ping"}, {http.MethodGet, "download"},
		{http.MethodPost, "upload"}, {http.MethodGet, "server"},
	} {
		for _, auth := range []string{"", "Bearer wrong", "Bearer " + performanceTestToken} {
			body := &unreadPerformanceBody{}
			r := performanceRequest(route.method, route.path, body)
			r.Header.Set("Authorization", auth)
			if auth == "Bearer "+performanceTestToken {
				r.RemoteAddr = "198.51.100.3:40000"
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != http.StatusUnauthorized || body.reads != 0 {
				t.Fatalf("%s auth=%q: code=%d reads=%d", route.path, auth, w.Code, body.reads)
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("an unauthorized diagnostic response may not be cached")
			}
		}
	}
}

func TestPerformanceRoutesWorkWithoutDatabase(t *testing.T) {
	handler := New(nil, nil, nil, Options{Token: performanceTestToken})
	for _, path := range []string{"ping", "server"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, performanceRequest(http.MethodGet, path, nil))
		var data map[string]any
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &data) != nil || len(data) == 0 {
			t.Fatalf("%s: code=%d body=%s", path, w.Code, w.Body.String())
		}
		if path == "ping" && data["ok"] != true {
			t.Fatalf("ping body=%s", w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Encoding") != "identity" {
			t.Fatalf("%s headers=%v", path, w.Header())
		}
	}
}

func TestPerformanceDownloadValidatesSize(t *testing.T) {
	for _, tc := range []struct {
		query  string
		status int
	}{
		{"bytes=0", 400}, {"bytes=", 400}, {"bytes=-1", 400}, {"bytes=1.5", 400},
		{"bytes=+1", 400}, {"bytes=%2B1", 400}, {"bytes=1&bytes=2", 400},
		{"bytes=1&%62ytes=2", 400}, {"bytes=%zz", 400}, {"bytes=1;bytes=2", 400},
		{"bytes=4194305", 413}, {"bytes=99999999999999999999999999", 413},
	} {
		t.Run(tc.query, func(t *testing.T) {
			s := &Server{}
			w := httptest.NewRecorder()
			s.performanceDownload(w, performanceRequest(http.MethodGet, "download?"+tc.query, nil))
			if w.Code != tc.status || s.performance.used != 0 {
				t.Fatalf("status=%d used=%d", w.Code, s.performance.used)
			}
		})
	}
	if size, status := performanceDownloadSize(""); size != performanceDownloadLimit || status != 0 {
		t.Fatalf("default size=%d status=%d", size, status)
	}
}

func TestPerformanceStreamsUncompressedBytesOverHTTP(t *testing.T) {
	server := httptest.NewServer(New(nil, nil, nil, Options{Token: performanceTestToken}))
	defer server.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	const size = 131071
	r, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/checks/performance/download?bytes="+strconv.Itoa(size), nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+performanceTestToken)
	r.Header.Set("Accept-Encoding", "gzip")
	response, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || len(data) != size || response.ContentLength != size {
		t.Fatalf("download status=%d bytes=%d length=%d err=%v", response.StatusCode, len(data), response.ContentLength, err)
	}
	if response.Header.Get("Content-Encoding") != "identity" || response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("download headers=%v", response.Header)
	}
	var compressed bytes.Buffer
	zipper := gzip.NewWriter(&compressed)
	if _, err = zipper.Write(data); err != nil {
		t.Fatal(err)
	}
	if err = zipper.Close(); err != nil {
		t.Fatal(err)
	}
	if compressed.Len() < len(data)*9/10 {
		t.Fatalf("benchmark payload is compressible: %d bytes became %d", len(data), compressed.Len())
	}
	r, err = http.NewRequest(http.MethodPost, server.URL+"/api/v1/checks/performance/upload", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+performanceTestToken)
	r.ContentLength = -1 // Проверяем настоящую chunked-передачу, а не только тело известной длины.
	response, err = client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var uploaded struct {
		Bytes int `json:"bytes"`
	}
	if err = json.NewDecoder(response.Body).Decode(&uploaded); err != nil || response.StatusCode != http.StatusOK || uploaded.Bytes != size {
		t.Fatalf("upload status=%d bytes=%d err=%v", response.StatusCode, uploaded.Bytes, err)
	}
}

func TestPerformanceUploadBoundsKnownAndChunkedBodies(t *testing.T) {
	for _, knownLength := range []bool{false, true} {
		s := &Server{}
		body := &unreadPerformanceBody{}
		r := performanceRequest(http.MethodPost, "upload", body)
		if knownLength {
			r.ContentLength = performanceUploadLimit + 1
		} else {
			r.ContentLength = -1
			r.Body = io.NopCloser(io.LimitReader(infinitePerformanceReader{}, performanceUploadLimit+100))
		}
		w := httptest.NewRecorder()
		s.performanceUpload(w, r)
		if w.Code != http.StatusRequestEntityTooLarge || body.reads != 0 {
			t.Fatalf("known=%t code=%d reads=%d", knownLength, w.Code, body.reads)
		}
		wantUsed := int64(performanceUploadLimit + 1)
		if knownLength {
			wantUsed = 0
		}
		if s.performance.used != wantUsed || s.performance.active {
			t.Fatalf("known=%t used=%d active=%t", knownLength, s.performance.used, s.performance.active)
		}
	}
	for _, length := range []int64{-1, performanceUploadLimit} {
		s := &Server{}
		r := performanceRequest(http.MethodPost, "upload", bytes.NewReader(make([]byte, performanceUploadLimit)))
		r.ContentLength = length
		w := httptest.NewRecorder()
		s.performanceUpload(w, r)
		var result struct {
			Bytes int64 `json:"bytes"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Bytes != performanceUploadLimit || s.performance.used != performanceUploadLimit {
			t.Fatalf("exact limit length=%d code=%d bytes=%d used=%d", length, w.Code, result.Bytes, s.performance.used)
		}
	}
	s := &Server{}
	r := performanceRequest(http.MethodPost, "upload", strings.NewReader("compressed"))
	r.Header.Set("Content-Encoding", "gzip")
	w := httptest.NewRecorder()
	s.performanceUpload(w, r)
	if w.Code != http.StatusUnsupportedMediaType || s.performance.used != 0 {
		t.Fatalf("compressed upload code=%d used=%d", w.Code, s.performance.used)
	}
}

type infinitePerformanceReader struct{}

func (infinitePerformanceReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestPerformanceBudgetAndConcurrencyDoNotBlockPing(t *testing.T) {
	s := &Server{}
	finish, ok := s.reservePerformance(httptest.NewRecorder(), 100)
	if !ok {
		t.Fatal("first reservation rejected")
	}
	w := httptest.NewRecorder()
	s.performanceDownload(w, performanceRequest(http.MethodGet, "download?bytes=1", nil))
	if w.Code != http.StatusConflict {
		t.Fatalf("concurrent download status=%d", w.Code)
	}
	w = httptest.NewRecorder()
	s.performancePing(w, performanceRequest(http.MethodGet, "ping", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("concurrent ping status=%d", w.Code)
	}
	finish(7)
	if s.performance.used != 7 || s.performance.active {
		t.Fatalf("incomplete transfer not refunded: used=%d active=%t", s.performance.used, s.performance.active)
	}
	s.performance.used = performanceMinuteBudget - 32
	w = httptest.NewRecorder()
	s.performanceDownload(w, performanceRequest(http.MethodGet, "download?bytes=33", nil))
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Fatalf("over-budget status=%d headers=%v", w.Code, w.Header())
	}
	w = httptest.NewRecorder()
	s.performanceUpload(w, performanceRequest(http.MethodPost, "upload", strings.NewReader("x")))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("upload has a separate budget: status=%d", w.Code)
	}
	s.performance.windowStart = time.Now().Add(-time.Minute)
	w = httptest.NewRecorder()
	s.performanceDownload(w, performanceRequest(http.MethodGet, "download?bytes=17", nil))
	if w.Code != http.StatusOK || w.Body.Len() != 17 || s.performance.used != 17 {
		t.Fatalf("budget did not reset: status=%d used=%d", w.Code, s.performance.used)
	}
}

type deadlinePerformanceWriter struct {
	*httptest.ResponseRecorder
	mu        sync.Mutex
	deadlines []time.Time
	interrupt chan struct{}
	once      sync.Once
}

func (w *deadlinePerformanceWriter) SetReadDeadline(deadline time.Time) error {
	w.mu.Lock()
	w.deadlines = append(w.deadlines, deadline)
	w.mu.Unlock()
	if !deadline.IsZero() && !deadline.After(time.Now()) {
		w.once.Do(func() { close(w.interrupt) })
	}
	return nil
}
func (w *deadlinePerformanceWriter) SetWriteDeadline(deadline time.Time) error {
	w.mu.Lock()
	w.deadlines = append(w.deadlines, deadline)
	w.mu.Unlock()
	return nil
}

type interruptedPerformanceBody struct {
	started   chan struct{}
	interrupt chan struct{}
}

func (b *interruptedPerformanceBody) Read([]byte) (int, error) {
	close(b.started)
	<-b.interrupt
	return 0, context.Canceled
}
func (b *interruptedPerformanceBody) Close() error { return nil }

func TestPerformanceCancellationInterruptsIOAndReleasesReservation(t *testing.T) {
	s := &Server{}
	w := &deadlinePerformanceWriter{ResponseRecorder: httptest.NewRecorder(), interrupt: make(chan struct{})}
	body := &interruptedPerformanceBody{started: make(chan struct{}), interrupt: w.interrupt}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := performanceRequest(http.MethodPost, "upload", body).WithContext(ctx)
	done := make(chan struct{})
	go func() { s.performanceUpload(w, r); close(done) }()
	select {
	case <-body.started:
	case <-time.After(2 * time.Second):
		t.Fatal("upload did not start reading")
	}
	other := httptest.NewRecorder()
	unread := &unreadPerformanceBody{}
	s.performanceUpload(other, performanceRequest(http.MethodPost, "upload", unread))
	if other.Code != http.StatusConflict || unread.reads != 0 {
		t.Fatalf("concurrent upload code=%d reads=%d", other.Code, unread.reads)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not interrupt upload")
	}
	if w.Code != http.StatusRequestTimeout || s.performance.active || s.performance.used != 0 {
		t.Fatalf("canceled upload code=%d active=%t used=%d", w.Code, s.performance.active, s.performance.used)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.deadlines) < 6 || !w.deadlines[len(w.deadlines)-1].IsZero() || !w.deadlines[len(w.deadlines)-2].IsZero() {
		t.Fatalf("deadlines were not cleared: %v", w.deadlines)
	}
	if initial := w.deadlines[0]; initial.Before(time.Now().Add(8*time.Second)) || initial.After(time.Now().Add(11*time.Second)) {
		t.Fatalf("initial deadline=%v", initial)
	}
}

func TestPerformanceCanceledDownloadDoesNotWritePayload(t *testing.T) {
	s := &Server{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := httptest.NewRecorder()
	s.performanceDownload(w, performanceRequest(http.MethodGet, "download?bytes=8192", nil).WithContext(ctx))
	if w.Code != http.StatusRequestTimeout || s.performance.active || s.performance.used != 0 {
		t.Fatalf("canceled download code=%d active=%t used=%d", w.Code, s.performance.active, s.performance.used)
	}
}
