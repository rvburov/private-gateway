package httpapi

import (
	"context"
	"crypto/ecdh"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/go-chi/chi/v5"
	"github.com/rvburov/private-gateway/internal/storage"
)

type notificationService struct {
	server *Server
	client webpush.HTTPClient
	queue  chan storage.Notification
	mu     sync.Mutex
	tests  map[string]time.Time
	active bool
}

type notificationMessage struct {
	category string
	event    string
	title    string
	body     string
}

// В push и центре уведомлений используются только эти тексты: имена устройств, адреса,
// профили и произвольные подробности ошибок на экран блокировки не попадают.
var notificationMessages = []notificationMessage{
	{"devices", "device-added", "Добавлено устройство", "В шлюзе создан новый профиль подключения."},
	{"devices", "device-revoked", "Доступ устройства отозван", "Примените конфигурацию, чтобы отключить доступ устройства."},
	{"devices", "device-deleted", "Устройство удалено", "Отозванный профиль удалён из списка устройств."},
	{"devices", "subscription-renewed", "Ссылка-подписка обновлена", "Для устройства выпущена новая ссылка; прежняя больше не действует."},
	{"configuration", "configuration-apply-failed", "Не удалось применить конфигурацию", "Откройте панель и проверьте состояние Xray."},
	{"configuration", "configuration-applied", "Конфигурация применена", "Xray работает с обновлёнными настройками доступа."},
	{"server", "database-unavailable", "База данных не отвечает", "API не смог подключиться к PostgreSQL. Работа VPN проверяется отдельно в разделе «Сервер»."},
	{"server", "database-recovered", "Связь с базой данных восстановлена", "PostgreSQL снова отвечает API. Это не подтверждает работу VPN на вашем устройстве."},
	{"server", "xray-check-failed", "Проверка Xray выявила проблему", "Локальная проверка процесса Xray не прошла. VPN может не работать; откройте раздел «Сервер»."},
	{"server", "xray-configuration-pending", "Настройки Xray ещё не загружены", "Xray ещё не работает с последней конфигурацией. Повторите проверку в разделе «Сервер»."},
	{"server", "xray-recovered", "Xray снова работает", "Локальная проверка процесса Xray снова проходит. Подключение с вашего устройства проверяется отдельно."},
	{"server", "configuration-check-failed", "Не удалось проверить конфигурацию Xray", "API не смог прочитать версии конфигурации. Откройте раздел «Сервер» и повторите проверку."},
	{"server", "configuration-pending", "Есть неприменённые изменения Xray", "Новые устройства и отзывы доступа ещё не действуют. Примените конфигурацию на «Обзоре»."},
	{"server", "configuration-recovered", "Конфигурация Xray актуальна", "Желаемая и применённая версии конфигурации снова совпадают. Состояние процесса Xray проверяется отдельно."},
}

func notificationContent(item storage.Notification) notificationMessage {
	for _, message := range notificationMessages {
		if item.Category == message.category && item.Title == message.title {
			return message
		}
	}
	return notificationMessage{category: item.Category, event: "notification", title: "Новое уведомление", body: "В панели есть новое уведомление. Откройте раздел «Уведомления»."}
}

type notificationPushPayload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Tag   string `json:"tag"`
	URL   string `json:"url"`
	Event string `json:"event"`
	Test  bool   `json:"test"`
}

func notificationPayload(item storage.Notification, test bool) notificationPushPayload {
	message := notificationContent(item)
	if test {
		message = notificationMessage{event: "notification-test", title: "Проверка уведомлений", body: "Это тестовое сообщение. Доставка уведомлений на это устройство работает."}
	}
	return notificationPushPayload{Title: message.title, Body: message.body, Tag: "private-gateway-" + message.event, URL: "/?page=notifications", Event: message.event, Test: test}
}

// startNotifications не выполняет внешних запросов: доставка начинается только после подписки.
// nil отключает фоновые задачи в тестах; сохранение центра уведомлений работает и без них.
func (s *Server) startNotifications(ctx context.Context) {
	transport := http.DefaultTransport
	if base, ok := transport.(*http.Transport); ok {
		transport = base.Clone()
	}
	s.notifications = &notificationService{server: s, queue: make(chan storage.Notification, 64), tests: make(map[string]time.Time),
		client: &http.Client{Transport: transport, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if ctx == nil || s.store == nil {
		return
	}
	s.notifications.active = true
	go s.notifications.run(ctx)
}

// emitNotification не передаёт личные данные через push и не задерживает действие сетевой доставкой.
func (s *Server) emitNotification(category, title, body string) {
	if s.notifications == nil || s.store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	message := notificationContent(storage.Notification{Category: category, Title: title, Body: body})
	item, created, err := s.store.AddNotification(ctx, message.category, message.title, message.body)
	if err != nil || !created || !s.notifications.active {
		return
	}
	select {
	case s.notifications.queue <- item:
	default:
	}
}

func (s *Server) notificationList(w http.ResponseWriter, r *http.Request) {
	items, unread, err := s.store.Notifications(r.Context())
	if err != nil {
		fail(w, 503, "notifications unavailable")
		return
	}
	respond(w, 200, map[string]any{"items": items, "unread": unread})
}

func (s *Server) notificationRead(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		fail(w, 415, "Content-Type must be application/json")
		return
	}
	var input struct {
		IDs []string `json:"ids"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		fail(w, 400, "invalid notification IDs")
		return
	}
	err := s.store.ReadNotifications(r.Context(), input.IDs)
	if errors.Is(err, storage.ErrInvalid) {
		fail(w, 400, "invalid notification IDs")
		return
	}
	if err != nil {
		fail(w, 503, "notifications unavailable")
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}

func (s *Server) notificationPreferences(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPut {
		var input struct {
			Server        *bool `json:"server"`
			Devices       *bool `json:"devices"`
			Configuration *bool `json:"configuration"`
		}
		if !decode(w, r, &input) {
			return
		}
		if input.Server == nil || input.Devices == nil || input.Configuration == nil {
			fail(w, 400, "all notification preferences are required")
			return
		}
		prefs := storage.NotificationPreferences{Server: *input.Server, Devices: *input.Devices, Configuration: *input.Configuration}
		if err := s.store.SaveNotificationPreferences(r.Context(), prefs); err != nil {
			fail(w, 503, "notification preferences unavailable")
			return
		}
		respond(w, 200, prefs)
		return
	}
	prefs, err := s.store.NotificationPreferences(r.Context())
	if err != nil {
		fail(w, 503, "notification preferences unavailable")
		return
	}
	respond(w, 200, prefs)
}

func (s *Server) notificationKeys(ctx context.Context) (storage.NotificationKeys, error) {
	return s.store.NotificationKeys(ctx, func() (storage.NotificationKeys, error) {
		private, public, err := webpush.GenerateVAPIDKeys()
		return storage.NotificationKeys{Private: private, Public: public}, err
	})
}

func (s *Server) notificationPushKey(w http.ResponseWriter, r *http.Request) {
	keys, err := s.notificationKeys(r.Context())
	if err != nil {
		fail(w, 503, "push configuration unavailable")
		return
	}
	respond(w, 200, map[string]string{"public_key": keys.Public})
}

// Адрес может принадлежать только браузерному сервису push; редиректы при доставке запрещены.
func validPushEndpoint(endpoint string) bool {
	if len(endpoint) > 2048 || strings.ContainsAny(endpoint, "\r\n\t\\") {
		return false
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Fragment != "" || parsed.Opaque != "" || parsed.Port() != "" || parsed.Path == "" || parsed.Path == "/" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if net.ParseIP(host) != nil || strings.HasSuffix(host, ".") {
		return false
	}
	return host == "fcm.googleapis.com" || host == "updates.push.services.mozilla.com" || host == "web.push.apple.com" ||
		strings.HasSuffix(host, ".push.apple.com") || strings.HasSuffix(host, ".notify.windows.com")
}

func validPushKeys(point, auth string) bool {
	public, err := base64.RawURLEncoding.DecodeString(point)
	if err != nil || len(public) != 65 {
		return false
	}
	if _, err = ecdh.P256().NewPublicKey(public); err != nil {
		return false
	}
	secret, err := base64.RawURLEncoding.DecodeString(auth)
	return err == nil && len(secret) == 16
}

func validPushDevice(device string) bool {
	if utf8.RuneCountInString(device) > 80 || strings.TrimSpace(device) == "" {
		return false
	}
	for _, r := range device {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

type notificationIdentity struct {
	hash       [32]byte
	generation int64
	expires    time.Time
	remembered bool
}

func (s *Server) pushIdentity(r *http.Request) (notificationIdentity, error) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	identity := notificationIdentity{hash: sha256.Sum256([]byte(token))}
	s.loginMu.Lock()
	session, found := s.sessions[token]
	s.loginMu.Unlock()
	if found {
		identity.generation, identity.expires = session.generation, session.expires
		if session.rememberedKey != ([32]byte{}) {
			identity.hash, identity.remembered = session.rememberedKey, true
			expires, err := s.store.RememberedPushExpiry(r.Context(), identity.hash, identity.generation)
			identity.expires = expires
			return identity, err
		}
		return identity, nil
	}
	// Совместимость с закрытым API-токеном; сам токен в подписке не сохраняется.
	admin, err := s.administrator(r.Context())
	identity.generation, identity.expires = admin.Generation, time.Now().Add(8*time.Hour)
	return identity, err
}

func (s *Server) notificationSubscriptions(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var input struct {
			Endpoint string `json:"endpoint"`
			Keys     struct {
				P256dh string `json:"p256dh"`
				Auth   string `json:"auth"`
			} `json:"keys"`
			Device string `json:"device"`
		}
		if !decode(w, r, &input) {
			return
		}
		if !validPushEndpoint(input.Endpoint) || !validPushKeys(input.Keys.P256dh, input.Keys.Auth) || !validPushDevice(input.Device) {
			fail(w, 400, "invalid push subscription")
			return
		}
		if _, err := s.notificationKeys(r.Context()); err != nil {
			fail(w, 503, "push configuration unavailable")
			return
		}
		identity, err := s.pushIdentity(r)
		if err != nil {
			fail(w, 503, "push session unavailable")
			return
		}
		item, err := s.store.SavePushSubscription(r.Context(), storage.PushSecret{Endpoint: input.Endpoint, P256dh: input.Keys.P256dh, Auth: input.Keys.Auth}, strings.TrimSpace(input.Device), identity.hash, identity.generation, identity.expires, identity.remembered)
		if err != nil {
			fail(w, 503, "push subscription unavailable")
			return
		}
		respond(w, 200, item)
		return
	}
	deliveries, err := s.store.PushSubscriptions(r.Context())
	if err != nil {
		fail(w, 503, "push subscriptions unavailable")
		return
	}
	items := make([]storage.PushDevice, 0, len(deliveries))
	for _, delivery := range deliveries {
		items = append(items, delivery.PushDevice)
	}
	respond(w, 200, map[string]any{"items": items})
}

func (s *Server) notificationUnsubscribe(w http.ResponseWriter, r *http.Request) {
	err := s.store.DeletePushSubscription(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, storage.ErrInvalid) {
		fail(w, 400, "invalid subscription ID")
		return
	}
	if err != nil {
		fail(w, 503, "push subscription unavailable")
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}

func (s *Server) revokeSessionPush(ctx context.Context, token string) error {
	identity := sha256.Sum256([]byte(token))
	s.loginMu.Lock()
	if session, found := s.sessions[token]; found && session.rememberedKey != ([32]byte{}) {
		identity = session.rememberedKey
	}
	s.loginMu.Unlock()
	if s.store == nil {
		return nil
	}
	return s.store.RevokePushIdentity(ctx, identity)
}

func (s *Server) revokeBrowserPush(ctx context.Context, hash string) error {
	if hash == "" {
		return nil
	}
	if s.store == nil {
		return nil
	}
	return s.store.RevokePushEndpoint(ctx, hash)
}

func (s *Server) notificationTest(w http.ResponseWriter, r *http.Request) {
	var input struct {
		SubscriptionID string `json:"subscription_id"`
	}
	if !decode(w, r, &input) {
		return
	}
	items, err := s.store.PushSubscriptions(r.Context())
	if err != nil {
		fail(w, 503, "push subscriptions unavailable")
		return
	}
	var selected *storage.PushDelivery
	for _, item := range items {
		if item.ID == input.SubscriptionID {
			selected = &item
			break
		}
	}
	if selected == nil {
		fail(w, 404, "push subscription not found")
		return
	}
	n := s.notifications
	n.mu.Lock()
	last := n.tests[input.SubscriptionID]
	if time.Since(last) < time.Minute {
		n.mu.Unlock()
		fail(w, 429, "wait one minute before another test")
		return
	}
	for id, at := range n.tests {
		if time.Since(at) > time.Minute {
			delete(n.tests, id)
		}
	}
	n.tests[input.SubscriptionID] = time.Now()
	n.mu.Unlock()
	if err = n.send(r.Context(), *selected, true, storage.Notification{}); err != nil {
		fail(w, 503, "test notification could not be delivered; check browser permission and try again")
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}

func (n *notificationService) send(ctx context.Context, delivery storage.PushDelivery, test bool, event storage.Notification) error {
	active, err := n.server.store.PushSubscriptionActive(ctx, delivery.ID)
	if err != nil {
		return err
	}
	if !active {
		return errors.New("push subscription was revoked")
	}
	if !test {
		prefs, err := n.server.store.NotificationPreferences(ctx)
		if err != nil {
			return err
		}
		if !(event.Category == "server" && prefs.Server || event.Category == "devices" && prefs.Devices || event.Category == "configuration" && prefs.Configuration) {
			return nil
		}
	}
	if !validPushEndpoint(delivery.Secret.Endpoint) || !validPushKeys(delivery.Secret.P256dh, delivery.Secret.Auth) {
		return errors.New("invalid stored push subscription")
	}
	keys, err := n.server.notificationKeys(ctx)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(notificationPayload(event, test))
	requestCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	response, err := webpush.SendNotificationWithContext(requestCtx, payload, &webpush.Subscription{Endpoint: delivery.Secret.Endpoint, Keys: webpush.Keys{Auth: delivery.Secret.Auth, P256dh: delivery.Secret.P256dh}}, &webpush.Options{
		HTTPClient: n.client, Subscriber: n.subject(), TTL: 3600, Urgency: webpush.UrgencyNormal, VAPIDPublicKey: keys.Public, VAPIDPrivateKey: keys.Private,
	})
	if err != nil {
		return errors.New("push delivery failed")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode == 404 || response.StatusCode == 410 {
		_ = n.server.store.DeletePushSubscription(ctx, delivery.ID)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("push service returned %d", response.StatusCode)
	}
	return n.server.store.PushSent(ctx, delivery.ID)
}

func (n *notificationService) run(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	previous := n.health(ctx)
	for {
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			current := n.health(ctx)
			if ctx.Err() != nil {
				return
			}
			var events []storage.Notification
			previous, events = notificationHealthChanges(previous, current)
			for _, event := range events {
				n.server.emitNotification(event.Category, event.Title, event.Body)
			}
		case event := <-n.queue:
			deliveryCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
			items, err := n.server.store.PushSubscriptions(deliveryCtx)
			if err == nil {
				for _, item := range items {
					if deliveryCtx.Err() != nil {
						break
					}
					_ = n.send(deliveryCtx, item, false, event)
				}
			}
			cancel()
		}
	}
}

type notificationHealth struct {
	database      string
	xray          string
	configuration string
}

// Неизменные состояния не создают повторов. Пропуск проверки не означает восстановления и
// не стирает последнюю известную неисправность; первое успешное наблюдение не считается сбоем.
func notificationHealthChanges(previous, current notificationHealth) (notificationHealth, []storage.Notification) {
	next := previous
	var events []storage.Notification
	for _, component := range []struct {
		before string
		after  string
		next   *string
		titles map[string]string
	}{
		{previous.database, current.database, &next.database, map[string]string{
			"fail": "База данных не отвечает", "ok": "Связь с базой данных восстановлена",
		}},
		{previous.xray, current.xray, &next.xray, map[string]string{
			"fail": "Проверка Xray выявила проблему", "warn": "Настройки Xray ещё не загружены", "ok": "Xray снова работает",
		}},
		{previous.configuration, current.configuration, &next.configuration, map[string]string{
			"fail": "Не удалось проверить конфигурацию Xray", "warn": "Есть неприменённые изменения Xray", "ok": "Конфигурация Xray актуальна",
		}},
	} {
		title, known := component.titles[component.after]
		if !known {
			continue
		}
		*component.next = component.after
		if component.before == component.after || component.after == "ok" && component.before != "fail" && component.before != "warn" {
			continue
		}
		message := notificationContent(storage.Notification{Category: "server", Title: title})
		events = append(events, storage.Notification{Category: message.category, Title: message.title, Body: message.body})
	}
	return next, events
}

// Фоновая проверка читает только локальную БД и состояние Xray, без внешних запросов. При
// отказе БД сохранить и доставить уведомление нельзя; переход сохраняется в памяти до её
// восстановления. Процесс Xray проверяется независимо: он может работать без PostgreSQL.
func (n *notificationService) health(ctx context.Context) notificationHealth {
	probe, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	result := notificationHealth{database: "ok", xray: "unknown", configuration: "unknown"}
	if n.server.store.Ping(probe) != nil {
		result.database = "fail"
	}
	if !n.server.options.XrayConfigured {
		return result
	}
	result.xray = n.server.checkXrayProcess(probe).Status
	if result.database == "ok" {
		result.configuration = n.server.checkConfiguration(probe).Status
	}
	return result
}

// Контакт VAPID берётся из конфигурации, а не из недоверенного заголовка Host.
func (n *notificationService) subject() string {
	domain := n.server.options.Checks.PanelDomain
	if domain != "" && !strings.ContainsAny(domain, "/:?#@ ") {
		return "https://" + domain
	}
	return "https://github.com/rvburov/private-gateway"
}
