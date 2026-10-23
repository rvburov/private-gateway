package httpapi

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPWAAssetsUseCorrectTypesWithoutCachingPrivateResponses(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"index.html":           "<!doctype html><title>Private Gateway</title>",
		"manifest.webmanifest": `{"name":"Private Gateway","start_url":"/"}`,
		"sw.js":                "self.addEventListener('fetch', () => {});",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	handler := New(nil, nil, nil, Options{WebDir: dir})
	for _, item := range []struct{ path, contentType string }{
		{"/manifest.webmanifest", "application/manifest+json"},
		{"/sw.js", "text/javascript"},
	} {
		t.Run(item.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest("GET", item.path, nil))
			if recorder.Code != 200 || !strings.HasPrefix(recorder.Header().Get("Content-Type"), item.contentType) {
				t.Fatalf("PWA asset status/type = %d %q", recorder.Code, recorder.Header().Get("Content-Type"))
			}
			if recorder.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("global private-response cache policy changed")
			}
			policy := recorder.Header().Get("Content-Security-Policy")
			if !strings.Contains(policy, "worker-src 'self'") || !strings.Contains(policy, "manifest-src 'self'") {
				t.Fatalf("PWA origin restriction missing: %s", policy)
			}
			if item.path == "/sw.js" && recorder.Header().Get("Service-Worker-Allowed") != "/" {
				t.Fatal("service worker scope missing")
			}
		})
	}
	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest("GET", "/missing-worker.js", nil))
	if missing.Code != 404 || strings.Contains(missing.Body.String(), "<title>Private Gateway</title>") {
		t.Fatal("missing static asset must not become an application shell")
	}
}
