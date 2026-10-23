package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type NotificationPreferences struct {
	Server        bool `json:"server"`
	Devices       bool `json:"devices"`
	Configuration bool `json:"configuration"`
}

type Notification struct {
	ID        string     `json:"id"`
	Category  string     `json:"category"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	CreatedAt time.Time  `json:"created_at"`
	ReadAt    *time.Time `json:"read_at"`
}

type NotificationKeys struct {
	Public  string `json:"public"`
	Private string `json:"private"`
}

type PushDevice struct {
	ID           string     `json:"id"`
	EndpointHash string     `json:"endpoint_hash"`
	Device       string     `json:"device"`
	CreatedAt    time.Time  `json:"created_at"`
	LastSentAt   *time.Time `json:"last_sent_at"`
}

type PushSecret struct {
	Endpoint string `json:"endpoint"`
	P256dh   string `json:"p256dh"`
	Auth     string `json:"auth"`
}

type PushDelivery struct {
	PushDevice
	Secret PushSecret
}

func (s *Store) NotificationPreferences(ctx context.Context) (NotificationPreferences, error) {
	prefs := NotificationPreferences{Server: true, Devices: true, Configuration: true}
	err := s.pool.QueryRow(ctx, `SELECT server, devices, configuration FROM notification_preferences WHERE id`).Scan(&prefs.Server, &prefs.Devices, &prefs.Configuration)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return prefs, err
}

func (s *Store) SaveNotificationPreferences(ctx context.Context, prefs NotificationPreferences) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO notification_preferences (id,server,devices,configuration) VALUES (true,$1,$2,$3)
		ON CONFLICT (id) DO UPDATE SET server=$1,devices=$2,configuration=$3`, prefs.Server, prefs.Devices, prefs.Configuration)
	return err
}

// NotificationKeys использует отдельную блокировку, чтобы два браузера не сменили ключ одновременно.
func (s *Store) NotificationKeys(ctx context.Context, generate func() (NotificationKeys, error)) (NotificationKeys, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return NotificationKeys{}, err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(9041206)`); err != nil {
		return NotificationKeys{}, err
	}
	var encrypted []byte
	err = tx.QueryRow(ctx, `SELECT ciphertext FROM notification_keys WHERE id`).Scan(&encrypted)
	var keys NotificationKeys
	if errors.Is(err, pgx.ErrNoRows) {
		keys, err = generate()
		if err != nil {
			return keys, err
		}
		payload, err := json.Marshal(keys)
		if err != nil {
			return keys, err
		}
		encrypted, err = s.cipher.Encrypt(string(payload), "notification-vapid-v1")
		if err != nil {
			return keys, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO notification_keys (id,ciphertext) VALUES (true,$1)`, encrypted)
		if err != nil {
			return keys, err
		}
	} else if err != nil {
		return keys, err
	} else {
		payload, err := s.cipher.Decrypt(encrypted, "notification-vapid-v1")
		if err != nil {
			return keys, err
		}
		if err = json.Unmarshal([]byte(payload), &keys); err != nil {
			return keys, err
		}
	}
	return keys, tx.Commit(ctx)
}

// AddNotification сохраняет только разрешённые категории, последние 200 записей и не более 30 дней.
func (s *Store) AddNotification(ctx context.Context, category, title, body string) (Notification, bool, error) {
	prefs, err := s.NotificationPreferences(ctx)
	if err != nil {
		return Notification{}, false, err
	}
	allowed := category == "server" && prefs.Server || category == "devices" && prefs.Devices || category == "configuration" && prefs.Configuration
	if !allowed {
		return Notification{}, false, nil
	}
	result := Notification{ID: uuid.NewString(), Category: category, Title: title, Body: body}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return result, false, err
	}
	defer tx.Rollback(context.Background())
	if err = tx.QueryRow(ctx, `INSERT INTO notifications (id,category,title,body) VALUES ($1,$2,$3,$4) RETURNING created_at`, result.ID, category, title, body).Scan(&result.CreatedAt); err != nil {
		return result, false, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM notifications WHERE created_at < now()-interval '30 days' OR id IN (SELECT id FROM notifications ORDER BY created_at DESC OFFSET 200)`); err != nil {
		return result, false, err
	}
	err = tx.Commit(ctx)
	return result, err == nil, err
}

func (s *Store) Notifications(ctx context.Context) ([]Notification, int, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text,category,title,body,created_at,read_at FROM notifications WHERE created_at >= now()-interval '30 days' ORDER BY created_at DESC LIMIT 200`)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	result := make([]Notification, 0)
	unread := 0
	for rows.Next() {
		var item Notification
		if err = rows.Scan(&item.ID, &item.Category, &item.Title, &item.Body, &item.CreatedAt, &item.ReadAt); err != nil {
			return nil, 0, err
		}
		if item.ReadAt == nil {
			unread++
		}
		result = append(result, item)
	}
	return result, unread, rows.Err()
}

func (s *Store) ReadNotifications(ctx context.Context, ids []string) error {
	if ids == nil {
		ids = []string{}
	}
	if len(ids) > 200 {
		return ErrInvalid
	}
	for _, id := range ids {
		if _, err := uuid.Parse(id); err != nil {
			return ErrInvalid
		}
	}
	_, err := s.pool.Exec(ctx, `UPDATE notifications SET read_at=COALESCE(read_at,now()) WHERE cardinality($1::uuid[])=0 OR id=ANY($1::uuid[])`, ids)
	return err
}

func (s *Store) SavePushSubscription(ctx context.Context, secret PushSecret, device string, identity [32]byte, generation int64, expires time.Time, remembered bool) (PushDevice, error) {
	hash := sha256.Sum256([]byte(secret.Endpoint))
	key := hex.EncodeToString(hash[:8])
	payload, err := json.Marshal(secret)
	if err != nil {
		return PushDevice{}, err
	}
	encrypted, err := s.cipher.Encrypt(string(payload), "notification-push/"+key)
	if err != nil {
		return PushDevice{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return PushDevice{}, err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `DELETE FROM notification_push_subscriptions WHERE expires_at<=now()`); err != nil {
		return PushDevice{}, err
	}
	var result PushDevice
	err = tx.QueryRow(ctx, `INSERT INTO notification_push_subscriptions (id,endpoint_hash,ciphertext,device,identity_hash,generation,expires_at,remembered)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(endpoint_hash) DO UPDATE SET ciphertext=$3,device=$4,identity_hash=$5,generation=$6,expires_at=$7,remembered=$8
		RETURNING id::text,endpoint_hash,device,created_at,last_sent_at`, uuid.NewString(), key, encrypted, device, identity[:], generation, expires, remembered).Scan(&result.ID, &result.EndpointHash, &result.Device, &result.CreatedAt, &result.LastSentAt)
	if err != nil {
		return result, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM notification_push_subscriptions WHERE id IN (SELECT id FROM notification_push_subscriptions ORDER BY created_at DESC OFFSET 16)`); err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

func (s *Store) PushSubscriptions(ctx context.Context) ([]PushDelivery, error) {
	admin, err := s.Administrator(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text,endpoint_hash,device,created_at,last_sent_at,ciphertext FROM notification_push_subscriptions WHERE expires_at>now() AND generation=$1 AND (NOT remembered OR EXISTS (
        SELECT 1 FROM administrator_remembered_logins l WHERE l.token_hash=identity_hash AND l.generation=$1 AND l.expires_at>now()
    )) ORDER BY created_at DESC LIMIT 16`, admin.Generation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]PushDelivery, 0)
	for rows.Next() {
		var item PushDelivery
		var encrypted []byte
		if err = rows.Scan(&item.ID, &item.EndpointHash, &item.Device, &item.CreatedAt, &item.LastSentAt, &encrypted); err != nil {
			return nil, err
		}
		payload, err := s.cipher.Decrypt(encrypted, "notification-push/"+item.EndpointHash)
		if err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(payload), &item.Secret); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) DeletePushSubscription(ctx context.Context, id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return ErrInvalid
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM notification_push_subscriptions WHERE id=$1`, id)
	return err
}

func (s *Store) RevokePushIdentity(ctx context.Context, identity [32]byte) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM notification_push_subscriptions WHERE identity_hash=$1`, identity[:])
	return err
}

// RevokePushEndpoint отзывает устройство после нового входа, даже если прежний сеанс потерян.
func (s *Store) RevokePushEndpoint(ctx context.Context, hash string) error {
	decoded, err := hex.DecodeString(hash)
	if err != nil || len(decoded) != 8 || len(hash) != 16 {
		return ErrInvalid
	}
	_, err = s.pool.Exec(ctx, `DELETE FROM notification_push_subscriptions WHERE endpoint_hash=$1`, strings.ToLower(hash))
	return err
}

func (s *Store) PushSent(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `UPDATE notification_push_subscriptions SET last_sent_at=now() WHERE id=$1`, id)
	return err
}

// PushSubscriptionActive повторно проверяет отзыв перед каждой сетевой доставкой.
func (s *Store) PushSubscriptionActive(ctx context.Context, id string) (bool, error) {
	admin, err := s.Administrator(ctx)
	if err != nil {
		return false, err
	}
	var active bool
	err = s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM notification_push_subscriptions p
		WHERE p.id=$1 AND p.generation=$2 AND p.expires_at>now() AND (NOT p.remembered OR EXISTS (
		SELECT 1 FROM administrator_remembered_logins l WHERE l.token_hash=p.identity_hash AND l.generation=$2 AND l.expires_at>now()
	)))`, id, admin.Generation).Scan(&active)
	return active, err
}

// RememberedPushExpiry не продлевает подписку дольше фактического срока запомненного входа.
func (s *Store) RememberedPushExpiry(ctx context.Context, identity [32]byte, generation int64) (time.Time, error) {
	var expires time.Time
	err := s.pool.QueryRow(ctx, `SELECT expires_at FROM administrator_remembered_logins WHERE token_hash=$1 AND generation=$2 AND expires_at>now()`, identity[:], generation).Scan(&expires)
	return expires, err
}
