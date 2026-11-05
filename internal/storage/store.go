package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rvburov/private-gateway/internal/model"
	"github.com/rvburov/private-gateway/internal/secure"
	"github.com/rvburov/private-gateway/internal/storage/db"
	"github.com/rvburov/private-gateway/internal/xray"
)

const LocalNodeID = "00000000-0000-4000-8000-000000000001"

var (
	ErrNotFound         = errors.New("not found")
	ErrConflict         = errors.New("active device with this name already exists")
	ErrRevoked          = errors.New("device is revoked")
	ErrNotRevoked       = errors.New("revoke the device before deleting it")
	ErrPendingConfig    = errors.New("apply the pending Xray configuration before deleting the device")
	ErrInvalid          = errors.New("invalid device name or node ID")
	ErrStaleConfig      = errors.New("node settings changed; restart this API process")
	ErrProxyUnavailable = errors.New("proxy exit is not available")
	validName           = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N} ._-]{0,63}$`)
)

type Store struct {
	pool         *pgxpool.Pool
	queries      *db.Queries
	cipher       *secure.Cipher
	configHash   atomic.Value
	proxyEnabled atomic.Bool
	publicNode   atomic.Pointer[model.Node]
}

func Open(ctx context.Context, dsn string, cipher *secure.Cipher) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("invalid database configuration")
	}
	cfg.MaxConns = 10
	cfg.MinConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err = pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, errors.New("PostgreSQL unavailable")
	}
	return &Store{pool: pool, queries: db.New(pool), cipher: cipher}, nil
}
func (s *Store) Close()                         { s.pool.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Все изменения и применения конфигурации делят одну блокировку в базе, в том числе между
// процессами API.
func (s *Store) transaction(ctx context.Context, fn func(*db.Queries) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(9041201)"); err != nil {
		return err
	}
	if err = fn(s.queries.WithTx(tx)); err != nil {
		return classify(err)
	}
	return tx.Commit(ctx)
}

func (s *Store) EnsureNode(ctx context.Context, node model.Node) error {
	var xhttpConfig []byte
	if node.XHTTP != nil {
		var err error
		xhttpConfig, err = json.Marshal(node.XHTTP)
		if err != nil {
			return errors.New("invalid XHTTP node settings")
		}
	}
	err := s.transaction(ctx, func(q *db.Queries) error {
		if err := q.UpsertNode(ctx, db.UpsertNodeParams{ID: uid(node.ID), Name: node.Name, Host: node.Host, Port: int32(node.Port), ServerName: node.ServerName, PublicKey: node.PublicKey, ShortID: node.ShortID, ConfigHash: node.ConfigHash, XhttpConfig: xhttpConfig}); err != nil {
			return err
		}
		clients, err := q.ActiveClients(ctx, uid(node.ID))
		if err != nil {
			return err
		}
		for _, d := range clients {
			if _, err := s.cipher.Decrypt(d.CredentialCiphertext, id(d.ID)); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		s.configHash.Store(node.ConfigHash)
		s.proxyEnabled.Store(node.ProxyAvailable)
		configured := node
		s.publicNode.Store(&configured)
	}
	return err
}
func (s *Store) Nodes(ctx context.Context) ([]model.Node, error) {
	rows, err := s.queries.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]model.Node, 0, len(rows))
	for _, r := range rows {
		node, err := nodeModel(r)
		if err != nil {
			return nil, err
		}
		node = s.withExpectedIPs(node, r.ConfigHash)
		expected, initialized := s.configHash.Load().(string)
		node.ProxyAvailable = s.proxyEnabled.Load() && initialized && r.ConfigHash == expected &&
			r.Revision == r.AppliedRevision
		out = append(out, node)
	}
	return out, nil
}

// Ожидаемые адреса принадлежат настройкам запущенного API, а не адресу входа в SOCKS5.
func (s *Store) withExpectedIPs(node model.Node, configHash string) model.Node {
	configured := s.publicNode.Load()
	if configured != nil && configured.ID == node.ID && configured.ConfigHash == configHash {
		node.ExpectedServerIP = configured.ExpectedServerIP
		node.ExpectedProxyIP = configured.ExpectedProxyIP
	}
	return node
}
func (s *Store) Devices(ctx context.Context) ([]model.Device, error) {
	rows, err := s.queries.ListDevices(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]model.Device, 0, len(rows))
	for _, r := range rows {
		out = append(out, deviceModel(r))
	}
	return out, nil
}

// ActiveTrafficIDs сопоставляет закрытые идентификаторы пользователей Xray с открытыми
// идентификаторами устройств, не раскрывая секреты в ответах API и журналах.
func (s *Store) ActiveTrafficIDs(ctx context.Context) (map[string]string, error) {
	rows, err := s.queries.ActiveClients(ctx, uid(LocalNodeID))
	if err != nil {
		return nil, err
	}
	ids := make(map[string]string, len(rows))
	for _, row := range rows {
		credential, err := s.cipher.Decrypt(row.CredentialCiphertext, id(row.ID))
		if err != nil {
			return nil, err
		}
		ids[strings.ToLower(credential)] = id(row.ID)
		if s.proxyEnabled.Load() {
			ids[s.cipher.ProxyCredential(id(row.ID))] = id(row.ID)
		}
	}
	return ids, nil
}
func (s *Store) Create(ctx context.Context, name, nodeID string) (model.Device, error) {
	name = strings.TrimSpace(name)
	if !validName.MatchString(name) || nodeID != LocalNodeID {
		return model.Device{}, ErrInvalid
	}
	deviceID := uuid.NewString()
	ciphertext, err := s.cipher.Encrypt(uuid.NewString(), deviceID)
	if err != nil {
		return model.Device{}, err
	}
	var result model.Device
	err = s.transaction(ctx, func(q *db.Queries) error {
		row, err := q.InsertDevice(ctx, db.InsertDeviceParams{ID: uid(deviceID), NodeID: uid(nodeID), Name: name, CredentialCiphertext: ciphertext})
		if err != nil {
			return err
		}
		result = deviceModel(row)
		if err = q.AdvanceRevision(ctx, uid(nodeID)); err != nil {
			return err
		}
		return q.InsertAudit(ctx, db.InsertAuditParams{Action: "device.create", DeviceID: uid(deviceID)})
	})
	return result, err
}

// UpdateDevice меняет только метаданные: доступ, подписка и конфигурация Xray остаются прежними.
func (s *Store) UpdateDevice(ctx context.Context, deviceID string, name, preferredExit *string) (model.Device, error) {
	if _, err := uuid.Parse(deviceID); err != nil {
		return model.Device{}, ErrNotFound
	}
	if name == nil && preferredExit == nil {
		return model.Device{}, ErrInvalid
	}
	var normalizedName string
	if name != nil {
		normalizedName = strings.TrimSpace(*name)
		if !validName.MatchString(normalizedName) {
			return model.Device{}, ErrInvalid
		}
	}
	if preferredExit != nil && *preferredExit != "server" && *preferredExit != "proxy" {
		return model.Device{}, ErrInvalid
	}
	var result model.Device
	err := s.transaction(ctx, func(q *db.Queries) error {
		row, err := q.GetDevice(ctx, uid(deviceID))
		if err != nil {
			return err
		}
		updatedName, updatedExit := row.Name, row.PreferredExit
		if name != nil {
			updatedName = normalizedName
		}
		if preferredExit != nil {
			if row.RevokedAt.Valid {
				return ErrRevoked
			}
			if *preferredExit == "proxy" {
				node, err := q.GetNode(ctx, row.NodeID)
				if err != nil {
					return err
				}
				if err := s.checkConfigHash(node.ConfigHash); err != nil {
					return err
				}
				if !s.proxyEnabled.Load() || node.Revision != node.AppliedRevision {
					return ErrProxyUnavailable
				}
			}
			updatedExit = *preferredExit
		}
		if updatedName == row.Name && updatedExit == row.PreferredExit {
			result = deviceModel(row)
			return nil
		}
		updated, err := q.UpdateDeviceMetadata(ctx, db.UpdateDeviceMetadataParams{ID: row.ID, Name: updatedName, PreferredExit: updatedExit})
		if err != nil {
			return err
		}
		if updatedName != row.Name {
			if err = q.InsertAudit(ctx, db.InsertAuditParams{Action: "device.rename", DeviceID: row.ID}); err != nil {
				return err
			}
		}
		if updatedExit != row.PreferredExit {
			if err = q.InsertAudit(ctx, db.InsertAuditParams{Action: "device.exit", DeviceID: row.ID}); err != nil {
				return err
			}
		}
		result = deviceModel(updated)
		return nil
	})
	return result, err
}

func (s *Store) Revoke(ctx context.Context, deviceID string) (model.Device, error) {
	if _, err := uuid.Parse(deviceID); err != nil {
		return model.Device{}, ErrNotFound
	}
	var result model.Device
	err := s.transaction(ctx, func(q *db.Queries) error {
		row, err := q.GetDevice(ctx, uid(deviceID))
		if err != nil {
			return err
		}
		if row.RevokedAt.Valid {
			result = deviceModel(row)
			return nil
		}
		row, err = q.RevokeDevice(ctx, uid(deviceID))
		if err != nil {
			return err
		}
		result = deviceModel(row)
		if err = q.AdvanceRevision(ctx, row.NodeID); err != nil {
			return err
		}
		return q.InsertAudit(ctx, db.InsertAuditParams{Action: "device.revoke", DeviceID: row.ID})
	})
	return result, err
}
func (s *Store) DeleteRevoked(ctx context.Context, deviceID string) error {
	if _, err := uuid.Parse(deviceID); err != nil {
		return ErrNotFound
	}
	return s.transaction(ctx, func(q *db.Queries) error {
		row, err := q.GetDevice(ctx, uid(deviceID))
		if err != nil {
			return err
		}
		if !row.RevokedAt.Valid {
			return ErrNotRevoked
		}
		node, err := q.GetNode(ctx, row.NodeID)
		if err != nil {
			return err
		}
		if node.AppliedRevision != node.Revision {
			return ErrPendingConfig
		}
		deleted, err := q.DeleteRevokedDevice(ctx, row.ID)
		if err != nil {
			return err
		}
		if deleted != 1 {
			return ErrNotFound
		}
		return q.InsertAudit(ctx, db.InsertAuditParams{Action: "device.delete", DeviceID: row.ID})
	})
}
func (s *Store) Export(ctx context.Context, deviceID string) (model.Profile, error) {
	return s.ExportForExit(ctx, deviceID, "server")
}

// ExportForExit выдаёт профиль выбранного выхода. Прежний путь без выбора сохраняет
// исходный UUID устройства и исходную ссылку-подписку.
func (s *Store) ExportForExit(ctx context.Context, deviceID, exit string) (model.Profile, error) {
	if exit != "server" && exit != "proxy" {
		return model.Profile{}, ErrInvalid
	}
	if _, err := uuid.Parse(deviceID); err != nil {
		return model.Profile{}, ErrNotFound
	}
	var result model.Profile
	err := s.transaction(ctx, func(q *db.Queries) error {
		row, err := q.GetDevice(ctx, uid(deviceID))
		if err != nil {
			return err
		}
		if row.RevokedAt.Valid {
			return ErrRevoked
		}
		node, err := q.GetNode(ctx, row.NodeID)
		if err != nil {
			return err
		}
		if err := s.checkConfigHash(node.ConfigHash); err != nil {
			return err
		}
		if exit == "proxy" && (!s.proxyEnabled.Load() || node.Revision != node.AppliedRevision) {
			return ErrProxyUnavailable
		}
		credential, err := s.cipher.Decrypt(row.CredentialCiphertext, id(row.ID))
		if err != nil {
			return err
		}
		if exit == "proxy" {
			credential = s.cipher.ProxyCredential(id(row.ID))
		}
		clientNode, err := nodeModel(node)
		if err != nil {
			return err
		}
		clientNode = s.withExpectedIPs(clientNode, node.ConfigHash)
		profileName := row.Name
		if exit == "proxy" {
			profileName += " · IP прокси"
		}
		result, err = xray.ClientProfiles(clientNode, credential, profileName)
		result.Port = clientNode.Port
		if err != nil {
			return err
		}
		result.ExpectedExitIP = clientNode.ExpectedServerIP
		if exit == "proxy" {
			result.ExpectedExitIP = clientNode.ExpectedProxyIP
		}
		// Ссылка вычисляется заново при каждой выдаче; после показа хранится только её хеш.
		token := s.cipher.SubscriptionToken(id(row.ID), row.SubscriptionGeneration)
		if hash := secure.SubscriptionHash(token); !bytes.Equal(row.SubscriptionHash, hash) {
			if err = q.SetSubscriptionHash(ctx, db.SetSubscriptionHashParams{ID: row.ID, SubscriptionHash: hash}); err != nil {
				return err
			}
		}
		result.SubscriptionPath = SubscriptionPathPrefix + token
		if exit == "proxy" {
			result.SubscriptionPath = ProxySubscriptionPathPrefix + token
		}
		return q.InsertAudit(ctx, db.InsertAuditParams{Action: "device.export", DeviceID: row.ID})
	})
	return result, err
}

// ProbeCredential возвращает узел и секрет одного активного устройства для собственной проверки
// трафика сервером — самого старого: его Xray скорее всего уже получил. Если активных устройств
// нет, возвращает ErrNotFound.
func (s *Store) ProbeCredential(ctx context.Context) (model.Node, string, error) {
	rows, err := s.queries.ActiveClients(ctx, uid(LocalNodeID))
	if err != nil {
		return model.Node{}, "", err
	}
	if len(rows) == 0 {
		return model.Node{}, "", ErrNotFound
	}
	oldest := rows[0]
	for _, row := range rows[1:] {
		if row.CreatedAt.Time.Before(oldest.CreatedAt.Time) {
			oldest = row
		}
	}
	node, err := s.queries.GetNode(ctx, uid(LocalNodeID))
	if err != nil {
		return model.Node{}, "", classify(err)
	}
	credential, err := s.cipher.Decrypt(oldest.CredentialCiphertext, id(oldest.ID))
	if err != nil {
		return model.Node{}, "", err
	}
	if err = s.checkConfigHash(node.ConfigHash); err != nil {
		return model.Node{}, "", err
	}
	clientNode, err := nodeModel(node)
	if err != nil {
		return model.Node{}, "", err
	}
	return clientNode, credential, nil
}

// SubscriptionPathPrefix — путь, по которому открытый адрес подписки выдаёт профили.
const SubscriptionPathPrefix = "/sub/"

// ProxySubscriptionPathPrefix содержит выбор выхода в пути: клиент не может
// случайно отбросить query и получить прямой выход при обновлении подписки.
const ProxySubscriptionPathPrefix = "/sub/proxy/"

// RotateSubscription выдаёт активному устройству новую ссылку-подписку; прежняя сразу перестаёт
// работать.
func (s *Store) RotateSubscription(ctx context.Context, deviceID string) (string, error) {
	return s.RotateSubscriptionForExit(ctx, deviceID, "server")
}

// RotateSubscriptionForExit меняет общий секрет обеих ссылок устройства. Суффикс
// выхода определяет только возвращаемый URL; прежняя ссылка всегда остаётся без него.
func (s *Store) RotateSubscriptionForExit(ctx context.Context, deviceID, exit string) (string, error) {
	if exit != "server" && exit != "proxy" {
		return "", ErrInvalid
	}
	if _, err := uuid.Parse(deviceID); err != nil {
		return "", ErrNotFound
	}
	var path string
	err := s.transaction(ctx, func(q *db.Queries) error {
		row, err := q.GetDevice(ctx, uid(deviceID))
		if err != nil {
			return err
		}
		if row.RevokedAt.Valid {
			return ErrRevoked
		}
		if exit == "proxy" {
			node, err := q.GetNode(ctx, row.NodeID)
			if err != nil {
				return err
			}
			if err := s.checkConfigHash(node.ConfigHash); err != nil {
				return err
			}
			if !s.proxyEnabled.Load() || node.Revision != node.AppliedRevision {
				return ErrProxyUnavailable
			}
		}
		if row, err = q.AdvanceSubscription(ctx, row.ID); err != nil {
			return err
		}
		token := s.cipher.SubscriptionToken(id(row.ID), row.SubscriptionGeneration)
		if err = q.SetSubscriptionHash(ctx, db.SetSubscriptionHashParams{ID: row.ID, SubscriptionHash: secure.SubscriptionHash(token)}); err != nil {
			return err
		}
		path = SubscriptionPathPrefix + token
		if exit == "proxy" {
			path = ProxySubscriptionPathPrefix + token
		}
		return q.InsertAudit(ctx, db.InsertAuditParams{Action: "device.subscription_rotate", DeviceID: row.ID})
	})
	return path, err
}

// Subscription возвращает профиль и имя активного устройства, в ссылке которого этот токен.
// Отозванное устройство и заменённая ссылка не находятся. Клиенты регулярно опрашивают ссылки,
// поэтому чтения не попадают в аудит.
func (s *Store) Subscription(ctx context.Context, token string) (model.Profile, string, error) {
	return s.SubscriptionForExit(ctx, token, "server")
}

// SubscriptionForExit возвращает профиль нужного выхода по той же защищённой ссылке.
func (s *Store) SubscriptionForExit(ctx context.Context, token, exit string) (model.Profile, string, error) {
	if exit != "server" && exit != "proxy" {
		return model.Profile{}, "", ErrInvalid
	}
	row, err := s.queries.GetActiveDeviceBySubscription(ctx, secure.SubscriptionHash(token))
	if err != nil {
		return model.Profile{}, "", classify(err)
	}
	node, err := s.queries.GetNode(ctx, row.NodeID)
	if err != nil {
		return model.Profile{}, "", classify(err)
	}
	if err = s.checkConfigHash(node.ConfigHash); err != nil {
		return model.Profile{}, "", err
	}
	if exit == "proxy" && (!s.proxyEnabled.Load() || node.Revision != node.AppliedRevision) {
		return model.Profile{}, "", ErrProxyUnavailable
	}
	credential, err := s.cipher.Decrypt(row.CredentialCiphertext, id(row.ID))
	if err != nil {
		return model.Profile{}, "", err
	}
	if exit == "proxy" {
		credential = s.cipher.ProxyCredential(id(row.ID))
	}
	clientNode, err := nodeModel(node)
	if err != nil {
		return model.Profile{}, "", err
	}
	clientNode = s.withExpectedIPs(clientNode, node.ConfigHash)
	// Публичная подписка не должна рекламировать новый вход до успешного
	// применения текущей ревизии. Закрытый Export остаётся доступен для подготовки.
	if node.Revision != node.AppliedRevision {
		clientNode.XHTTP = nil
	}
	profileName := row.Name
	if exit == "proxy" {
		profileName += " · IP прокси"
	}
	profile, err := xray.ClientProfiles(clientNode, credential, profileName)
	profile.Port = clientNode.Port
	if err != nil {
		return model.Profile{}, "", err
	}
	profile.ExpectedExitIP = clientNode.ExpectedServerIP
	if exit == "proxy" {
		profile.ExpectedExitIP = clientNode.ExpectedProxyIP
	}
	return profile, profileName, nil
}

type Status struct {
	Database        string `json:"database"`
	NodeID          string `json:"node_id"`
	Devices         int64  `json:"devices"`
	ActiveDevices   int64  `json:"active_devices"`
	DesiredRevision int64  `json:"desired_revision"`
	AppliedRevision int64  `json:"applied_revision"`
	XrayConfigured  bool   `json:"xray_configured"`
}

func (s *Store) Status(ctx context.Context) (Status, error) {
	node, err := s.queries.GetNode(ctx, uid(LocalNodeID))
	if err != nil {
		return Status{}, err
	}
	counts, err := s.queries.CountDevices(ctx, uid(LocalNodeID))
	if err != nil {
		return Status{}, err
	}
	return Status{Database: "ok", NodeID: LocalNodeID, Devices: counts.Total, ActiveDevices: counts.Active, DesiredRevision: node.Revision, AppliedRevision: node.AppliedRevision}, nil
}
func (s *Store) Audit(ctx context.Context) ([]model.AuditEvent, error) {
	rows, err := s.queries.ListAudit(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]model.AuditEvent, 0, len(rows))
	for _, r := range rows {
		item := model.AuditEvent{ID: r.ID, Action: r.Action, CreatedAt: r.CreatedAt.Time}
		if r.DeviceID.Valid {
			v := id(r.DeviceID)
			item.DeviceID = &v
		}
		out = append(out, item)
	}
	return out, nil
}
func (s *Store) Configure(ctx context.Context, apply bool, operation func(context.Context, []xray.Client) error) error {
	return s.transaction(ctx, func(q *db.Queries) error {
		node, err := q.GetNode(ctx, uid(LocalNodeID))
		if err != nil {
			return err
		}
		if err := s.checkConfigHash(node.ConfigHash); err != nil {
			return err
		}
		// Повторное применение не должно перезапускать Xray и отключать активных клиентов.
		if apply && node.AppliedRevision == node.Revision {
			return nil
		}
		rows, err := q.ActiveClients(ctx, uid(LocalNodeID))
		if err != nil {
			return err
		}
		clients := make([]xray.Client, 0, len(rows))
		for _, row := range rows {
			credential, err := s.cipher.Decrypt(row.CredentialCiphertext, id(row.ID))
			if err != nil {
				return err
			}
			client := xray.Client{ID: credential, Name: row.Name}
			if s.proxyEnabled.Load() {
				client.ProxyID = s.cipher.ProxyCredential(id(row.ID))
			}
			clients = append(clients, client)
		}
		if err = operation(ctx, clients); err != nil {
			return err
		}
		action := "config.validate"
		if apply {
			action = "config.apply"
			if err = q.MarkApplied(ctx, uid(LocalNodeID)); err != nil {
				return err
			}
		}
		return q.InsertAudit(ctx, db.InsertAuditParams{Action: action})
	})
}
func uid(value string) pgtype.UUID {
	parsed, err := uuid.Parse(value)
	return pgtype.UUID{Bytes: parsed, Valid: err == nil}
}
func id(value pgtype.UUID) string { return uuid.UUID(value.Bytes).String() }
func deviceModel(d db.Device) model.Device {
	result := model.Device{ID: id(d.ID), NodeID: id(d.NodeID), Name: d.Name, PreferredExit: d.PreferredExit, CreatedAt: d.CreatedAt.Time}
	if d.RevokedAt.Valid {
		result.RevokedAt = &d.RevokedAt.Time
	}
	return result
}
func nodeModel(n db.Node) (model.Node, error) {
	node := model.Node{ID: id(n.ID), Name: n.Name, Host: n.Host, Port: int(n.Port), ServerName: n.ServerName, PublicKey: n.PublicKey, ShortID: n.ShortID}
	if len(n.XhttpConfig) > 0 {
		if err := json.Unmarshal(n.XhttpConfig, &node.XHTTP); err != nil {
			return model.Node{}, errors.New("invalid stored XHTTP node settings")
		}
	}
	return node, nil
}

func (s *Store) checkConfigHash(actual string) error {
	expected, initialized := s.configHash.Load().(string)
	if !initialized || actual != expected {
		return ErrStaleConfig
	}
	return nil
}
func classify(err error) error {
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) && pgerr.Code == "23505" {
		return ErrConflict
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
