package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/rvburov/private-gateway/internal/model"
	"github.com/rvburov/private-gateway/internal/storage"
	"github.com/rvburov/private-gateway/internal/xray"
)

const deviceMetadataToken = "device-metadata-admin-token-with-32-characters"

func deviceMetadataRequest(handler http.Handler, method, path, body string, auth bool) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "https://example.org:8443"+path, strings.NewReader(body))
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("Content-Type", "application/json")
	if auth {
		request.Header.Set("Authorization", "Bearer "+deviceMetadataToken)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestDeviceMetadataRequiresAuthentication(t *testing.T) {
	handler := New(nil, nil, nil, Options{Token: deviceMetadataToken})
	response := deviceMetadataRequest(handler, http.MethodPatch,
		"/api/v1/devices/00000000-0000-4000-8000-000000000001", `{"name":"New name"}`, false)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated metadata update: status=%d", response.Code)
	}
}

func TestDeviceRenamePreservesAccessPostgreSQL(t *testing.T) {
	store, pool, node, _ := integrationStore(t)
	ctx := context.Background()
	first, err := store.Create(ctx, "Original laptop", node.ID)
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.Create(ctx, "Other laptop", node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.PreferredExit != "server" || other.PreferredExit != "server" {
		t.Fatal("new devices must use the server exit")
	}
	if err := store.Configure(ctx, true, func(context.Context, []xray.Client) error { return nil }); err != nil {
		t.Fatal(err)
	}
	before, err := store.Export(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	var credential, hash []byte
	var generation int64
	if err := pool.QueryRow(ctx, "SELECT credential_ciphertext, subscription_hash, subscription_generation FROM devices WHERE id=$1", first.ID).
		Scan(&credential, &hash, &generation); err != nil {
		t.Fatal(err)
	}
	statusBefore, err := store.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	handler := New(store, &testXray{}, &testBrowser{}, Options{Token: deviceMetadataToken, PanelOrigin: "https://panel.example:8443"})
	response := deviceMetadataRequest(handler, http.MethodPatch, "/api/v1/devices/"+first.ID, `{"name":"  Рабочий ноутбук  "}`, true)
	var updated model.Device
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &updated) != nil ||
		updated.ID != first.ID || updated.NodeID != first.NodeID || !updated.CreatedAt.Equal(first.CreatedAt) ||
		updated.Name != "Рабочий ноутбук" || updated.PreferredExit != "server" {
		t.Fatal("rename altered device identity or exit")
	}
	var afterCredential, afterHash []byte
	var afterGeneration int64
	if err := pool.QueryRow(ctx, "SELECT credential_ciphertext, subscription_hash, subscription_generation FROM devices WHERE id=$1", first.ID).
		Scan(&afterCredential, &afterHash, &afterGeneration); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(credential, afterCredential) || !bytes.Equal(hash, afterHash) || generation != afterGeneration {
		t.Fatal("rename changed credential or subscription")
	}
	statusAfter, err := store.Status(ctx)
	if err != nil || statusAfter.DesiredRevision != statusBefore.DesiredRevision || statusAfter.AppliedRevision != statusBefore.AppliedRevision {
		t.Fatal("rename changed Xray revision")
	}
	after, err := store.Export(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	oldURI, _ := url.Parse(before.URI)
	newURI, _ := url.Parse(after.URI)
	if before.SubscriptionPath != after.SubscriptionPath || oldURI.User.Username() != newURI.User.Username() || !strings.Contains(newURI.Fragment, updated.Name) {
		t.Fatal("rename changed access or lost the new profile label")
	}
	devices, err := store.Devices(ctx)
	if err != nil || len(devices) != 2 {
		t.Fatal("device list failed")
	}
	for _, device := range devices {
		if device.ID == first.ID && device.Name != updated.Name {
			t.Fatal("rename was not persisted")
		}
		if device.ID == other.ID && device.Name != other.Name {
			t.Fatal("rename affected another device")
		}
	}
	var renameCount, exitCount int
	if err := pool.QueryRow(ctx, "SELECT count(*) FILTER (WHERE action='device.rename'), count(*) FILTER (WHERE action='device.exit') FROM audit_events WHERE device_id=$1", first.ID).
		Scan(&renameCount, &exitCount); err != nil || renameCount != 1 || exitCount != 0 {
		t.Fatal("rename audit is incorrect")
	}
}

func TestDeviceMetadataRejectsInvalidOrProxyExitAtomicallyPostgreSQL(t *testing.T) {
	store, pool, node, _ := integrationStore(t)
	ctx := context.Background()
	first, err := store.Create(ctx, "First", node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, "Taken", node.ID); err != nil {
		t.Fatal(err)
	}
	handler := New(store, &testXray{}, &testBrowser{}, Options{Token: deviceMetadataToken})
	path := "/api/v1/devices/" + first.ID
	for _, body := range []string{
		`{}`, `null`, `[]`, `{"name":null}`, `{"name":false}`, `{"name":4}`,
		`{"name":""}`, `{"name":"   "}`, `{"name":"<script>"}`, `{"name":"line\nbreak"}`,
		`{"name":"` + strings.Repeat("я", 65) + `"}`, `{"preferred_exit":"other"}`,
		`{"preferred_exit":null}`, `{"preferred_exit":3}`, `{"name":null,"preferred_exit":"server"}`,
		`{"unknown":"value"}`, `{"name":"Changed","preferred_exit":null}`,
		`{"name":"Changed","preferred_exit":"proxy"}`,
	} {
		response := deviceMetadataRequest(handler, http.MethodPatch, path, body, true)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid metadata update %s returned %d", body, response.Code)
		}
	}
	if response := deviceMetadataRequest(handler, http.MethodPatch, path, `{"name":"Taken"}`, true); response.Code != http.StatusConflict {
		t.Fatal("duplicate name accepted")
	}
	var name, exit string
	if err := pool.QueryRow(ctx, "SELECT name, preferred_exit FROM devices WHERE id=$1", first.ID).Scan(&name, &exit); err != nil || name != first.Name || exit != "server" {
		t.Fatal("rejected patch partially changed metadata")
	}
	if _, err := pool.Exec(ctx, "UPDATE nodes SET config_hash='other-api-settings' WHERE id=$1", node.ID); err != nil {
		t.Fatal(err)
	}
	if response := deviceMetadataRequest(handler, http.MethodPatch, path, `{"name":"Renamed safely"}`, true); response.Code != http.StatusOK {
		t.Fatal("rename incorrectly depended on Xray config")
	}
	if _, err := store.Revoke(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if response := deviceMetadataRequest(handler, http.MethodPatch, path, `{"name":"Revoked renamed"}`, true); response.Code != http.StatusOK {
		t.Fatal("revoked device could not be renamed")
	}
	if response := deviceMetadataRequest(handler, http.MethodPatch, path, `{"name":"Must not persist","preferred_exit":"server"}`, true); response.Code != http.StatusGone {
		t.Fatal("revoked device allowed exit update")
	}
}

func TestDevicePreferredExitMigrationDisablesProxyPostgreSQL(t *testing.T) {
	store, pool, _, dsn := integrationStore(t)
	ctx := context.Background()
	device, err := store.Create(ctx, "Before migration", storage.LocalNodeID)
	if err != nil {
		t.Fatal(err)
	}
	// Возвращаем изолированную схему к версии 8 и проверяем все последующие миграции.
	if _, err := pool.Exec(ctx, `ALTER TABLE nodes DROP CONSTRAINT nodes_bridge_config_valid, DROP COLUMN bridge_config, DROP COLUMN connection_mode;
  ALTER TABLE nodes DROP CONSTRAINT nodes_h3_config_object, DROP COLUMN h3_config, DROP COLUMN requested_revision,
  DROP COLUMN agent_error, DROP COLUMN agent_last_seen_at, DROP COLUMN agent_token_hash,
  DROP COLUMN agent_join_expires_at, DROP COLUMN agent_join_hash, DROP COLUMN kind;
  UPDATE schema_migrations SET version=8`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE devices SET preferred_exit='proxy' WHERE id=$1", device.ID); err != nil {
		t.Fatal(err)
	}
	if err := storage.Migrate(dsn); err != nil {
		t.Fatal(err)
	}
	var preferred string
	if err := pool.QueryRow(ctx, "SELECT preferred_exit FROM devices WHERE id=$1", device.ID).Scan(&preferred); err != nil || preferred != "server" {
		t.Fatal("migration did not disable old proxy exit")
	}
}
