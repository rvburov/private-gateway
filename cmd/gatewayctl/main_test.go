package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func executeCLI(serverURL, token string, client *http.Client, args ...string) (string, string, error) {
	command := newRootCommand(func(name string) string {
		if name == "GATEWAY_API_URL" {
			return serverURL
		}
		if name == "GATEWAY_API_TOKEN" {
			return token
		}
		return ""
	}, client)
	var out, stderr bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&stderr)
	command.SetArgs(args)
	err := command.Execute()
	return out.String(), stderr.String(), err
}

func TestStatusAuthenticationAndPendingRevision(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/status" || r.Method != http.MethodGet {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Error("authorization header missing")
		}
		fmtJSON(w, `{"database":"ok","node_id":"node-1","devices":2,"active_devices":1,"desired_revision":3,"applied_revision":2,"xray_configured":true}`)
	}))
	defer server.Close()
	out, stderr, err := executeCLI(server.URL, "test-secret", server.Client(), "status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "pending") || !strings.Contains(out, "node-1") {
		t.Fatalf("missing status fields: %s", out)
	}
	if strings.Contains(out+stderr, "test-secret") {
		t.Fatal("credential leaked in CLI output")
	}
}

func TestDeviceAddSendsNameAndNode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/devices" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["name"] != "My phone" || body["node_id"] != "node-1" {
			t.Errorf("unexpected payload: %#v", body)
		}
		w.WriteHeader(http.StatusCreated)
		fmtJSON(w, `{"id":"device-1","name":"My phone","node_id":"node-1"}`)
	}))
	defer server.Close()
	out, stderr, err := executeCLI(server.URL, "secret", server.Client(), "device", "add", " My phone ", "--node", "node-1")
	if err != nil || out != "device-1\n" || !strings.Contains(stderr, "config apply") {
		t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
	}
}

func TestDeviceExportPrintsOnlySensitiveURI(t *testing.T) {
	const uri = "vless://private-device-credential@example.com:443?security=reality#phone"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/devices/device-1/profile" || r.Method != http.MethodGet {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"uri": uri})
	}))
	defer server.Close()
	out, stderr, err := executeCLI(server.URL, "secret", server.Client(), "device", "export", "device-1")
	if err != nil || out != uri+"\n" || stderr != "" {
		t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
	}
}

func TestDeviceListIgnoresCredentialFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmtJSON(w, `{"devices":[{"id":"device-1","name":"Phone","node_id":"n","created_at":"2026-01-01T00:00:00Z","revoked_at":null,"credential":"never-print-this","uri":"also-secret"},{"id":"device-2","name":"Old","revoked_at":"2026-01-02T00:00:00Z"}]}`)
	}))
	defer server.Close()
	out, _, err := executeCLI(server.URL, "secret", server.Client(), "device", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "active") || !strings.Contains(out, "revoked") || strings.Contains(out, "never-print-this") || strings.Contains(out, "also-secret") {
		t.Fatalf("unexpected device table: %s", out)
	}
}

func TestAuditShowsEventsWithoutCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/audit" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		fmtJSON(w, `{"events":[{"id":2,"action":"config.apply","device_id":null,"created_at":"2026-01-02T00:00:00Z"},{"id":1,"action":"device.create","device_id":"d1","created_at":"2026-01-01T00:00:00Z","credential":"private"}]}`)
	}))
	defer server.Close()
	out, _, err := executeCLI(server.URL, "secret", server.Client(), "audit")
	if err != nil || !strings.Contains(out, "config.apply") || !strings.Contains(out, "d1") || strings.Contains(out, "private") {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestAPIErrors(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"unauthorized", 401, `{"error":"secret-value"}`, "check GATEWAY_API_TOKEN"},
		{"revoked", 409, `{"error":"device has been revoked"}`, "device has been revoked"},
		{"not-json", 502, "bad gateway", "HTTP 502"},
		{"invalid-success", 200, "not json", "invalid JSON"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				fmtJSON(w, test.body)
			}))
			defer server.Close()
			out, _, err := executeCLI(server.URL, "secret-value", server.Client(), "device", "export", "device-1")
			if err == nil || !strings.Contains(err.Error(), test.want) || out != "" || strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("out=%q err=%v", out, err)
			}
		})
	}
}

func TestMissingTokenDoesNotContactServer(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	_, _, err := executeCLI(server.URL, "", server.Client(), "status")
	if err == nil || !strings.Contains(err.Error(), "GATEWAY_API_TOKEN") || called {
		t.Fatalf("called=%t err=%v", called, err)
	}
}

func TestTimeoutAndURLValidation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(30 * time.Millisecond)
		fmtJSON(w, `{}`)
	}))
	defer server.Close()
	client := server.Client()
	client.Timeout = time.Millisecond
	_, _, err := executeCLI(server.URL, "secret", client, "status")
	if err == nil || !strings.Contains(err.Error(), "could not reach gateway API") {
		t.Fatalf("unexpected timeout error: %v", err)
	}
	for _, address := range []string{"ftp://localhost", "http://user:password@localhost", "http://localhost?token=secret", "http://localhost#fragment"} {
		_, _, err = executeCLI(address, "secret", client, "status")
		if err == nil || !strings.Contains(err.Error(), "API URL") {
			t.Fatalf("unexpected URL validation result for %q: %v", address, err)
		}
	}
}

func TestLifecycleMethods(t *testing.T) {
	for _, test := range []struct {
		args   []string
		method string
		path   string
		body   string
	}{
		{[]string{"config", "validate"}, http.MethodPost, "/api/v1/config/validate", `{"status":"valid"}`},
		{[]string{"config", "apply"}, http.MethodPost, "/api/v1/config/apply", `{"status":"applied"}`},
		{[]string{"browser", "status"}, http.MethodGet, "/api/v1/browser", `{"running":false,"url":"http://127.0.0.1:7900"}`},
		{[]string{"browser", "start"}, http.MethodPost, "/api/v1/browser/start", `{"running":true,"url":"http://127.0.0.1:7900"}`},
		{[]string{"browser", "stop"}, http.MethodPost, "/api/v1/browser/stop", `{"running":false,"url":"http://127.0.0.1:7900"}`},
		{[]string{"device", "revoke", "d1"}, http.MethodPost, "/api/v1/devices/d1/revoke", `{"id":"d1","revoked_at":"2026-01-01T00:00:00Z"}`},
	} {
		t.Run(strings.Join(test.args, "-"), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != test.method || r.URL.Path != test.path {
					t.Errorf("request=%s %s; want %s %s", r.Method, r.URL.Path, test.method, test.path)
				}
				fmtJSON(w, test.body)
			}))
			defer server.Close()
			_, _, err := executeCLI(server.URL, "secret", server.Client(), test.args...)
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func fmtJSON(w http.ResponseWriter, body string) {
	_, _ = w.Write([]byte(body))
}
