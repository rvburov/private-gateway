package httpapi

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rvburov/private-gateway/internal/browser"
	"github.com/rvburov/private-gateway/internal/model"
	"github.com/rvburov/private-gateway/internal/secure"
	"github.com/rvburov/private-gateway/internal/storage"
	"github.com/rvburov/private-gateway/internal/xray"
)

type testXray struct {
	clients []xray.Client
	fail    bool
	applies int
}

func (x *testXray) Validate(_ context.Context, c []xray.Client) error { x.clients = c; return nil }
func (x *testXray) Apply(_ context.Context, c []xray.Client) error {
	x.applies++
	if x.fail {
		return errors.New("restart failed")
	}
	x.clients = c
	return nil
}

type disconnectXray struct {
	started  chan struct{}
	release  chan struct{}
	finished chan struct{}
}

func (x *disconnectXray) Validate(context.Context, []xray.Client) error { return nil }
func (x *disconnectXray) Apply(ctx context.Context, _ []xray.Client) error {
	close(x.started)
	<-x.release
	defer close(x.finished)
	return ctx.Err()
}

type testBrowser struct{ running bool }

func (b *testBrowser) Status(context.Context) (browser.Status, error) {
	return browser.Status{Running: b.running}, nil
}
func (b *testBrowser) Start(context.Context) (browser.Status, error) {
	b.running = true
	return browser.Status{Running: true}, nil
}
func (b *testBrowser) Stop(context.Context) (browser.Status, error) {
	b.running = false
	return browser.Status{}, nil
}

func integrationStore(t *testing.T) (*storage.Store, *pgxpool.Pool, model.Node, string) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; PostgreSQL integration not run")
	}
	ctx := context.Background()
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("connect test PostgreSQL:", err)
	}
	schema := "gateway_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		base.Close()
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	isolated := u.String()
	t.Cleanup(func() {
		_, _ = base.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		base.Close()
	})
	if err = storage.Migrate(isolated); err != nil {
		t.Fatal(err)
	}
	if err = storage.Migrate(isolated); err != nil {
		t.Fatal("migration repeat failed:", err)
	}
	cipher, err := secure.New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(ctx, isolated, cipher)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	pool, err := pgxpool.New(ctx, isolated)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	node := model.Node{ID: storage.LocalNodeID, Name: "test", Host: "vpn.example.com", Port: 443, ServerName: "www.microsoft.com", PublicKey: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), ShortID: "0011223344556677", ConfigHash: "initial"}
	if err = store.EnsureNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	return store, pool, node, isolated
}

func TestDeviceLifecyclePostgreSQL(t *testing.T) {
	store, pool, node, dsn := integrationStore(t)
	engine := &testXray{}
	worker := &testBrowser{}
	const token = "integration-token-at-least-thirty-two-characters"
	server := httptest.NewServer(New(store, engine, worker, Options{Token: token, XrayConfigured: true}))
	defer server.Close()
	request := func(method, path, body, auth string) (int, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("sensitive responses can be cached")
		}
		return response.StatusCode, data
	}
	for _, auth := range []string{"", "incorrect"} {
		code, _ := request("GET", "/api/v1/devices", "", auth)
		if code != 401 {
			t.Fatalf("unauthorized=%d", code)
		}
		code, _ = request("DELETE", "/api/v1/devices/00000000-0000-4000-8000-000000000002", "", auth)
		if code != 401 {
			t.Fatalf("unauthorized delete=%d", code)
		}
	}
	for _, path := range []string{"/healthz", "/readyz"} {
		code, _ := request("GET", path, "", "")
		if code != 200 {
			t.Fatalf("%s: %d", path, code)
		}
	}
	code, data := request("POST", "/api/v1/devices", `{"name":"MacBook"}`, token)
	if code != 201 {
		t.Fatalf("create=%d %s", code, data)
	}
	var device model.Device
	if err := json.Unmarshal(data, &device); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("credential")) {
		t.Fatal("credential exposed in device response")
	}
	code, _ = request("POST", "/api/v1/devices", `{"name":"MacBook"}`, token)
	if code != 409 {
		t.Fatalf("duplicate=%d", code)
	}
	for _, body := range []string{`{"name":""}`, `{"name":"x","unknown":true}`, `{"name":"x"} {}`, `{"name":"x","node_id":"bad"}`} {
		code, _ = request("POST", "/api/v1/devices", body, token)
		if code != 400 {
			t.Fatalf("invalid input %s = %d", body, code)
		}
	}
	code, data = request("GET", "/api/v1/devices/"+device.ID+"/profile", "", token)
	if code != 200 {
		t.Fatalf("profile=%d %s", code, data)
	}
	var profile model.Profile
	_ = json.Unmarshal(data, &profile)
	uri, err := url.Parse(profile.URI)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(profile.SingBoxJSON, uri.User.Username()) || !strings.Contains(profile.MihomoYAML, uri.User.Username()) {
		t.Fatal("client formats do not contain the exported device credential")
	}
	credential := uri.User.Username()
	code, _ = request("GET", "/api/v1/devices/"+strings.ToUpper(device.ID)+"/profile", "", token)
	if code != 200 {
		t.Fatal("canonical UUID variant cannot be exported")
	}
	if uri.Scheme != "vless" || uri.Query().Get("security") != "reality" || uri.Query().Get("pbk") != node.PublicKey {
		t.Fatal("invalid import profile")
	}
	var encrypted []byte
	if err = pool.QueryRow(context.Background(), "SELECT credential_ciphertext FROM devices WHERE id=$1", device.ID).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, []byte(credential)) {
		t.Fatal("plaintext credential stored in PostgreSQL")
	}
	code, data = request("GET", "/api/v1/devices", "", token)
	if code != 200 || bytes.Contains(data, []byte(credential)) {
		t.Fatal("device listing leaks credential")
	}
	code, _ = request("DELETE", "/api/v1/devices/"+device.ID, "", token)
	if code != 409 {
		t.Fatal("active device could be deleted")
	}
	engine.fail = true
	code, _ = request("POST", "/api/v1/config/apply", "", token)
	if code != 503 {
		t.Fatalf("failed apply=%d", code)
	}
	status, err := store.Status(context.Background())
	if err != nil || status.AppliedRevision != 0 {
		t.Fatal("failed deployment marked applied", status, err)
	}
	engine.fail = false
	code, _ = request("POST", "/api/v1/config/validate", "", token)
	if code != 200 || len(engine.clients) != 1 || engine.clients[0].ID != credential {
		t.Fatal("active client not rendered")
	}
	code, _ = request("POST", "/api/v1/config/apply", "", token)
	if code != 200 {
		t.Fatal("apply failed")
	}
	status, _ = store.Status(context.Background())
	if status.AppliedRevision != status.DesiredRevision {
		t.Fatal("successful deployment revision not saved")
	}
	beforeApplies := engine.applies
	code, _ = request("POST", "/api/v1/config/apply", "", token)
	if code != 200 || engine.applies != beforeApplies {
		t.Fatal("reapplying the current revision restarted Xray")
	}
	before := status.DesiredRevision
	if err = store.EnsureNode(context.Background(), node); err != nil {
		t.Fatal(err)
	}
	status, _ = store.Status(context.Background())
	if status.DesiredRevision != before {
		t.Fatal("unchanged restart changed revision")
	}
	wrongCipher, _ := secure.New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)))
	wrong, err := storage.Open(context.Background(), dsn, wrongCipher)
	if err != nil {
		t.Fatal(err)
	}
	if err = wrong.EnsureNode(context.Background(), node); err == nil {
		t.Fatal("wrong encryption key accepted")
	}
	wrong.Close()
	code, data = request("POST", "/api/v1/devices/"+device.ID+"/revoke", "", token)
	if code != 200 {
		t.Fatalf("revoke=%d %s", code, data)
	}
	code, _ = request("GET", "/api/v1/devices/"+device.ID+"/profile", "", token)
	if code != 410 {
		t.Fatalf("revoked export=%d", code)
	}
	status, _ = store.Status(context.Background())
	if status.AppliedRevision >= status.DesiredRevision {
		t.Fatal("revocation not marked pending")
	}
	code, _ = request("DELETE", "/api/v1/devices/"+device.ID, "", token)
	if code != 409 {
		t.Fatal("device could be deleted before revocation reached Xray")
	}
	before = status.DesiredRevision
	code, _ = request("POST", "/api/v1/devices/"+device.ID+"/revoke", "", token)
	if code != 200 {
		t.Fatal("revoke not idempotent")
	}
	status, _ = store.Status(context.Background())
	if status.DesiredRevision != before {
		t.Fatal("repeat revoke changed revision")
	}
	code, _ = request("POST", "/api/v1/config/apply", "", token)
	if code != 200 || len(engine.clients) != 0 {
		t.Fatal("revoked client still in deployment")
	}
	status, _ = store.Status(context.Background())
	code, _ = request("DELETE", "/api/v1/devices/"+device.ID, "", token)
	if code != 200 {
		t.Fatalf("delete revoked device=%d", code)
	}
	code, data = request("GET", "/api/v1/devices", "", token)
	if code != 200 || bytes.Contains(data, []byte(device.ID)) {
		t.Fatal("deleted device is still listed")
	}
	current, _ := store.Status(context.Background())
	if current.Devices != 0 || current.DesiredRevision != status.DesiredRevision {
		t.Fatal("deleting a revoked device changed active Xray configuration")
	}
	code, _ = request("DELETE", "/api/v1/devices/"+device.ID, "", token)
	if code != 404 {
		t.Fatal("deleted device could be removed twice")
	}
	code, data = request("GET", "/api/v1/audit", "", token)
	if code != 200 || bytes.Contains(data, []byte(credential)) || !bytes.Contains(data, []byte(`"action":"device.delete","device_id":"`+device.ID+`"`)) {
		t.Fatal("audit leaks credential")
	}
	if _, err = pool.Exec(context.Background(), "DELETE FROM audit_events"); err == nil {
		t.Fatal("audit history can be deleted")
	}
	for _, op := range []string{"start", "stop"} {
		code, _ = request("POST", "/api/v1/browser/"+op, "", token)
		if code != 200 {
			t.Fatal("browser endpoint failed")
		}
	}
	node.ConfigHash = "changed-settings"
	sameCipher, _ := secure.New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)))
	second, err := storage.Open(context.Background(), dsn, sameCipher)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err = second.EnsureNode(context.Background(), node); err != nil {
		t.Fatal(err)
	}
	status, _ = store.Status(context.Background())
	if status.DesiredRevision == status.AppliedRevision {
		t.Fatal("settings change not marked pending")
	}
	invoked := false
	err = store.Configure(context.Background(), true, func(context.Context, []xray.Client) error { invoked = true; return nil })
	if !errors.Is(err, storage.ErrStaleConfig) || invoked {
		t.Fatal("stale process can apply current revision", err)
	}
}

func TestApplyCommitsAfterVPNCallerDisconnects(t *testing.T) {
	store, _, node, _ := integrationStore(t)
	if _, err := store.Create(context.Background(), "Phone", node.ID); err != nil {
		t.Fatal(err)
	}
	engine := &disconnectXray{started: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
	const token = "integration-token-at-least-thirty-two-characters"
	server := httptest.NewServer(New(store, engine, &testBrowser{}, Options{Token: token, XrayConfigured: true}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/v1/config/apply", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		response, err := server.Client().Do(request)
		if err == nil {
			response.Body.Close()
		}
	}()
	select {
	case <-engine.started:
	case <-time.After(5 * time.Second):
		t.Fatal("Xray apply did not start")
	}
	cancel() // перезапуск Xray может оборвать собственный VPN администратора
	close(engine.release)
	select {
	case <-engine.finished:
	case <-time.After(5 * time.Second):
		t.Fatal("Xray apply did not finish after caller disconnected")
	}
	select {
	case <-clientDone:
	case <-time.After(5 * time.Second):
		t.Fatal("disconnected client did not exit")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, err := store.Status(context.Background())
		if err == nil && status.AppliedRevision == status.DesiredRevision {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("Xray apply completed but the desired revision was not committed")
}

func TestDeviceTrafficMapsPrivateCredentialToDevice(t *testing.T) {
	store, _, node, _ := integrationStore(t)
	device, err := store.Create(context.Background(), "Other person", node.ID)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.Export(context.Background(), device.ID)
	if err != nil {
		t.Fatal(err)
	}
	uri, err := url.Parse(profile.URI)
	if err != nil {
		t.Fatal(err)
	}
	credential := uri.User.Username()
	path := t.TempDir() + "/xray-stats"
	output := `{"stat":[{"name":"user>>>` + credential + `@private-gateway>>>traffic>>>uplink","value":"1024"},{"name":"user>>>` + credential + `@private-gateway>>>traffic>>>downlink","value":"2048"}]}`
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' '"+output+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	const token = "private-integration-token-at-least-32-characters"
	server := httptest.NewServer(New(store, &testXray{}, &testBrowser{}, Options{
		Token: token, StatsAddress: "xray:10085", StatsBinary: path,
	}))
	defer server.Close()
	request, _ := http.NewRequest(http.MethodGet, server.URL+"/api/v1/devices/traffic", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Devices map[string]xray.Traffic `json:"devices"`
	}
	if response.StatusCode != 200 || json.Unmarshal(data, &result) != nil ||
		result.Devices[device.ID].UplinkBytes != 1024 ||
		result.Devices[device.ID].DownlinkBytes != 2048 || bytes.Contains(data, []byte(credential)) {
		t.Fatalf("device traffic response is invalid or exposes a credential: status %d", response.StatusCode)
	}
}

func TestConcurrentDeviceCreationPostgreSQL(t *testing.T) {
	store, _, _, _ := integrationStore(t)
	var group sync.WaitGroup
	var mu sync.Mutex
	created, conflicts := 0, 0
	for range 12 {
		group.Add(1)
		go func() {
			defer group.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, err := store.Create(ctx, "same-name", storage.LocalNodeID)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				created++
			case errors.Is(err, storage.ErrConflict):
				conflicts++
			default:
				t.Errorf("concurrent create: %v", err)
			}
		}()
	}
	group.Wait()
	if created != 1 || conflicts != 11 {
		t.Fatalf("created=%d conflicts=%d", created, conflicts)
	}
	status, err := store.Status(context.Background())
	if err != nil || status.Devices != 1 || status.DesiredRevision != 2 {
		t.Fatalf("inconsistent committed state: %+v %v", status, err)
	}
}
