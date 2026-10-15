package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/felixge/httpsnoop"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rvburov/private-gateway/internal/browser"
	"github.com/rvburov/private-gateway/internal/storage"
	"github.com/rvburov/private-gateway/internal/xray"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type Xray interface {
	Validate(context.Context, []xray.Client) error
	Apply(context.Context, []xray.Client) error
}
type Browser interface {
	Status(context.Context) (browser.Status, error)
	Start(context.Context) (browser.Status, error)
	Stop(context.Context) (browser.Status, error)
}
type Options struct {
	Token          string
	WebDir         string
	XrayConfigured bool
}
type Server struct {
	store         *storage.Store
	xray          Xray
	browser       Browser
	options       Options
	loginMu       sync.Mutex
	sessions      map[string]adminSession
	pending       map[string]*pendingLogin
	loginAttempts map[string]loginAttempts
}

type loginAttempts struct {
	count int
	since time.Time
}

func New(store *storage.Store, xray Xray, browser Browser, options Options) http.Handler {
	s := &Server{store: store, xray: xray, browser: browser, options: options, sessions: make(map[string]adminSession), pending: make(map[string]*pendingLogin), loginAttempts: make(map[string]loginAttempts)}
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(securityHeaders)
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, map[string]string{"status": "ok"}) })
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := store.Ping(ctx); err != nil {
			fail(w, 503, "database unavailable")
			return
		}
		respond(w, 200, map[string]string{"status": "ready"})
	})
	registry := prometheus.NewRegistry()
	registry.MustRegister(prometheus.NewGoCollector(), prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
	requests := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "gateway_http_requests_total", Help: "HTTP requests by method and status class."}, []string{"method", "status_class"})
	registry.MustRegister(requests)
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/auth/config", s.authConfig)
		r.Post("/auth/login", s.login)
		r.Post("/auth/2fa", s.secondFactor)
		r.Post("/auth/2fa/setup", s.startTOTPSetup)
		r.Post("/auth/2fa/setup/confirm", s.confirmTOTPSetup)
		r.Group(func(r chi.Router) {
			r.Use(s.auth)
			r.Post("/auth/logout", s.logout)
			r.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
			r.Get("/status", s.status)
			r.Get("/nodes", s.nodes)
			r.Get("/devices", s.devices)
			r.Post("/devices", s.create)
			r.Get("/devices/{id}/profile", s.export)
			r.Post("/devices/{id}/revoke", s.revoke)
			r.Get("/audit", s.audit)
			r.Post("/config/validate", s.validate)
			r.Post("/config/apply", s.apply)
			r.Get("/browser", s.browserStatus)
			r.Post("/browser/start", s.browserStart)
			r.Post("/browser/stop", s.browserStop)
		})
		r.NotFound(func(w http.ResponseWriter, r *http.Request) { fail(w, 404, "not found") })
	})
	r.Get("/*", s.web)
	// Avoid metric labels containing device identifiers or secrets.
	measured := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		m := httpsnoop.CaptureMetrics(r, w, req)
		class := "5xx"
		switch {
		case m.Code < 300:
			class = "2xx"
		case m.Code < 400:
			class = "3xx"
		case m.Code < 500:
			class = "4xx"
		}
		method := req.Method
		if method != "GET" && method != "POST" && method != "HEAD" {
			method = "OTHER"
		}
		requests.WithLabelValues(method, class).Inc()
	})
	return otelhttp.NewHandler(measured, "gateway.http")
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		value := strings.TrimPrefix(header, "Bearer ")
		if !strings.HasPrefix(header, "Bearer ") || value == "" {
			w.Header().Set("WWW-Authenticate", "Bearer")
			fail(w, 401, "unauthorized")
			return
		}
		admin, err := s.administrator(r.Context())
		if err != nil {
			fail(w, 503, "authentication unavailable")
			return
		}
		if admin.Credentials.Configured() && s.validSession(value, admin.Generation) {
			next.ServeHTTP(w, r)
			return
		}
		a := sha256.Sum256([]byte(value))
		b := sha256.Sum256([]byte(s.options.Token))
		if s.options.Token == "" || subtle.ConstantTimeCompare(a[:], b[:]) != 1 || (admin.Credentials.Configured() && !loopbackClient(r.RemoteAddr)) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			fail(w, 401, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func loopbackClient(address string) bool {
	host, _, err := net.SplitHostPort(address)
	return err == nil && net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	result, err := s.store.Status(r.Context())
	if err != nil {
		s.error(w, err)
		return
	}
	result.XrayConfigured = s.options.XrayConfigured
	respond(w, 200, result)
}
func (s *Server) nodes(w http.ResponseWriter, r *http.Request) {
	result, err := s.store.Nodes(r.Context())
	if err != nil {
		s.error(w, err)
		return
	}
	respond(w, 200, map[string]any{"nodes": result})
}
func (s *Server) devices(w http.ResponseWriter, r *http.Request) {
	result, err := s.store.Devices(r.Context())
	if err != nil {
		s.error(w, err)
		return
	}
	respond(w, 200, map[string]any{"devices": result})
}
func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name   string `json:"name"`
		NodeID string `json:"node_id"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.NodeID == "" {
		input.NodeID = storage.LocalNodeID
	}
	result, err := s.store.Create(r.Context(), input.Name, input.NodeID)
	if err != nil {
		s.error(w, err)
		return
	}
	respond(w, 201, result)
}
func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	result, err := s.store.Revoke(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.error(w, err)
		return
	}
	respond(w, 200, result)
}
func (s *Server) export(w http.ResponseWriter, r *http.Request) {
	result, err := s.store.Export(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.error(w, err)
		return
	}
	respond(w, 200, result)
}
func (s *Server) audit(w http.ResponseWriter, r *http.Request) {
	result, err := s.store.Audit(r.Context())
	if err != nil {
		s.error(w, err)
		return
	}
	respond(w, 200, map[string]any{"events": result})
}
func (s *Server) validate(w http.ResponseWriter, r *http.Request) { s.configure(w, r, false) }
func (s *Server) apply(w http.ResponseWriter, r *http.Request)    { s.configure(w, r, true) }
func (s *Server) configure(w http.ResponseWriter, r *http.Request, apply bool) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	operation := s.xray.Validate
	if apply {
		operation = s.xray.Apply
	}
	if err := s.store.Configure(ctx, apply, operation); err != nil {
		if errors.Is(err, storage.ErrStaleConfig) {
			fail(w, 409, err.Error())
			return
		}
		slog.Error("Xray operation failed")
		fail(w, 503, "Xray operation failed; verify binary, configuration and restart service")
		return
	}
	status := "valid"
	if apply {
		status = "applied"
	}
	respond(w, 200, map[string]string{"status": status})
}
func (s *Server) browserStatus(w http.ResponseWriter, r *http.Request) {
	s.browserOperation(w, r, s.browser.Status)
}
func (s *Server) browserStart(w http.ResponseWriter, r *http.Request) {
	s.browserOperation(w, r, s.browser.Start)
}
func (s *Server) browserStop(w http.ResponseWriter, r *http.Request) {
	s.browserOperation(w, r, s.browser.Stop)
}
func (s *Server) browserOperation(w http.ResponseWriter, r *http.Request, fn func(context.Context) (browser.Status, error)) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	result, err := fn(ctx)
	if err != nil {
		fail(w, 503, "browser worker unavailable; check the Docker browser service")
		return
	}
	respond(w, 200, result)
}
func (s *Server) web(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		fail(w, 404, "not found")
		return
	}
	if s.options.WebDir == "" {
		fail(w, 404, "web panel not built")
		return
	}
	if _, err := os.Stat(filepath.Join(s.options.WebDir, "index.html")); err != nil {
		fail(w, 404, "web panel not built")
		return
	}
	http.FileServer(http.Dir(s.options.WebDir)).ServeHTTP(w, r)
}
func (s *Server) error(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrNotFound):
		fail(w, 404, err.Error())
	case errors.Is(err, storage.ErrInvalid):
		fail(w, 400, err.Error())
	case errors.Is(err, storage.ErrConflict):
		fail(w, 409, err.Error())
	case errors.Is(err, storage.ErrRevoked):
		fail(w, 410, err.Error())
	default:
		slog.Error("database operation failed")
		fail(w, 500, "operation failed")
	}
}
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		fail(w, 415, "Content-Type must be application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		fail(w, 400, "invalid JSON body")
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		fail(w, 400, "body must contain one JSON object")
		return false
	}
	return true
}
func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, status int, message string) {
	respond(w, status, map[string]string{"error": message})
}
