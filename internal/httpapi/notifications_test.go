package httpapi

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rvburov/private-gateway/internal/secure"
	"github.com/rvburov/private-gateway/internal/storage"
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
	if err = api.notifications.send(ctx, stale, false, "server"); err != nil || calls != 0 {
		t.Fatal("queued category bypassed disabled preferences", err)
	}
	if err = api.notifications.send(ctx, stale, false, "devices"); err != nil || calls != 1 {
		t.Fatal("allowed category not sent", err)
	}
	if err = store.DeletePushSubscription(ctx, device.ID); err != nil {
		t.Fatal(err)
	}
	if err = api.notifications.send(ctx, stale, true, ""); err == nil || calls != 1 {
		t.Fatal("stale target sent after logout/revoke")
	}
	device, err = store.SavePushSubscription(ctx, stale.Secret, "Chrome", identity, 0, time.Now().Add(time.Hour), false)
	if err != nil {
		t.Fatal(err)
	}
	stale.ID = device.ID
	status = 410
	if err = api.notifications.send(ctx, stale, true, ""); err == nil {
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
