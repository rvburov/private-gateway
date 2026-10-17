package httpapi

import (
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rvburov/private-gateway/internal/adminauth"
)

func TestAdministratorBrowserEnrollmentAndDatabasePersistence(t *testing.T) {
	store, pool, _, _ := integrationStore(t)
	salt := []byte("0123456789abcdef")
	hash, err := pbkdf2.Key(sha256.New, "a sufficiently long password", salt, 600000, 32)
	if err != nil {
		t.Fatal(err)
	}
	passwordHash := "pbkdf2_sha256:600000:" + hex.EncodeToString(salt) + ":" + hex.EncodeToString(hash)
	// При переносе учётной записи из env переносится только хеш пароля: раскрытый прежний
	// секрет TOTP после этого не даёт войти.
	if err := store.ImportAdministrator(t.Context(), "admin@example.com", passwordHash); err != nil {
		t.Fatal(err)
	}
	const cliToken = "internal-cli-token-at-least-thirty-two-characters"
	handler := New(store, nil, &testBrowser{}, Options{Token: cliToken})
	call := func(method, path, body, bearer, remote string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.RemoteAddr = remote
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	const remote = "192.0.2.1:1234"
	if got := call("GET", "/api/v1/auth/config", "", "", remote).Code; got != 404 {
		t.Fatalf("public administrator setup status: %d", got)
	}
	if got := call("GET", "/api/v1/metrics", "", cliToken, remote).Code; got != 401 {
		t.Fatalf("external admin token: %d", got)
	}
	if got := call("GET", "/api/v1/metrics", "", cliToken, "127.0.0.1:1234").Code; got != 200 {
		t.Fatalf("internal CLI token: %d", got)
	}
	if got := call("POST", "/api/v1/auth/login", `{"username":"admin@example.com","password":"wrong"}`, "", remote).Code; got != 401 {
		t.Fatalf("bad password: %d", got)
	}
	loginBody := `{"username":"admin@example.com","password":"a sufficiently long password"}`
	first := call("POST", "/api/v1/auth/login", loginBody, "", remote)
	var pending struct{ Status, Pending string }
	if first.Code != 200 || json.Unmarshal(first.Body.Bytes(), &pending) != nil || pending.Status != "totp_setup_required" || len(pending.Pending) != 64 {
		t.Fatalf("first password step: %d %s", first.Code, first.Body.String())
	}
	if got := call("GET", "/api/v1/metrics", "", pending.Pending, remote).Code; got != 401 {
		t.Fatalf("pending token accessed API: %d", got)
	}
	if got := call("POST", "/api/v1/auth/2fa/setup", "", "", remote).Code; got != 401 {
		t.Fatalf("anonymous authenticator setup: %d", got)
	}
	setup := call("POST", "/api/v1/auth/2fa/setup", "", pending.Pending, remote)
	var details struct {
		Secret string `json:"secret"`
		URI    string `json:"otpauth_uri"`
	}
	if setup.Code != 200 || json.Unmarshal(setup.Body.Bytes(), &details) != nil || details.Secret == "" || !strings.Contains(details.URI, "admin%40example.com") {
		t.Fatalf("browser setup: %d %s", setup.Code, setup.Body.String())
	}
	if got := call("POST", "/api/v1/auth/2fa/setup/confirm", `{"code":"000000"}`, pending.Pending, remote).Code; got != 401 {
		t.Fatalf("bad enrollment code: %d", got)
	}
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(details.Secret)
	if err != nil {
		t.Fatal(err)
	}
	code := adminauth.Code(secret, time.Now().Unix()/30)
	confirmed := call("POST", "/api/v1/auth/2fa/setup/confirm", `{"code":"`+code+`"}`, pending.Pending, remote)
	var session struct{ Session string }
	if confirmed.Code != 200 || json.Unmarshal(confirmed.Body.Bytes(), &session) != nil || len(session.Session) != 64 {
		t.Fatalf("enrollment: %d %s", confirmed.Code, confirmed.Body.String())
	}
	var ciphertext []byte
	if err = pool.QueryRow(t.Context(), "SELECT totp_secret_ciphertext FROM administrator_credentials WHERE id=1").Scan(&ciphertext); err != nil || len(ciphertext) == 0 || strings.Contains(string(ciphertext), details.Secret) {
		t.Fatal("TOTP secret was not stored encrypted")
	}
	if got := call("GET", "/api/v1/metrics", "", session.Session, remote).Code; got != 200 {
		t.Fatalf("administrator session: %d", got)
	}
	browserStatus := call("GET", "/api/v1/browser", "", session.Session, remote)
	if browserStatus.Code != 200 {
		t.Fatalf("browser status: %d", browserStatus.Code)
	}
	var browserCookie *http.Cookie
	for _, cookie := range browserStatus.Result().Cookies() {
		if cookie.Name == browserCookieName {
			browserCookie = cookie
		}
	}
	if browserCookie == nil || browserCookie.Value == session.Session || !browserCookie.Secure ||
		!browserCookie.HttpOnly || browserCookie.Path != "/browser" || browserCookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("browser ticket was not scoped and protected")
	}
	browserAccess := func() int {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/browser-authorization", nil)
		req.AddCookie(browserCookie)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w.Code
	}
	if got := call("GET", "/browser-authorization", "", "", remote).Code; got != 403 {
		t.Fatalf("anonymous browser access: %d", got)
	}
	if got := browserAccess(); got != 204 {
		t.Fatalf("administrator browser access: %d", got)
	}
	if cli := call("GET", "/api/v1/browser", "", cliToken, "127.0.0.1:1234"); cli.Code != 200 ||
		len(cli.Result().Cookies()) != 0 {
		t.Fatal("internal CLI token received browser access")
	}
	if got := call("POST", "/api/v1/auth/logout", "", session.Session, remote).Code; got != 200 {
		t.Fatalf("logout: %d", got)
	}
	if got := browserAccess(); got != 403 {
		t.Fatalf("browser access survived logout: %d", got)
	}
	// Новый процесс API должен прочитать из PostgreSQL зашифрованный секрет и шаг последнего кода.
	handler = New(store, nil, &testBrowser{}, Options{Token: cliToken})
	second := call("POST", "/api/v1/auth/login", loginBody, "", remote)
	if second.Code != 200 || json.Unmarshal(second.Body.Bytes(), &pending) != nil || pending.Status != "totp_required" {
		t.Fatalf("second login: %d %s", second.Code, second.Body.String())
	}
	if got := call("POST", "/api/v1/auth/2fa", `{"code":"`+code+`"}`, pending.Pending, remote).Code; got != 401 {
		t.Fatalf("replayed TOTP: %d", got)
	}
	code = adminauth.Code(secret, time.Now().Unix()/30+1)
	var nextSession struct{ Session string }
	verified := call("POST", "/api/v1/auth/2fa", `{"code":"`+code+`"}`, pending.Pending, remote)
	if verified.Code != 200 || json.Unmarshal(verified.Body.Bytes(), &nextSession) != nil {
		t.Fatalf("new TOTP after restart: %d", verified.Code)
	}
	for _, cookie := range call("GET", "/api/v1/browser", "", nextSession.Session, remote).Result().Cookies() {
		if cookie.Name == browserCookieName {
			browserCookie = cookie
		}
	}
	if got := browserAccess(); got != 204 {
		t.Fatalf("browser access after second login: %d", got)
	}
	snapshot, err := store.SnapshotAdministrator(t.Context())
	if err != nil || !snapshot.Exists || len(snapshot.TOTPSecretCiphertext) == 0 {
		t.Fatal("administrator backup is incomplete", err)
	}
	if err := store.SetAdministrator(t.Context(), "admin@example.com", passwordHash); err != nil {
		t.Fatal(err)
	}
	if got := call("GET", "/api/v1/metrics", "", nextSession.Session, remote).Code; got != 401 {
		t.Fatalf("old session survived password reset: %d", got)
	}
	if got := browserAccess(); got != 403 {
		t.Fatalf("browser access survived password reset: %d", got)
	}
	third := call("POST", "/api/v1/auth/login", loginBody, "", remote)
	if third.Code != 200 || json.Unmarshal(third.Body.Bytes(), &pending) != nil || pending.Status != "totp_setup_required" {
		t.Fatalf("new enrollment after reset: %d %s", third.Code, third.Body.String())
	}
	restored, err := store.RestoreAdministrator(t.Context(), snapshot)
	if err != nil || !restored {
		t.Fatal("administrator rollback failed", err)
	}
	fourth := call("POST", "/api/v1/auth/login", loginBody, "", remote)
	if fourth.Code != 200 || json.Unmarshal(fourth.Body.Bytes(), &pending) != nil || pending.Status != "totp_required" {
		t.Fatalf("administrator rollback did not restore prior 2FA: %d %s", fourth.Code, fourth.Body.String())
	}
}

func TestCLITokenOnlyWorksOnLoopbackBeforeSetup(t *testing.T) {
	handler := New(nil, nil, nil, Options{Token: strings.Repeat("x", 64)})
	login := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"admin","password":"test"}`))
	login.Header.Set("Content-Type", "application/json")
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, login)
	if loginResponse.Code != 401 || strings.Contains(loginResponse.Body.String(), "configured") {
		t.Fatalf("login disclosed administrator setup status: %d %s", loginResponse.Code, loginResponse.Body.String())
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil)
	req.RemoteAddr = "192.0.2.1:1234"
	req.Header.Set("X-Forwarded-For", "127.0.0.1")
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("x", 64))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("public CLI token access: %d", w.Code)
	}
	req.RemoteAddr = "127.0.0.1:1234"
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("local CLI token access: %d", w.Code)
	}
}
