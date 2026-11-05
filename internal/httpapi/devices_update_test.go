package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/rvburov/private-gateway/internal/model"
	"github.com/rvburov/private-gateway/internal/secure"
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

func TestDeviceMetadataPersistsWithoutChangingAccessPostgreSQL(t *testing.T) {
	store, pool, node, dsn := integrationStore(t)
	ctx := context.Background()
	node.ProxyAvailable = true
	node.ExpectedServerIP, node.ExpectedProxyIP = "8.8.8.8", "1.1.1.1"
	if err := store.EnsureNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	first, err := store.Create(ctx, "Original laptop", node.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Create(ctx, "Other laptop", node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.PreferredExit != "server" || second.PreferredExit != "server" {
		t.Fatal("new devices did not default to the original server exit")
	}
	if err = store.Configure(ctx, true, func(context.Context, []xray.Client) error { return nil }); err != nil {
		t.Fatal(err)
	}
	directBefore, err := store.ExportForExit(ctx, first.ID, "server")
	if err != nil {
		t.Fatal(err)
	}
	proxyBefore, err := store.ExportForExit(ctx, first.ID, "proxy")
	if err != nil {
		t.Fatal(err)
	}
	var beforeCredential, beforeHash []byte
	var beforeGeneration int64
	if err = pool.QueryRow(ctx, "SELECT credential_ciphertext, subscription_hash, subscription_generation FROM devices WHERE id=$1", first.ID).
		Scan(&beforeCredential, &beforeHash, &beforeGeneration); err != nil {
		t.Fatal(err)
	}
	statusBefore, err := store.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	handler := New(store, &testXray{}, &testBrowser{}, Options{Token: deviceMetadataToken})
	path := "/api/v1/devices/" + first.ID
	response := deviceMetadataRequest(handler, http.MethodPatch, path, `{"name":"  Рабочий ноутбук  ","preferred_exit":"proxy"}`, true)
	var updated model.Device
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &updated) != nil {
		t.Fatalf("valid metadata update failed: status=%d", response.Code)
	}
	if updated.ID != first.ID || updated.NodeID != first.NodeID || !updated.CreatedAt.Equal(first.CreatedAt) ||
		updated.RevokedAt != nil || updated.Name != "Рабочий ноутбук" || updated.PreferredExit != "proxy" {
		t.Fatal("metadata update changed device identity or lost the selection")
	}
	var afterCredential, afterHash []byte
	var afterGeneration int64
	if err = pool.QueryRow(ctx, "SELECT credential_ciphertext, subscription_hash, subscription_generation FROM devices WHERE id=$1", first.ID).
		Scan(&afterCredential, &afterHash, &afterGeneration); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeCredential, afterCredential) || !bytes.Equal(beforeHash, afterHash) || beforeGeneration != afterGeneration {
		t.Fatal("metadata update changed credentials or subscription generation/hash")
	}
	statusAfter, err := store.Status(ctx)
	if err != nil || statusAfter.DesiredRevision != statusBefore.DesiredRevision || statusAfter.AppliedRevision != statusBefore.AppliedRevision {
		t.Fatal("metadata update required a new Xray configuration")
	}
	for _, exit := range []string{"server", "proxy"} {
		profile, err := store.ExportForExit(ctx, first.ID, exit)
		if err != nil {
			t.Fatal(err)
		}
		before := directBefore
		expectedIP := node.ExpectedServerIP
		if exit == "proxy" {
			before, expectedIP = proxyBefore, node.ExpectedProxyIP
		}
		uri, _ := url.Parse(profile.URI)
		oldURI, _ := url.Parse(before.URI)
		if profile.SubscriptionPath != before.SubscriptionPath || uri.User.Username() != oldURI.User.Username() ||
			profile.ExpectedExitIP != expectedIP || !strings.Contains(uri.Fragment, updated.Name) {
			t.Fatal("rename/selection changed access or lost the expected IP/new profile name")
		}
		subscription, _, err := store.SubscriptionForExit(ctx, strings.TrimPrefix(directBefore.SubscriptionPath, storage.SubscriptionPathPrefix), exit)
		if err != nil || subscription.ExpectedExitIP != expectedIP {
			t.Fatal("subscription profile lost its configured expected exit IP")
		}
	}
	// Отдельный процесс читает выбранный выход из PostgreSQL, а не из памяти старого API.
	cipher, err := secure.New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := storage.Open(ctx, dsn, cipher)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err = reopened.EnsureNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	devices, err := reopened.Devices(ctx)
	if err != nil || len(devices) != 2 {
		t.Fatal("persisted devices are unavailable after reopening storage")
	}
	for _, device := range devices {
		if device.ID == first.ID && (device.Name != updated.Name || device.PreferredExit != "proxy") {
			t.Fatal("name or selected exit was not persisted")
		}
		if device.ID == second.ID && (device.Name != second.Name || device.PreferredExit != "server") {
			t.Fatal("one device selection changed another device")
		}
	}
	nodes, err := reopened.Nodes(ctx)
	if err != nil || len(nodes) != 1 || !nodes[0].ProxyAvailable ||
		nodes[0].ExpectedServerIP != node.ExpectedServerIP || nodes[0].ExpectedProxyIP != node.ExpectedProxyIP {
		t.Fatal("configured expected IPs were not included in node metadata")
	}
	response = deviceMetadataRequest(New(reopened, &testXray{}, &testBrowser{}, Options{Token: deviceMetadataToken}), http.MethodGet, "/api/v1/devices", "", true)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"preferred_exit":"proxy"`) {
		t.Fatal("device list did not expose persisted exit preference")
	}
	response = deviceMetadataRequest(handler, http.MethodGet, path+"/profile", "", true)
	var legacyDefault model.Profile
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &legacyDefault) != nil ||
		legacyDefault.SubscriptionPath != directBefore.SubscriptionPath || legacyDefault.ExpectedExitIP != node.ExpectedServerIP {
		t.Fatal("remembered preference silently changed the legacy default export")
	}
	var renameCount, exitCount int
	if err = pool.QueryRow(ctx, "SELECT count(*) FILTER (WHERE action='device.rename'), count(*) FILTER (WHERE action='device.exit') FROM audit_events WHERE device_id=$1", first.ID).
		Scan(&renameCount, &exitCount); err != nil || renameCount != 1 || exitCount != 1 {
		t.Fatal("metadata changes did not create distinct immutable audit events")
	}
	response = deviceMetadataRequest(handler, http.MethodGet, "/api/v1/audit", "", true)
	if strings.Contains(response.Body.String(), updated.Name) || strings.Contains(response.Body.String(), "Original laptop") {
		t.Fatal("audit exposed a device name")
	}
}

func TestDeviceMetadataRejectsInvalidOrUnavailableChangesAtomicallyPostgreSQL(t *testing.T) {
	store, pool, node, _ := integrationStore(t)
	ctx := context.Background()
	first, err := store.Create(ctx, "First", node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Create(ctx, "Taken", node.ID); err != nil {
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
	} {
		t.Run(body, func(t *testing.T) {
			response := deviceMetadataRequest(handler, http.MethodPatch, path, body, true)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("invalid metadata update returned %d", response.Code)
			}
		})
	}
	response := deviceMetadataRequest(handler, http.MethodPatch, path, `{"name":"Taken"}`, true)
	if response.Code != http.StatusConflict {
		t.Fatalf("duplicate active name returned %d", response.Code)
	}
	for _, id := range []string{"invalid-uuid", "00000000-0000-4000-8000-000000000099"} {
		if response = deviceMetadataRequest(handler, http.MethodPatch, "/api/v1/devices/"+id, `{"name":"Changed"}`, true); response.Code != http.StatusNotFound {
			t.Fatal("unknown device metadata update was not a 404")
		}
	}
	checkUnchanged := func() {
		t.Helper()
		var name, exit string
		if err := pool.QueryRow(ctx, "SELECT name, preferred_exit FROM devices WHERE id=$1", first.ID).Scan(&name, &exit); err != nil || name != first.Name || exit != "server" {
			t.Fatal("rejected metadata patch partially changed name or preferred exit")
		}
	}
	response = deviceMetadataRequest(handler, http.MethodPatch, path, `{"name":"Changed","preferred_exit":"proxy"}`, true)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatal("disabled proxy preference was accepted")
	}
	checkUnchanged()
	node.ProxyAvailable = true
	node.ConfigHash = "enabled-proxy-settings"
	if err = store.EnsureNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	response = deviceMetadataRequest(handler, http.MethodPatch, path, `{"name":"Changed","preferred_exit":"proxy"}`, true)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatal("pending proxy configuration was accepted")
	}
	checkUnchanged()
	if err = store.Configure(ctx, true, func(context.Context, []xray.Client) error { return nil }); err != nil {
		t.Fatal(err)
	}
	response = deviceMetadataRequest(handler, http.MethodPatch, path, `{"name":"Taken","preferred_exit":"proxy"}`, true)
	if response.Code != http.StatusConflict {
		t.Fatal("duplicate name and valid exit did not fail as one transaction")
	}
	checkUnchanged()
	if _, err = pool.Exec(ctx, "UPDATE nodes SET config_hash='other-api-settings' WHERE id=$1", node.ID); err != nil {
		t.Fatal(err)
	}
	response = deviceMetadataRequest(handler, http.MethodPatch, path, `{"name":"Changed","preferred_exit":"proxy"}`, true)
	if response.Code != http.StatusConflict {
		t.Fatal("stale process configuration did not reject proxy preference")
	}
	checkUnchanged()
	// Переименование не управляет маршрутом и остаётся доступно при устаревшей конфигурации.
	response = deviceMetadataRequest(handler, http.MethodPatch, path, `{"name":"Renamed safely"}`, true)
	if response.Code != http.StatusOK {
		t.Fatal("rename-only incorrectly required a ready proxy or matching Xray configuration")
	}
	if _, err = store.Revoke(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	response = deviceMetadataRequest(handler, http.MethodPatch, path, `{"name":"Revoked renamed"}`, true)
	if response.Code != http.StatusOK {
		t.Fatal("revoked device could not be renamed")
	}
	response = deviceMetadataRequest(handler, http.MethodPatch, path, `{"name":"Must not persist","preferred_exit":"server"}`, true)
	if response.Code != http.StatusGone {
		t.Fatal("revoked device allowed an exit change")
	}
	var finalName string
	if err = pool.QueryRow(ctx, "SELECT name FROM devices WHERE id=$1", first.ID).Scan(&finalName); err != nil || finalName != "Revoked renamed" {
		t.Fatal("rejected revoked-device exit patch partially renamed the device")
	}
}

func TestDevicePreferredExitMigrationPreservesExistingDevicesPostgreSQL(t *testing.T) {
	store, pool, _, dsn := integrationStore(t)
	ctx := context.Background()
	device, err := store.Create(ctx, "Before migration", storage.LocalNodeID)
	if err != nil {
		t.Fatal(err)
	}
	// В отдельной тестовой схеме воспроизводим базу версии7 с уже существующим устройством.
	if _, err = pool.Exec(ctx, "ALTER TABLE devices DROP COLUMN preferred_exit; UPDATE schema_migrations SET version=7"); err != nil {
		t.Fatal(err)
	}
	if err = storage.Migrate(dsn); err != nil {
		t.Fatal(err)
	}
	var preferred string
	if err = pool.QueryRow(ctx, "SELECT preferred_exit FROM devices WHERE id=$1", device.ID).Scan(&preferred); err != nil || preferred != "server" {
		t.Fatal("migration did not preserve the server exit for an existing device")
	}
	if _, err = pool.Exec(ctx, "UPDATE devices SET preferred_exit='invalid' WHERE id=$1", device.ID); err == nil {
		t.Fatal("database allowed an unsupported exit")
	}
}

func TestDeviceExpectedExitIPsRequireMatchingRuntimeNodePostgreSQL(t *testing.T) {
	store, pool, node, _ := integrationStore(t)
	ctx := context.Background()
	node.ProxyAvailable = true
	node.ExpectedServerIP, node.ExpectedProxyIP = "8.8.8.8", "1.1.1.1"
	if err := store.EnsureNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	device, err := store.Create(ctx, "Expected exit", node.ID)
	if err != nil {
		t.Fatal(err)
	}
	handler := New(store, &testXray{}, &testBrowser{}, Options{Token: deviceMetadataToken})
	response := deviceMetadataRequest(handler, http.MethodGet, "/api/v1/nodes", "", true)
	var result struct {
		Nodes []model.Node `json:"nodes"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &result) != nil || len(result.Nodes) != 1 ||
		result.Nodes[0].ExpectedServerIP != node.ExpectedServerIP || result.Nodes[0].ExpectedProxyIP != node.ExpectedProxyIP || result.Nodes[0].ProxyAvailable {
		t.Fatal("pending node lost expected addresses or was advertised as proxy-ready")
	}
	if err = store.Configure(ctx, true, func(context.Context, []xray.Client) error { return nil }); err != nil {
		t.Fatal(err)
	}
	response = deviceMetadataRequest(handler, http.MethodGet, "/api/v1/devices/"+device.ID+"/profile?exit=proxy", "", true)
	var profile model.Profile
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &profile) != nil || profile.ExpectedExitIP != node.ExpectedProxyIP {
		t.Fatal("proxy export did not expose its configured expected IP")
	}
	if _, err = pool.Exec(ctx, "UPDATE nodes SET config_hash='other-process' WHERE id=$1", node.ID); err != nil {
		t.Fatal(err)
	}
	nodes, err := store.Nodes(ctx)
	if err != nil || len(nodes) != 1 || nodes[0].ExpectedServerIP != "" || nodes[0].ExpectedProxyIP != "" || nodes[0].ProxyAvailable {
		t.Fatal("stale process attached expected IPs to another configuration")
	}
}
