package storage

import (
	"context"
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
	ErrNotFound    = errors.New("not found")
	ErrConflict    = errors.New("active device with this name already exists")
	ErrRevoked     = errors.New("device is revoked")
	ErrInvalid     = errors.New("invalid device name or node ID")
	ErrStaleConfig = errors.New("node settings changed; restart this API process")
	validName      = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N} ._-]{0,63}$`)
)

type Store struct {
	pool       *pgxpool.Pool
	queries    *db.Queries
	cipher     *secure.Cipher
	configHash atomic.Value
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

// All config mutations and deployments share a database lock, including across API processes.
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
	err := s.transaction(ctx, func(q *db.Queries) error {
		if err := q.UpsertNode(ctx, db.UpsertNodeParams{ID: uid(node.ID), Name: node.Name, Host: node.Host, Port: int32(node.Port), ServerName: node.ServerName, PublicKey: node.PublicKey, ShortID: node.ShortID, ConfigHash: node.ConfigHash}); err != nil {
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
		out = append(out, nodeModel(r))
	}
	return out, nil
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
func (s *Store) Export(ctx context.Context, deviceID string) (model.Profile, error) {
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
		credential, err := s.cipher.Decrypt(row.CredentialCiphertext, id(row.ID))
		if err != nil {
			return err
		}
		result.URI, err = xray.Profile(nodeModel(node), credential, row.Name)
		if err != nil {
			return err
		}
		return q.InsertAudit(ctx, db.InsertAuditParams{Action: "device.export", DeviceID: row.ID})
	})
	return result, err
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
		expected, initialized := s.configHash.Load().(string)
		if !initialized || node.ConfigHash != expected {
			return ErrStaleConfig
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
			clients = append(clients, xray.Client{ID: credential, Name: row.Name})
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
	result := model.Device{ID: id(d.ID), NodeID: id(d.NodeID), Name: d.Name, CreatedAt: d.CreatedAt.Time}
	if d.RevokedAt.Valid {
		result.RevokedAt = &d.RevokedAt.Time
	}
	return result
}
func nodeModel(n db.Node) model.Node {
	return model.Node{ID: id(n.ID), Name: n.Name, Host: n.Host, Port: int(n.Port), ServerName: n.ServerName, PublicKey: n.PublicKey, ShortID: n.ShortID}
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
