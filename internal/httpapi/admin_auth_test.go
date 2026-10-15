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

func TestAdministratorLoginUsesPasswordAndOneTimeCode(t *testing.T) {
	secret := []byte("12345678901234567890")
	salt := []byte("0123456789abcdef")
	hash, err := pbkdf2.Key(sha256.New, "a sufficiently long password", salt, 600000, 32)
	if err != nil {
		t.Fatal(err)
	}
	credentials := adminauth.Credentials{Username: "admin", PasswordHash: "pbkdf2_sha256:600000:" + hex.EncodeToString(salt) + ":" + hex.EncodeToString(hash), TOTPSecret: base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret)}
	handler := New(nil, nil, nil, Options{Token: strings.Repeat("x", 64), Admin: credentials})
	call := func(method, path, body, bearer, remote string) *httptest.ResponseRecorder {
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
	if got := call("GET", "/api/v1/auth/config", "", "", "192.0.2.1:1234").Body.String(); !strings.Contains(got, `"configured":true`) {
		t.Fatal(got)
	}
	if got := call("GET", "/api/v1/metrics", "", strings.Repeat("x", 64), "192.0.2.1:1234").Code; got != 401 {
		t.Fatalf("external admin token: %d", got)
	}
	if got := call("GET", "/api/v1/metrics", "", strings.Repeat("x", 64), "127.0.0.1:1234").Code; got != 200 {
		t.Fatalf("internal CLI token: %d", got)
	}
	code := adminauth.Code(secret, time.Now().Unix()/30)
	loginBody := `{"username":"admin","password":"a sufficiently long password","code":"` + code + `"}`
	if got := call("POST", "/api/v1/auth/login", `{"username":"admin","password":"bad","code":"`+code+`"}`, "", "192.0.2.1:1234").Code; got != 401 {
		t.Fatalf("bad password: %d", got)
	}
	response := call("POST", "/api/v1/auth/login", loginBody, "", "192.0.2.1:1234")
	if response.Code != 200 {
		t.Fatalf("login: %d %s", response.Code, response.Body.String())
	}
	var session struct {
		Session string `json:"session"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &session); err != nil || len(session.Session) != 64 {
		t.Fatal("invalid session response")
	}
	if got := call("POST", "/api/v1/auth/login", loginBody, "", "192.0.2.1:1234").Code; got != 401 {
		t.Fatalf("replayed TOTP: %d", got)
	}
	if got := call("GET", "/api/v1/metrics", "", session.Session, "192.0.2.1:1234").Code; got != 200 {
		t.Fatalf("session: %d", got)
	}
	if got := call("POST", "/api/v1/auth/logout", "", session.Session, "192.0.2.1:1234").Code; got != 200 {
		t.Fatalf("logout: %d", got)
	}
	if got := call("GET", "/api/v1/metrics", "", session.Session, "192.0.2.1:1234").Code; got != 401 {
		t.Fatalf("revoked session: %d", got)
	}
}

func TestLegacyTokenRemainsUntilSetup(t *testing.T) {
	handler := New(nil, nil, nil, Options{Token: strings.Repeat("x", 64)})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil)
	req.RemoteAddr = "192.0.2.1:1234"
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("x", 64))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("legacy access: %d", w.Code)
	}
}
