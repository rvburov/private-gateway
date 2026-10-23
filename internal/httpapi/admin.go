package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rvburov/private-gateway/internal/adminauth"
	"github.com/rvburov/private-gateway/internal/storage"
)

type adminSession struct {
	expires       time.Time
	generation    int64
	rememberedKey [32]byte
}

const browserCookieName = "__Secure-private-gateway-browser"
const rememberedLoginCookieName = "__Host-private-gateway-login"
const localRememberedLoginCookieName = "private-gateway-login"
const rememberedLoginDuration = 30 * 24 * time.Hour

func rememberedLoginCookie(r *http.Request) (string, bool) {
	host := r.Host
	if name, _, err := net.SplitHostPort(host); err == nil {
		host = name
	}
	local := host == "localhost" || host == "127.0.0.1" || host == "[::1]" || host == "::1"
	if local && r.TLS == nil && r.Header.Get("X-Forwarded-Proto") != "https" {
		return localRememberedLoginCookieName, false
	}
	return rememberedLoginCookieName, true
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
		fail(w, 401, "invalid username or password")
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
		Code     string `json:"code"`
		Remember bool   `json:"remember"`
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
	s.finishLogin(w, r, token, pending, input.Remember)
}

func (s *Server) confirmTOTPSetup(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Code     string `json:"code"`
		Remember bool   `json:"remember"`
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
	s.finishLogin(w, r, token, pending, input.Remember)
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

func (s *Server) finishLogin(w http.ResponseWriter, r *http.Request, pendingToken string, pending *pendingLogin, remember bool) {
	session, err := adminauth.NewSessionToken()
	if err != nil {
		fail(w, 503, "authentication unavailable")
		return
	}
	var rememberedKey [32]byte
	var oldRememberedKey [32]byte
	if remember {
		remembered, err := adminauth.NewSessionToken()
		if err != nil {
			fail(w, 503, "authentication unavailable")
			return
		}
		expires := time.Now().Add(rememberedLoginDuration)
		if err := s.store.RememberAdministratorLogin(r.Context(), remembered, pending.generation, expires); err != nil {
			fail(w, 503, "authentication unavailable")
			return
		}
		rememberedKey = sha256.Sum256([]byte(remembered))
		name, secure := rememberedLoginCookie(r)
		http.SetCookie(w, &http.Cookie{Name: name, Value: remembered, Path: "/",
			Expires: expires, Secure: secure, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	} else {
		name, _ := rememberedLoginCookie(r)
		if cookie, err := r.Cookie(name); err == nil {
			oldRememberedKey = sha256.Sum256([]byte(cookie.Value))
		}
		if err := s.forgetRememberedLogin(r.Context(), r); err != nil {
			fail(w, 503, "authentication unavailable")
			return
		}
		clearRememberedLoginCookie(w, r)
	}
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	if oldRememberedKey != ([32]byte{}) {
		for id, current := range s.sessions {
			if current.rememberedKey == oldRememberedKey {
				delete(s.sessions, id)
			}
		}
	}
	delete(s.pending, pendingToken)
	now := time.Now()
	for id, current := range s.sessions {
		if now.After(current.expires) || len(s.sessions) >= 32 {
			delete(s.sessions, id)
		}
	}
	s.sessions[session] = adminSession{expires: now.Add(8 * time.Hour), generation: pending.generation, rememberedKey: rememberedKey}
	respond(w, 200, map[string]string{"session": session})
}

func clearRememberedLoginCookie(w http.ResponseWriter, r *http.Request) {
	name, secure := rememberedLoginCookie(r)
	http.SetCookie(w, &http.Cookie{Name: name, Path: "/", MaxAge: -1,
		Secure: secure, HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

func (s *Server) forgetRememberedLogin(ctx context.Context, r *http.Request) error {
	name, _ := rememberedLoginCookie(r)
	cookie, err := r.Cookie(name)
	if err != nil || cookie.Value == "" {
		return nil
	}
	return s.store.ForgetAdministratorLogin(ctx, cookie.Value)
}

// restoreSession восстанавливает короткий сеанс только по действующему ключу браузера.
func (s *Server) restoreSession(w http.ResponseWriter, r *http.Request) {
	name, _ := rememberedLoginCookie(r)
	cookie, err := r.Cookie(name)
	if err != nil || cookie.Value == "" {
		fail(w, 401, "unauthorized")
		return
	}
	generation, valid, err := s.store.AdministratorRememberedLogin(r.Context(), cookie.Value)
	if err != nil {
		fail(w, 503, "authentication unavailable")
		return
	}
	if !valid {
		clearRememberedLoginCookie(w, r)
		fail(w, 401, "unauthorized")
		return
	}
	session, err := adminauth.NewSessionToken()
	if err != nil {
		fail(w, 503, "authentication unavailable")
		return
	}
	s.loginMu.Lock()
	now := time.Now()
	for id, current := range s.sessions {
		if now.After(current.expires) || len(s.sessions) >= 32 {
			delete(s.sessions, id)
		}
	}
	s.sessions[session] = adminSession{expires: now.Add(8 * time.Hour), generation: generation,
		rememberedKey: sha256.Sum256([]byte(cookie.Value))}
	s.loginMu.Unlock()
	respond(w, 200, map[string]string{"session": session})
}

func loginClient(r *http.Request) string {
	// reverse_proxy в Caddy перезаписывает недоверенный входящий X-Forwarded-For.
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
	if err := s.revokeBrowserPush(r.Context(), r.Header.Get("X-Gateway-Push-Hash")); err != nil {
		if errors.Is(err, storage.ErrInvalid) {
			fail(w, 400, "invalid browser push fingerprint")
		} else {
			fail(w, 503, "cannot revoke browser push; retry logout")
		}
		return
	}
	if err := s.revokeSessionPush(r.Context(), value); err != nil {
		fail(w, 503, "cannot revoke push subscriptions; retry logout")
		return
	}
	if err := s.forgetRememberedLogin(r.Context(), r); err != nil {
		fail(w, 503, "authentication unavailable")
		return
	}
	var rememberedKey [32]byte
	name, _ := rememberedLoginCookie(r)
	if cookie, err := r.Cookie(name); err == nil {
		rememberedKey = sha256.Sum256([]byte(cookie.Value))
	}
	s.loginMu.Lock()
	delete(s.sessions, value)
	if rememberedKey != ([32]byte{}) {
		for id, session := range s.sessions {
			if session.rememberedKey == rememberedKey {
				delete(s.sessions, id)
			}
		}
	}
	for ticket, session := range s.browserTickets {
		if session == value {
			delete(s.browserTickets, ticket)
		}
	}
	s.loginMu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: browserCookieName, Path: "/browser", MaxAge: -1,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	clearRememberedLoginCookie(w, r)
	respond(w, 200, map[string]string{"status": "logged out"})
}

// Cookie браузера — отдельный непрозрачный билет, привязанный к уже подтверждённому сеансу
// администратора. Никакую другую операцию API он не разрешает.
func (s *Server) issueBrowserCookie(w http.ResponseWriter, r *http.Request) error {
	admin, err := s.administrator(r.Context())
	if err != nil {
		return err
	}
	sessionID := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	session, found := s.sessions[sessionID]
	if !found || !admin.Credentials.Configured() || session.generation != admin.Generation ||
		time.Now().After(session.expires) {
		return nil // Внутренние запросы CLI билет браузера не получают.
	}
	var ticket string
	for candidate, parent := range s.browserTickets {
		current, active := s.sessions[parent]
		if !active || time.Now().After(current.expires) {
			delete(s.browserTickets, candidate)
		} else if parent == sessionID {
			ticket = candidate
		}
	}
	if ticket == "" {
		if len(s.browserTickets) >= 64 {
			for oldest := range s.browserTickets {
				delete(s.browserTickets, oldest)
				break
			}
		}
		ticket, err = adminauth.NewSessionToken()
		if err != nil {
			return err
		}
		s.browserTickets[ticket] = sessionID
	}
	http.SetCookie(w, &http.Cookie{Name: browserCookieName, Value: ticket, Path: "/browser",
		Expires: session.expires, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	return nil
}

func (s *Server) browserAuthorization(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(browserCookieName)
	if err != nil || cookie.Value == "" {
		fail(w, 403, "browser access denied")
		return
	}
	admin, err := s.administrator(r.Context())
	if err != nil {
		fail(w, 503, "browser access unavailable")
		return
	}
	s.loginMu.Lock()
	parent := s.browserTickets[cookie.Value]
	session, found := s.sessions[parent]
	valid := parent != "" && found && admin.Credentials.Configured() &&
		session.generation == admin.Generation && time.Now().Before(session.expires)
	if !valid {
		delete(s.browserTickets, cookie.Value)
	}
	s.loginMu.Unlock()
	if !valid {
		fail(w, 403, "browser access denied")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
