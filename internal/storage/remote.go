package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/rvburov/private-gateway/internal/model"
	"github.com/rvburov/private-gateway/internal/storage/db"
	"github.com/rvburov/private-gateway/internal/xray"
)

var (
	ErrUnauthorized          = errors.New("unauthorized agent")
	ErrConnectionUnavailable = errors.New("node connection is not configured")
)

// AgentDesired передаёт только UUID устройства и открытые параметры узла.
// Параметры клиентского моста не меняют listener выходного сервера.
type AgentDesired struct {
	Revision int64         `json:"revision"`
	Node     model.Node    `json:"node"`
	Clients  []xray.Client `json:"clients"`
}

func validEndpoint(host string, port int) bool {
	if port < 1 || port > 65535 {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	if host == "" || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if c != '-' && !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') {
				return false
			}
		}
	}
	return true
}

func validateRemoteNode(node model.Node) error {
	if !validName.MatchString(strings.TrimSpace(node.Name)) ||
		!validEndpoint(node.Host, node.Port) || node.ServerName == "" ||
		node.PublicKey == "" || node.ShortID == "" {
		return ErrInvalid
	}
	if node.XHTTP != nil && (!validEndpoint(node.Host, node.XHTTP.Port) || node.XHTTP.PublicKey == "" || node.XHTTP.Path == "") {
		return ErrInvalid
	}
	if node.H3 != nil && (!validEndpoint(node.Host, node.H3.Port) || node.H3.ServerName == "" || node.H3.Path == "") {
		return ErrInvalid
	}
	const testCredential = "00000000-0000-4000-8000-000000000000"
	if _, err := xray.Profile(node, testCredential, "validation"); err != nil {
		return ErrInvalid
	}
	if node.XHTTP != nil {
		if _, err := xray.XHTTPProfile(node, testCredential, "validation"); err != nil {
			return ErrInvalid
		}
	}
	if node.H3 != nil {
		if _, err := xray.H3Profile(node, testCredential, "validation"); err != nil {
			return ErrInvalid
		}
	}
	return nil
}

func randomToken() (string, []byte, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	hash := sha256.Sum256([]byte(token))
	return token, hash[:], nil
}

func marshalTransport(node model.Node) ([]byte, []byte, error) {
	var xhttp, h3 []byte
	var err error
	if node.XHTTP != nil {
		xhttp, err = json.Marshal(node.XHTTP)
		if err != nil {
			return nil, nil, ErrInvalid
		}
	}
	if node.H3 != nil {
		h3, err = json.Marshal(node.H3)
		if err != nil {
			return nil, nil, ErrInvalid
		}
	}
	return xhttp, h3, nil
}

func normalizeTransports(node *model.Node) {
	if node.XHTTP != nil && node.XHTTP.Mode == "" {
		node.XHTTP.Mode = "auto"
	}
	if node.H3 != nil && node.H3.Mode == "" {
		node.H3.Mode = "auto"
	}
}

func (s *Store) CreateRemoteNode(ctx context.Context, node model.Node) (model.Node, string, time.Time, error) {
	normalizeTransports(&node)
	if err := validateRemoteNode(node); err != nil {
		return model.Node{}, "", time.Time{}, err
	}
	node.ID = uuid.NewString()
	xhttp, h3, err := marshalTransport(node)
	if err != nil {
		return model.Node{}, "", time.Time{}, err
	}
	join, hash, err := randomToken()
	if err != nil {
		return model.Node{}, "", time.Time{}, err
	}
	expires := time.Now().Add(15 * time.Minute).UTC()
	var out model.Node
	err = s.transaction(ctx, func(q *db.Queries) error {
		row, err := q.InsertRemoteNode(ctx, db.InsertRemoteNodeParams{
			ID: uid(node.ID), Name: strings.TrimSpace(node.Name), Host: node.Host,
			Port: int32(node.Port), ServerName: node.ServerName, PublicKey: node.PublicKey,
			ShortID: node.ShortID, ConfigHash: "remote:" + node.ID,
			XhttpConfig: xhttp, H3Config: h3, AgentJoinHash: hash,
			AgentJoinExpiresAt: pgtype.Timestamptz{Time: expires, Valid: true},
		})
		if err != nil {
			return err
		}
		out, err = nodeModel(row)
		if err != nil {
			return err
		}
		return q.InsertAudit(ctx, db.InsertAuditParams{Action: "node.create"})
	})
	return out, join, expires, err
}

func (s *Store) Node(ctx context.Context, nodeID string) (model.Node, error) {
	if _, err := uuid.Parse(nodeID); err != nil {
		return model.Node{}, ErrNotFound
	}
	row, err := s.queries.GetNode(ctx, uid(nodeID))
	if err != nil {
		return model.Node{}, classify(err)
	}
	return nodeModel(row)
}

func (s *Store) UpdateRemoteNode(ctx context.Context, node model.Node) (model.Node, error) {
	normalizeTransports(&node)
	if _, err := uuid.Parse(node.ID); err != nil {
		return model.Node{}, ErrNotFound
	}
	if err := validateRemoteNode(node); err != nil {
		return model.Node{}, err
	}
	xhttp, h3, err := marshalTransport(node)
	if err != nil {
		return model.Node{}, err
	}
	var out model.Node
	err = s.transaction(ctx, func(q *db.Queries) error {
		row, err := q.UpdateRemoteNode(ctx, db.UpdateRemoteNodeParams{
			ID: uid(node.ID), Name: strings.TrimSpace(node.Name), Host: node.Host,
			Port: int32(node.Port), ServerName: node.ServerName, PublicKey: node.PublicKey,
			ShortID: node.ShortID, XhttpConfig: xhttp, H3Config: h3,
		})
		if err != nil {
			return err
		}
		out, err = nodeModel(row)
		if err != nil {
			return err
		}
		return q.InsertAudit(ctx, db.InsertAuditParams{Action: "node.update"})
	})
	return out, err
}

func (s *Store) DeleteRemoteNode(ctx context.Context, nodeID string) error {
	if _, err := uuid.Parse(nodeID); err != nil {
		return ErrNotFound
	}
	return s.transaction(ctx, func(q *db.Queries) error {
		row, err := q.GetNode(ctx, uid(nodeID))
		if err != nil {
			return err
		}
		if row.Kind != "remote" {
			return ErrInvalid
		}
		n, err := q.DeleteRemoteNode(ctx, uid(nodeID))
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrConflict
		}
		return q.InsertAudit(ctx, db.InsertAuditParams{Action: "node.delete"})
	})
}

func (s *Store) RotateAgentJoin(ctx context.Context, nodeID string) (string, time.Time, error) {
	if _, err := uuid.Parse(nodeID); err != nil {
		return "", time.Time{}, ErrNotFound
	}
	join, hash, err := randomToken()
	if err != nil {
		return "", time.Time{}, err
	}
	expires := time.Now().Add(15 * time.Minute).UTC()
	err = s.transaction(ctx, func(q *db.Queries) error {
		_, err := q.RotateAgentJoin(ctx, db.RotateAgentJoinParams{ID: uid(nodeID), AgentJoinHash: hash,
			AgentJoinExpiresAt: pgtype.Timestamptz{Time: expires, Valid: true}})
		if err != nil {
			return err
		}
		return q.InsertAudit(ctx, db.InsertAuditParams{Action: "node.rotate_join"})
	})
	return join, expires, err
}

func (s *Store) RequestRemoteApply(ctx context.Context, nodeID string) (model.Node, error) {
	if _, err := uuid.Parse(nodeID); err != nil {
		return model.Node{}, ErrNotFound
	}
	var out model.Node
	err := s.transaction(ctx, func(q *db.Queries) error {
		row, err := q.SetRequestedRevision(ctx, uid(nodeID))
		if err != nil {
			return err
		}
		out, err = nodeModel(row)
		if err != nil {
			return err
		}
		return q.InsertAudit(ctx, db.InsertAuditParams{Action: "node.apply_request"})
	})
	return out, err
}

func (s *Store) EnrollAgent(ctx context.Context, nodeID, join string) (string, error) {
	if _, err := uuid.Parse(nodeID); err != nil {
		return "", ErrUnauthorized
	}
	if len(join) != 43 {
		return "", ErrUnauthorized
	}
	permanent, hash, err := randomToken()
	if err != nil {
		return "", err
	}
	joinHash := sha256.Sum256([]byte(join))
	err = s.transaction(ctx, func(q *db.Queries) error {
		_, err := q.EnrollAgent(ctx, db.EnrollAgentParams{ID: uid(nodeID), AgentJoinHash: joinHash[:], AgentTokenHash: hash})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUnauthorized
		}
		return err
	})
	if err != nil {
		return "", err
	}
	return permanent, nil
}

func (s *Store) agentNode(ctx context.Context, nodeID, token string) (db.Node, []byte, error) {
	if _, err := uuid.Parse(nodeID); err != nil {
		return db.Node{}, nil, ErrUnauthorized
	}
	if len(token) != 43 {
		return db.Node{}, nil, ErrUnauthorized
	}
	hash := sha256.Sum256([]byte(token))
	node, err := s.queries.GetAgentNode(ctx, db.GetAgentNodeParams{ID: uid(nodeID), AgentTokenHash: hash[:]})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Node{}, nil, ErrUnauthorized
	}
	return node, hash[:], err
}

func (s *Store) AgentDesired(ctx context.Context, nodeID, token string) (*AgentDesired, error) {
	node, hash, err := s.agentNode(ctx, nodeID, token)
	if err != nil {
		return nil, err
	}
	if err := s.queries.TouchAgent(ctx, db.TouchAgentParams{ID: node.ID, AgentTokenHash: hash}); err != nil {
		return nil, err
	}
	if node.RequestedRevision != node.Revision || node.AppliedRevision == node.Revision {
		return nil, nil
	}
	rows, err := s.queries.ActiveClients(ctx, node.ID)
	if err != nil {
		return nil, err
	}
	clients := make([]xray.Client, 0, len(rows))
	for _, row := range rows {
		credential, err := s.cipher.Decrypt(row.CredentialCiphertext, id(row.ID))
		if err != nil {
			return nil, err
		}
		clients = append(clients, xray.Client{ID: credential, Name: row.Name})
	}
	public, err := nodeModel(node)
	if err != nil {
		return nil, err
	}
	return &AgentDesired{Revision: node.Revision, Node: public, Clients: clients}, nil
}

func (s *Store) AgentAck(ctx context.Context, nodeID, token string, revision int64, failure string) error {
	node, hash, err := s.agentNode(ctx, nodeID, token)
	if err != nil {
		return err
	}
	if failure != "" {
		switch failure {
		case "invalid_config", "apply_failed", "metadata_mismatch", "internal_error":
		default:
			return ErrInvalid
		}
		count, err := s.queries.AgentFailure(ctx, db.AgentFailureParams{ID: node.ID, AgentTokenHash: hash, AgentError: failure, Revision: revision})
		if err != nil {
			return err
		}
		if count != 1 {
			return ErrConflict
		}
		return nil
	}
	count, err := s.queries.AgentAck(ctx, db.AgentAckParams{ID: node.ID, AppliedRevision: revision, AgentTokenHash: hash})
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrConflict
	}
	return nil
}
