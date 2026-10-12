package browser

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRemoteManagerControlsPrivateWorker(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	running := false
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /status":
		case "POST /start":
			running = true
		case "POST /stop":
			running = false
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"running": running})
	}))
	defer worker.Close()
	manager, err := NewRemoteManager(RemoteOptions{URL: worker.URL, Token: token, PublicURL: "https://panel.example:8443/browser/vnc.html"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if status, err := manager.Status(ctx); err != nil || status.Running || status.URL != "https://panel.example:8443/browser/vnc.html" {
		t.Fatalf("initial status = %+v, %v", status, err)
	}
	if status, err := manager.Start(ctx); err != nil || !status.Running || status.URL != "https://panel.example:8443/browser/vnc.html" {
		t.Fatalf("started status = %+v, %v", status, err)
	}
	if status, err := manager.Stop(ctx); err != nil || status.Running {
		t.Fatalf("stopped status = %+v, %v", status, err)
	}
}
