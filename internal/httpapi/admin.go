package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rvburov/private-gateway/internal/adminauth"
	"github.com/rvburov/private-gateway/internal/storage"
)

type adminSession struct {
	expires    time.Time
	generation int64
}

type pendingLogin struct {
	username   string
	client     string
	stage      string
	secret     string
	generation int64
	expires    time.Time
	attempts   int
}

func (s *Server) administrator(ctx context.Context) (storage.Administrator, error) {
	if s.store == nil {
		return storage.Administrator{}, nil
	}
	return s.store.Administrator(ctx)
}

func (s *Server) authConfig(w http.ResponseWriter, r *http.Request) {
	admin, err := s.administrator(r.Context())
	if err != nil {
		fail(w, 503, "authentication unavailable")
		return
	}
	respond(w, 200, map[string]bool{"configured": admin.Credentials.Configured()})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &input) {
		return
	}
	admin, err := s.administrator(r.Context())
	if err != nil {
		fail(w, 503, "authentication unavailable")
		return
	}
	if !admin.Credentials.Configured() {
		fail(w, 503, "administrator credentials have not been configured on the server")
		return
	}
	now := time.Now()
	client := loginClient(r)
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	for key, entry := range s.loginAttempts {
		if now.Sub(entry.since) > 5*time.Minute {
			delete(s.loginAttempts, key)
		}
	}
	attempt := s.loginAttempts[client]
	if now.Sub(attempt.since) > 5*time.Minute {
		attempt = loginAttempts{since: now}
	}
	if attempt.count >= 10 || len(s.loginAttempts) >= 1024 && attempt.count == 0 {
		fail(w, 429, "too many attempts; retry in five minutes")
		return
	}
	if !admin.Credentials.VerifyPassword(input.Username, input.Password) {
		attempt.count++
		s.loginAttempts[client] = attempt
		fail(w, 401, "invalid username or password")
		return
	}
	token, err := adminauth.NewSessionToken()
	if err != nil {
		fail(w, 503, "authentication unavailable")
		return
	}
	delete(s.loginAttempts, client)
	for key, pending := range s.pending {
		if now.After(pending.expires) || len(s.pending) >= 64 {
			delete(s.pending, key)
		}
	}
	stage := "totp_required"
	if admin.Credentials.TOTPSecret == "" {
		stage = "totp_setup_required"
	}
	s.pending[token] = &pendingLogin{username: admin.Credentials.Username, client: client,
		stage: stage, generation: admin.Generation, expires: now.Add(5 * time.Minute)}
	respond(w, 200, map[string]string{"status": stage, "pending": token})
}

func (s *Server) pendingFor(r *http.Request, stage string) (string, *pendingLogin, bool) {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return "", nil, false
	}
	token := strings.TrimPrefix(header, "Bearer ")
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	pending := s.pending[token]
	if pending == nil || pending.stage != stage || pending.client != loginClient(r) ||
		time.Now().After(pending.expires) || pending.attempts >= 10 {
		delete(s.pending, token)
		return "", nil, false
	}
	return token, pending, true
}

func (s *Server) currentPendingAccount(w http.ResponseWriter, r *http.Request, stage string) (string, *pendingLogin, storage.Administrator, bool) {
	token, pending, ok := s.pendingFor(r, stage)
	if !ok {
		fail(w, 401, "login expired; enter username and password again")
		return "", nil, storage.Administrator{}, false
	}
	admin, err := s.administrator(r.Context())
	if err != nil {
		fail(w, 503, "authentication unavailable")
		return "", nil, storage.Administrator{}, false
	}
	if admin.Generation != pending.generation || admin.Credentials.Username != pending.username ||
		(stage == "totp_setup_required" && admin.Credentials.TOTPSecret != "") ||
		(stage == "totp_required" && admin.Credentials.TOTPSecret == "") {
		fail(w, 401, "login expired; enter username and password again")
		return "", nil, storage.Administrator{}, false
	}
	return token, pending, admin, true
}

func (s *Server) startTOTPSetup(w http.ResponseWriter, r *http.Request) {
	_, pending, _, ok := s.currentPendingAccount(w, r, "totp_setup_required")
	if !ok {
		return
	}
	s.loginMu.Lock()
	if pending.secret == "" {
		var raw [20]byte
		if _, err := rand.Read(raw[:]); err != nil {
			s.loginMu.Unlock()
			fail(w, 503, "authentication unavailable")
			return
		}
		pending.secret = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw[:])
	}
	secret, username := pending.secret, pending.username
	s.loginMu.Unlock()
	uri := "otpauth://totp/Private%20Gateway:" + url.QueryEscape(username) +
		"?secret=" + secret + "&issuer=Private%20Gateway&algorithm=SHA1&digits=6&period=30"
	respond(w, 200, map[string]string{"secret": secret, "otpauth_uri": uri})
}

func (s *Server) secondFactor(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &input) {
		return
	}
	token, pending, admin, ok := s.currentPendingAccount(w, r, "totp_required")
	if !ok {
		return
	}
	step, valid := adminauth.VerifyCode(admin.Credentials.TOTPSecret, input.Code, time.Now(), admin.LastStep)
	if !valid {
		s.codeFailure(w, token)
		return
	}
	used, err := s.store.UseAdministratorTOTP(r.Context(), step, admin.Generation)
	if err != nil {
		fail(w, 503, "authentication unavailable")
		return
	}
	if !used {
		s.codeFailure(w, token)
		return
	}
	s.finishLogin(w, token, pending)
}

func (s *Server) confirmTOTPSetup(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &input) {
		return
	}
	token, pending, admin, ok := s.currentPendingAccount(w, r, "totp_setup_required")
	if !ok {
		return
	}
	s.loginMu.Lock()
	secret := pending.secret
	s.loginMu.Unlock()
	if secret == "" {
		fail(w, 401, "start authenticator setup first")
		return
	}
	step, valid := adminauth.VerifyCode(secret, input.Code, time.Now(), -1)
	if !valid {
		s.codeFailure(w, token)
		return
	}
	enabled, err := s.store.EnableAdministratorTOTP(r.Context(), secret, step, admin.Generation)
	if err != nil {
		fail(w, 503, "authentication unavailable")
		return
	}
	if !enabled {
		fail(w, 401, "login expired; enter username and password again")
		return
	}
	s.finishLogin(w, token, pending)
}

func (s *Server) codeFailure(w http.ResponseWriter, token string) {
	s.loginMu.Lock()
	if pending := s.pending[token]; pending != nil {
		pending.attempts++
		if pending.attempts >= 10 {
			delete(s.pending, token)
		}
	}
	s.loginMu.Unlock()
	fail(w, 401, "invalid authentication code")
}

func (s *Server) finishLogin(w http.ResponseWriter, pendingToken string, pending *pendingLogin) {
	session, err := adminauth.NewSessionToken()
	if err != nil {
		fail(w, 503, "authentication unavailable")
		return
	}
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	delete(s.pending, pendingToken)
	now := time.Now()
	for id, current := range s.sessions {
		if now.After(current.expires) || len(s.sessions) >= 32 {
			delete(s.sessions, id)
		}
	}
	s.sessions[session] = adminSession{expires: now.Add(8 * time.Hour), generation: pending.generation}
	respond(w, 200, map[string]string{"session": session})
}

func loginClient(r *http.Request) string {
	// Caddy's reverse_proxy overwrites untrusted incoming X-Forwarded-For.
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		parts := strings.Split(forwarded, ",")
		if ip := net.ParseIP(strings.TrimSpace(parts[len(parts)-1])); ip != nil {
			return ip.String()
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) validSession(token string, generation int64) bool {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	session, found := s.sessions[token]
	if !found {
		return false
	}
	if time.Now().After(session.expires) || generation != session.generation {
		delete(s.sessions, token)
		return false
	}
	return true
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	value := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.loginMu.Lock()
	delete(s.sessions, value)
	s.loginMu.Unlock()
	respond(w, 200, map[string]string{"status": "logged out"})
}
