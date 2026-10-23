package httpapi

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rvburov/private-gateway/internal/secure"
	"github.com/rvburov/private-gateway/internal/storage"
	"github.com/rvburov/private-gateway/internal/xray"
)

func notificationTestKeys(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 16))
}

func TestPushEndpointAndKeyValidation(t *testing.T) {
	valid := []string{"https://fcm.googleapis.com/fcm/send/test-token", "https://updates.push.services.mozilla.com/wpush/v2/test-token", "https://web.push.apple.com/test-token", "https://s.notify.windows.com/w/?token=test"}
	for _, endpoint := range valid {
		if !validPushEndpoint(endpoint) {
			t.Errorf("rejected supported endpoint %q", endpoint)
		}
	}
	invalid := []string{"http://fcm.googleapis.com/fcm/send/test", "https://127.0.0.1/token", "https://[::1]/token", "https://localhost/token", "https://fcm.googleapis.com.evil.example/token", "https://evilnotify.windows.com/token", "https://example.com/token", "https://user:pass@fcm.googleapis.com/token", "https://fcm.googleapis.com:8443/token", "https://fcm.googleapis.com:443/token", "https://fcm.googleapis.com/token#fragment", "https://fcm.googleapis.com./token", "https://fcm.googleapis.com/", "https://fcm.googleapis.com/\\test"}
	for _, endpoint := range invalid {
		if validPushEndpoint(endpoint) {
			t.Errorf("accepted unsafe endpoint %q", endpoint)
		}
	}
	point, auth := notificationTestKeys(t)
	if !validPushKeys(point, auth) {
		t.Fatal("valid subscription keys rejected")
	}
	if validPushKeys(base64.RawURLEncoding.EncodeToString(make([]byte, 65)), auth) || validPushKeys(point, "short") {
		t.Fatal("invalid subscription keys accepted")
	}
	if !validPushDevice("iPhone — Safari") || validPushDevice(strings.Repeat("я", 81)) || validPushDevice("Windows\nChrome") || validPushDevice("   ") {
		t.Fatal("device label bounds invalid")
	}
}

func TestNotificationPushPayloadIsSpecificAndDoesNotExposePrivateData(t *testing.T) {
	private := "vless://device-secret@192.0.2.4:8443/#Личный телефон"
	seen := map[string]bool{}
	for _, message := range notificationMessages {
		payload := notificationPayload(storage.Notification{ID: private, Category: message.category, Title: message.title, Body: private}, false)
		if payload.Event == "notification" || seen[payload.Event] || payload.Title != message.title || payload.Body == "" || payload.Test {
			t.Fatalf("event did not retain its concrete safe content: %+v", payload)
		}
		seen[payload.Event] = true
		data, err := json.Marshal(payload)
		if err != nil || bytes.Contains(data, []byte(private)) || payload.URL != "/?page=notifications" || payload.Tag != "private-gateway-"+payload.Event {
			t.Fatalf("unsafe push metadata: %s; %v", data, err)
		}
	}
	for _, event := range []string{"device-added", "device-revoked", "device-deleted", "subscription-renewed", "configuration-apply-failed", "configuration-applied", "database-unavailable", "database-recovered", "xray-check-failed", "xray-configuration-pending", "xray-recovered", "configuration-check-failed", "configuration-pending", "configuration-recovered"} {
		if !seen[event] {
			t.Errorf("event %q is missing", event)
		}
	}
	// Неизвестный текст или заголовок из другой категории не становятся содержимым push.
	for _, item := range []storage.Notification{
		{Category: "devices", Title: private, Body: private},
		{Category: "devices", Title: "База данных не отвечает", Body: private},
		{Category: private, Title: private, Body: private},
	} {
		payload := notificationPayload(item, false)
		data, _ := json.Marshal(payload)
		if bytes.Contains(data, []byte(private)) || payload.Event != "notification" || payload.Title != "Новое уведомление" {
			t.Fatalf("unrecognized content escaped the closed catalog: %s", data)
		}
	}
	// Тестовое сообщение явно отличается от серверной ошибки, в том числе в JSON.
	payload := notificationPayload(storage.Notification{Category: "server", Title: "База данных не отвечает", Body: private}, true)
	data, _ := json.Marshal(payload)
	if payload.Title != "Проверка уведомлений" || payload.Event != "notification-test" || !payload.Test || !strings.Contains(payload.Body, "тестовое") || bytes.Contains(data, []byte(private)) || !bytes.Contains(data, []byte(`"test":true`)) {
		t.Fatalf("test push is ambiguous or exposes event data: %s", data)
	}
}

func TestNotificationHealthTransitionsAreSpecificAndDeduplicated(t *testing.T) {
	good := notificationHealth{database: "ok", xray: "ok", configuration: "ok"}
	state, events := notificationHealthChanges(notificationHealth{}, good)
	if state != good || len(events) != 0 {
		t.Fatal("first successful observation was presented as recovery", events)
	}
	bad := notificationHealth{database: "fail", xray: "warn", configuration: "warn"}
	state, events = notificationHealthChanges(state, bad)
	expectEvents := func(want ...string) {
		t.Helper()
		if len(events) != len(want) {
			t.Fatalf("got %d transition events, want %v: %+v", len(events), want, events)
		}
		for index, event := range events {
			payload := notificationPayload(event, false)
			if payload.Event != want[index] || event.Category != "server" || event.Body == "" {
				t.Fatalf("transition %d = %+v, want %q", index, event, want[index])
			}
		}
	}
	expectEvents("database-unavailable", "xray-configuration-pending", "configuration-pending")
	state, events = notificationHealthChanges(state, bad)
	expectEvents()
	// Новый вид проблемы процесса виден, даже если остальные службы всё ещё неисправны.
	bad.xray = "fail"
	state, events = notificationHealthChanges(state, bad)
	expectEvents("xray-check-failed")
	state, events = notificationHealthChanges(state, notificationHealth{database: "unknown", xray: "skip", configuration: "unknown"})
	expectEvents()
	if state != bad {
		t.Fatal("unknown observations erased known failures", state)
	}
	// Восстановление БД не превращается в сообщение о восстановлении всего VPN.
	bad.database = "ok"
	state, events = notificationHealthChanges(state, bad)
	expectEvents("database-recovered")
	state, events = notificationHealthChanges(state, good)
	expectEvents("xray-recovered", "configuration-recovered")
	_, events = notificationHealthChanges(state, good)
	expectEvents()
	// Неудача чтения версий отличается от наличия неприменённых изменений.
	_, events = notificationHealthChanges(good, notificationHealth{database: "ok", xray: "ok", configuration: "fail"})
	expectEvents("configuration-check-failed")
}

func TestNotificationHealthReadsIndependentLocalServicesPostgreSQL(t *testing.T) {
	store, _, node, _ := integrationStore(t)
	ctx := context.Background()
	if err := store.Configure(ctx, true, func(context.Context, []xray.Client) error { return nil }); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	path := filepath.Join(directory, "xray.json")
	config := []byte(`{"inbounds":[]}`)
	if err := os.WriteFile(path, config, 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	status, _ := json.Marshal(map[string]any{"running": true, "mtime_ns": info.ModTime().UnixNano(), "sha256": fmt.Sprintf("%x", sha256.Sum256(config))})
	statusPath := filepath.Join(directory, "xray.status.json")
	if err = os.WriteFile(statusPath, status, 0600); err != nil {
		t.Fatal(err)
	}
	api := &Server{store: store, options: Options{XrayConfigured: true, Checks: ServerChecks{XrayConfigPath: path}}}
	api.startNotifications(nil)
	if state := api.notifications.health(ctx); state != (notificationHealth{database: "ok", xray: "ok", configuration: "ok"}) {
		t.Fatal("healthy local services were not recognized", state)
	}
	if _, err = store.Create(ctx, "test", node.ID); err != nil {
		t.Fatal(err)
	}
	if state := api.notifications.health(ctx); state != (notificationHealth{database: "ok", xray: "ok", configuration: "warn"}) {
		t.Fatal("pending configuration was mistaken for a process failure", state)
	}
	if err = os.WriteFile(statusPath, []byte(`{"running":false,"error":"private error details"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if state := api.notifications.health(ctx); state != (notificationHealth{database: "ok", xray: "fail", configuration: "warn"}) {
		t.Fatal("process failure masked pending configuration", state)
	}
	// Xray может продолжать работать при отказе БД; локальную проверку не пропускаем.
	if err = os.WriteFile(statusPath, status, 0600); err != nil {
		t.Fatal(err)
	}
	store.Close()
	if state := api.notifications.health(ctx); state != (notificationHealth{database: "fail", xray: "ok", configuration: "unknown"}) {
		t.Fatal("database outage was presented as a VPN outage or configuration failure", state)
	}
}

func TestNotificationInboxUsesOnlyControlledTextsPostgreSQL(t *testing.T) {
	store, _, _, _ := integrationStore(t)
	api := &Server{store: store}
	api.startNotifications(nil)
	private := "vless://secret@192.0.2.4:8443/#Личный телефон"
	api.emitNotification("devices", "Добавлено устройство", private)
	api.emitNotification("server", private, private)
	items, unread, err := store.Notifications(context.Background())
	if err != nil || len(items) != 2 || unread != 2 {
		t.Fatal("notification center did not preserve both safe events", items, unread, err)
	}
	data, _ := json.Marshal(items)
	if bytes.Contains(data, []byte(private)) || !bytes.Contains(data, []byte("Добавлено устройство")) || !bytes.Contains(data, []byte("Новое уведомление")) {
		t.Fatalf("notification center content was not normalized: %s", data)
	}
}

func TestNotificationRoutesRequireAuthentication(t *testing.T) {
	handler := New(nil, nil, nil, Options{Token: "test-secret"})
	for _, route := range []struct{ method, path string }{
		{"GET", "/api/v1/notifications"}, {"POST", "/api/v1/notifications/read"},
		{"GET", "/api/v1/notifications/preferences"}, {"PUT", "/api/v1/notifications/preferences"},
		{"GET", "/api/v1/notifications/push-key"}, {"GET", "/api/v1/notifications/push-subscriptions"},
		{"POST", "/api/v1/notifications/push-subscriptions"}, {"DELETE", "/api/v1/notifications/push-subscriptions/" + uuid.NewString()},
		{"POST", "/api/v1/notifications/test"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(route.method, route.path, strings.NewReader(`{}`)))
		if response.Code != 401 {
			t.Errorf("%s %s status=%d", route.method, route.path, response.Code)
		}
	}
}

func TestNotificationPersistencePreferencesAndEncryptedPushPostgreSQL(t *testing.T) {
	store, pool, _, dsn := integrationStore(t)
	ctx := context.Background()
	token := "notification-test-api-token-long-enough"
	handler := New(store, &testXray{}, &testBrowser{}, Options{Token: token})
	ask := func(method, path, body string) (int, []byte) {
		t.Helper()
		request := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		request.RemoteAddr = "127.0.0.1:1234"
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Code, response.Body.Bytes()
	}
	if code, body := ask("GET", "/notifications/preferences", ""); code != 200 || string(body) != "{\"server\":true,\"devices\":true,\"configuration\":true}\n" {
		t.Fatalf("default prefs: %d %s", code, body)
	}
	code, first := ask("GET", "/notifications/push-key", "")
	if code != 200 {
		t.Fatalf("push key: %d %s", code, first)
	}
	_, again := ask("GET", "/notifications/push-key", "")
	if !bytes.Equal(first, again) {
		t.Fatal("VAPID public key changed on repeated request")
	}
	cipher, _ := secure.New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)))
	reopened, err := storage.Open(ctx, dsn, cipher)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	keys, err := reopened.NotificationKeys(ctx, func() (storage.NotificationKeys, error) {
		t.Fatal("key regenerated after reopen")
		return storage.NotificationKeys{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(first, []byte(keys.Public)) {
		t.Fatal("persisted VAPID key differs")
	}
	var keyCipher []byte
	if err = pool.QueryRow(ctx, `SELECT ciphertext FROM notification_keys`).Scan(&keyCipher); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(keyCipher, []byte(keys.Private)) {
		t.Fatal("private key stored without encryption")
	}

	point, auth := notificationTestKeys(t)
	endpoint := "https://fcm.googleapis.com/fcm/send/private-endpoint-secret"
	subscription, _ := json.Marshal(map[string]any{"endpoint": endpoint, "keys": map[string]string{"p256dh": point, "auth": auth}, "device": "Windows Chrome"})
	code, body := ask("POST", "/notifications/push-subscriptions", string(subscription))
	if code != 200 {
		t.Fatalf("subscribe: %d %s", code, body)
	}
	var device storage.PushDevice
	if err = json.Unmarshal(body, &device); err != nil {
		t.Fatal(err)
	}
	if len(device.EndpointHash) != 16 || device.ID == "" {
		t.Fatalf("invalid subscription metadata: %s", body)
	}
	if bytes.Contains(body, []byte(endpoint)) || bytes.Contains(body, []byte(point)) || bytes.Contains(body, []byte(auth)) {
		t.Fatal("subscription response exposes keys or endpoint")
	}
	_, duplicate := ask("POST", "/notifications/push-subscriptions", string(subscription))
	var same storage.PushDevice
	_ = json.Unmarshal(duplicate, &same)
	if same.ID != device.ID {
		t.Fatal("resubscribe created duplicate device")
	}
	var encrypted []byte
	var identity []byte
	var expires time.Time
	if err = pool.QueryRow(ctx, `SELECT ciphertext,identity_hash,expires_at FROM notification_push_subscriptions WHERE id=$1`, device.ID).Scan(&encrypted, &identity, &expires); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, []byte(endpoint)) || bytes.Contains(encrypted, []byte(auth)) || bytes.Contains(identity, []byte(token)) {
		t.Fatal("push credential stored as plaintext")
	}
	if time.Until(expires) > 8*time.Hour || time.Until(expires) < 7*time.Hour {
		t.Fatal("session grant expiry is not bounded")
	}
	if code, list := ask("GET", "/notifications/push-subscriptions", ""); code != 200 || !bytes.Contains(list, []byte(device.ID)) || bytes.Contains(list, []byte(endpoint)) {
		t.Fatalf("list leaked or lost subscription: %d %s", code, list)
	}
	if code, _ = ask("POST", "/notifications/push-subscriptions", `{"endpoint":"https://127.0.0.1/secret","keys":{"p256dh":"x","auth":"x"},"device":"test"}`); code != 400 {
		t.Fatal("unsafe push endpoint accepted")
	}

	if code, _ = ask("PUT", "/notifications/preferences", `{"server":false,"devices":true,"configuration":true}`); code != 200 {
		t.Fatal("save preferences failed")
	}
	if _, created, err := store.AddNotification(ctx, "server", "Сервер", "Нужно проверить сервер."); err != nil || created {
		t.Fatal("disabled category added an item", err)
	}
	item, created, err := store.AddNotification(ctx, "configuration", "Конфигурация применена", "Изменения сохранены.")
	if err != nil || !created {
		t.Fatal("enabled category not added", err)
	}
	if code, list := ask("GET", "/notifications", ""); code != 200 || !bytes.Contains(list, []byte(item.ID)) || !bytes.Contains(list, []byte(`"unread":1`)) {
		t.Fatalf("inbox: %d %s", code, list)
	}
	if code, _ = ask("POST", "/notifications/read", `{}`); code != 200 {
		t.Fatal("read all with omitted IDs failed")
	}
	if _, unread, err := store.Notifications(ctx); err != nil || unread != 0 {
		t.Fatal("read all not persisted", unread, err)
	}
	// Максимальный документ прочтения содержит больше 4 КБ, но остаётся ограниченным 200 UUID.
	ids := make([]string, 200)
	for index := range ids {
		ids[index] = uuid.NewString()
	}
	readBody, _ := json.Marshal(map[string]any{"ids": ids})
	if code, _ = ask("POST", "/notifications/read", string(readBody)); code != 200 {
		t.Fatal("200 IDs rejected")
	}
	if code, _ = ask("DELETE", "/notifications/push-subscriptions/"+device.ID, ""); code != 200 {
		t.Fatal("unsubscribe failed")
	}
	if targets, err := store.PushSubscriptions(ctx); err != nil || len(targets) != 0 {
		t.Fatal("subscription retained after revoke", err)
	}
}

type notificationHTTPClient func(*http.Request) (*http.Response, error)

func (f notificationHTTPClient) Do(r *http.Request) (*http.Response, error) { return f(r) }

// Расшифровываем захваченный запрос ключом тестового получателя, не обращаясь к push-сервису.
func notificationReceivedPayload(t *testing.T, request *http.Request, recipient *ecdh.PrivateKey, auth []byte) notificationPushPayload {
	t.Helper()
	data, err := io.ReadAll(request.Body)
	if err != nil || request.Header.Get("Content-Encoding") != "aes128gcm" || len(data) < 102 || data[20] != 65 {
		t.Fatal("invalid encrypted Web Push record", err)
	}
	server, err := ecdh.P256().NewPublicKey(data[21:86])
	if err != nil {
		t.Fatal(err)
	}
	shared, err := recipient.ECDH(server)
	if err != nil {
		t.Fatal(err)
	}
	info := append([]byte("WebPush: info\x00"), recipient.PublicKey().Bytes()...)
	info = append(info, server.Bytes()...)
	ikm, err := hkdf.Key(sha256.New, shared, auth, string(info), 32)
	if err != nil {
		t.Fatal(err)
	}
	key, err := hkdf.Key(sha256.New, ikm, data[:16], "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := hkdf.Key(sha256.New, ikm, data[:16], "Content-Encoding: nonce\x00", 12)
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := gcm.Open(nil, nonce, data[86:], nil)
	if err != nil {
		t.Fatal("recipient cannot decrypt push payload", err)
	}
	plain = bytes.TrimRight(plain, "\x00")
	if len(plain) == 0 || plain[len(plain)-1] != 2 {
		t.Fatal("push final-record delimiter missing")
	}
	var payload notificationPushPayload
	if err = json.Unmarshal(plain[:len(plain)-1], &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestNotificationTestRouteAndEventDeliveryHaveDistinctEncryptedPayloadsPostgreSQL(t *testing.T) {
	store, _, _, _ := integrationStore(t)
	ctx := context.Background()
	api := &Server{store: store}
	api.startNotifications(nil)
	recipient, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := bytes.Repeat([]byte{7}, 16)
	secret := storage.PushSecret{Endpoint: "https://fcm.googleapis.com/fcm/send/test-selected-device", P256dh: base64.RawURLEncoding.EncodeToString(recipient.PublicKey().Bytes()), Auth: base64.RawURLEncoding.EncodeToString(auth)}
	device, err := store.SavePushSubscription(ctx, secret, "Phone", sha256.Sum256([]byte("test-session")), 0, time.Now().Add(time.Hour), false)
	if err != nil {
		t.Fatal(err)
	}
	var received []notificationPushPayload
	api.notifications.client = notificationHTTPClient(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != secret.Endpoint {
			t.Fatal("test delivered to another device")
		}
		received = append(received, notificationReceivedPayload(t, request, recipient, auth))
		return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	probe := func(id string) int {
		t.Helper()
		request := httptest.NewRequest("POST", "/api/v1/notifications/test", strings.NewReader(`{"subscription_id":"`+id+`"}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		api.notificationTest(response, request)
		return response.Code
	}
	if code := probe(uuid.NewString()); code != 404 || len(received) != 0 {
		t.Fatal("unknown test destination sent a push", code)
	}
	if code := probe(device.ID); code != 200 || len(received) != 1 {
		t.Fatal("selected test destination not delivered", code)
	}
	if received[0].Title != "Проверка уведомлений" || received[0].Event != "notification-test" || !received[0].Test {
		t.Fatal("decrypted test was not explicitly marked", received[0])
	}
	if code := probe(device.ID); code != 429 || len(received) != 1 {
		t.Fatal("test rate limit did not prevent duplicate delivery", code)
	}
	event := storage.Notification{Category: "devices", Title: "Доступ устройства отозван", Body: "vless://private-profile-secret"}
	if err = api.notifications.send(ctx, storage.PushDelivery{PushDevice: device, Secret: secret}, false, event); err != nil || len(received) != 2 {
		t.Fatal("regular event delivery failed", err)
	}
	if received[1].Title != event.Title || received[1].Event != "device-revoked" || received[1].Test || strings.Contains(received[1].Body, event.Body) || !strings.Contains(received[1].Body, "Примените конфигурацию") {
		t.Fatal("concrete encrypted event lost its meaning or exposed a profile", received[1])
	}
	if items, _, err := store.Notifications(ctx); err != nil || len(items) != 0 {
		t.Fatal("test delivery created a misleading notification-center event", items, err)
	}
}

func TestNotificationDeliveryRechecksConsentAndRevocationPostgreSQL(t *testing.T) {
	store, pool, _, _ := integrationStore(t)
	ctx := context.Background()
	api := &Server{store: store, options: Options{Checks: ServerChecks{PanelDomain: "panel.example"}}}
	api.startNotifications(nil)
	calls := 0
	status := 201
	api.notifications.client = notificationHTTPClient(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "fcm.googleapis.com" || r.Header.Get("Authorization") == "" {
			t.Fatal("invalid encrypted push request")
		}
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte("Private Gateway")) {
			t.Fatal("push payload was not encrypted")
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	point, auth := notificationTestKeys(t)
	identity := sha256.Sum256([]byte("session-token"))
	device, err := store.SavePushSubscription(ctx, storage.PushSecret{Endpoint: "https://fcm.googleapis.com/fcm/send/token", P256dh: point, Auth: auth}, "Chrome", identity, 0, time.Now().Add(time.Hour), false)
	if err != nil {
		t.Fatal(err)
	}
	items, err := store.PushSubscriptions(ctx)
	if err != nil || len(items) != 1 {
		t.Fatal("target missing", err)
	}
	stale := items[0]
	if err = store.SaveNotificationPreferences(ctx, storage.NotificationPreferences{Server: false, Devices: true, Configuration: true}); err != nil {
		t.Fatal(err)
	}
	if err = api.notifications.send(ctx, stale, false, storage.Notification{Category: "server", Title: "База данных не отвечает"}); err != nil || calls != 0 {
		t.Fatal("queued category bypassed disabled preferences", err)
	}
	if err = api.notifications.send(ctx, stale, false, storage.Notification{Category: "devices", Title: "Добавлено устройство"}); err != nil || calls != 1 {
		t.Fatal("allowed category not sent", err)
	}
	if err = store.DeletePushSubscription(ctx, device.ID); err != nil {
		t.Fatal(err)
	}
	if err = api.notifications.send(ctx, stale, true, storage.Notification{}); err == nil || calls != 1 {
		t.Fatal("stale target sent after logout/revoke")
	}
	device, err = store.SavePushSubscription(ctx, stale.Secret, "Chrome", identity, 0, time.Now().Add(time.Hour), false)
	if err != nil {
		t.Fatal(err)
	}
	stale.ID = device.ID
	status = 410
	if err = api.notifications.send(ctx, stale, true, storage.Notification{}); err == nil {
		t.Fatal("expired push endpoint accepted")
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM notification_push_subscriptions`).Scan(&count); err != nil || count != 0 {
		t.Fatal("410 endpoint not removed", err)
	}
}

func TestRememberedPushGrantExpiryAndRevocationPostgreSQL(t *testing.T) {
	store, pool, _, _ := integrationStore(t)
	ctx := context.Background()
	passwordHash := "pbkdf2_sha256:600000:" + strings.Repeat("01", 16) + ":" + strings.Repeat("02", 32)
	_, err := pool.Exec(ctx, `INSERT INTO administrator_credentials(id,username,password_hash,generation) VALUES (1,'admin',$1,0)`, passwordHash)
	if err != nil {
		t.Fatal(err)
	}
	identity := sha256.Sum256([]byte("remembered-token"))
	expires := time.Now().Add(2 * time.Hour)
	_, err = pool.Exec(ctx, `INSERT INTO administrator_remembered_logins(token_hash,administrator_id,generation,expires_at) VALUES ($1,1,0,$2)`, identity[:], expires)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := store.RememberedPushExpiry(ctx, identity, 0)
	if err != nil || actual.Sub(expires) > time.Second {
		t.Fatal("remembered grant expiry differs", err)
	}
	api := &Server{store: store, sessions: map[string]adminSession{"new-session": {generation: 0, expires: time.Now().Add(8 * time.Hour), rememberedKey: identity}}}
	request := httptest.NewRequest("POST", "/", nil)
	request.Header.Set("Authorization", "Bearer new-session")
	grant, err := api.pushIdentity(request)
	if err != nil || !grant.remembered || grant.hash != identity || grant.expires.Sub(expires) > time.Second {
		t.Fatal("remembered push grant extended", err)
	}
	point, auth := notificationTestKeys(t)
	device, err := store.SavePushSubscription(ctx, storage.PushSecret{Endpoint: "https://fcm.googleapis.com/fcm/send/remembered", P256dh: point, Auth: auth}, "Phone", identity, 0, actual, true)
	if err != nil {
		t.Fatal(err)
	}
	items, err := store.PushSubscriptions(ctx)
	if err != nil || len(items) != 1 {
		t.Fatal("active remembered grant missing", err)
	}
	_, err = pool.Exec(ctx, `DELETE FROM administrator_remembered_logins`)
	if err != nil {
		t.Fatal(err)
	}
	items, err = store.PushSubscriptions(ctx)
	if err != nil || len(items) != 0 {
		t.Fatal("push survived remembered login revocation", err)
	}
	active, err := store.PushSubscriptionActive(ctx, device.ID)
	if err != nil || active {
		t.Fatal("stale delivery survived revoked grant", err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO administrator_remembered_logins(token_hash,administrator_id,generation,expires_at) VALUES ($1,1,0,$2)`, identity[:], time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.RememberedPushExpiry(ctx, identity, 0); err == nil {
		t.Fatal("expired remembered credential accepted")
	}
	_, err = pool.Exec(ctx, `UPDATE administrator_remembered_logins SET expires_at=now()+interval '1 hour'`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE administrator_credentials SET generation=1`)
	if err != nil {
		t.Fatal(err)
	}
	if active, err = store.PushSubscriptionActive(ctx, device.ID); err != nil || active {
		t.Fatal("generation change did not revoke push", err)
	}
}

func TestLogoutRevokesPreviousBrowserPushIdentityPostgreSQL(t *testing.T) {
	store, _, _, _ := integrationStore(t)
	ctx := context.Background()
	point, auth := notificationTestKeys(t)
	oldIdentity := sha256.Sum256([]byte("old-session-before-restart"))
	device, err := store.SavePushSubscription(ctx, storage.PushSecret{Endpoint: "https://fcm.googleapis.com/fcm/send/old-device", P256dh: point, Auth: auth}, "Windows", oldIdentity, 0, time.Now().Add(time.Hour), false)
	if err != nil {
		t.Fatal(err)
	}
	token := "new-session-after-api-restart"
	handler := New(store, &testXray{}, &testBrowser{}, Options{Token: token})
	logout := func(hash string) int {
		request := httptest.NewRequest("POST", "/api/v1/auth/logout", nil)
		request.RemoteAddr = "127.0.0.1:1234"
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Gateway-Push-Hash", hash)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Code
	}
	if code := logout("malformed"); code != 400 {
		t.Fatalf("malformed browser hash status=%d", code)
	}
	if active, _ := store.PushSubscriptionActive(ctx, device.ID); !active {
		t.Fatal("malformed hash changed the subscription")
	}
	if code := logout(""); code != 200 {
		t.Fatalf("legacy logout status=%d", code)
	}
	if active, _ := store.PushSubscriptionActive(ctx, device.ID); !active {
		t.Fatal("test did not preserve old session identity")
	}
	if code := logout(device.EndpointHash); code != 200 {
		t.Fatalf("new-session browser logout status=%d", code)
	}
	if active, _ := store.PushSubscriptionActive(ctx, device.ID); active {
		t.Fatal("old browser subscription survived new-session logout")
	}
}
