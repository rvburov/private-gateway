package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/rvburov/private-gateway/internal/model"
	"github.com/rvburov/private-gateway/internal/secure"
	"github.com/rvburov/private-gateway/internal/storage/db"
	"github.com/rvburov/private-gateway/internal/xray"
)

var (
	ErrUnauthorized     = errors.New("unauthorized agent")
	ErrChainUnavailable = errors.New("chain is not available")
	validHost           = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$`)
)

const ChainSubscriptionPathPrefix = "/sub/entry/"

// AgentDesired передаёт только UUID устройства и открытые параметры узла.
// Пароль входного SOCKS5 на выходной сервер никогда не отправляется.
type AgentDesired struct {
	Revision int64         `json:"revision"`
	Node     model.Node    `json:"node"`
	Clients  []xray.Client `json:"clients"`
}

func validEndpoint(host string, port int) bool {
	return port > 0 && port <= 65535 && (net.ParseIP(host) != nil ||
		(validHost.MatchString(host) && !strings.Contains(host, "..")))
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

func (s *Store) AssignEntryProxy(ctx context.Context, nodeID string, proxyID *string) (model.Node, error) {
	if _, err := uuid.Parse(nodeID); err != nil {
		return model.Node{}, ErrNotFound
	}
	var proxy pgtype.UUID
	if proxyID != nil {
		if _, err := uuid.Parse(*proxyID); err != nil {
			return model.Node{}, ErrInvalid
		}
		proxy = uid(*proxyID)
	}
	var out model.Node
	err := s.transaction(ctx, func(q *db.Queries) error {
		if proxyID != nil {
			p, err := q.GetEntryProxy(ctx, proxy)
			if err != nil {
				return err
			}
			if !p.Enabled {
				return ErrChainUnavailable
			}
		}
		row, err := q.SetNodeEntryProxy(ctx, db.SetNodeEntryProxyParams{ID: uid(nodeID), EntryProxyID: proxy})
		if err != nil {
			return err
		}
		out, err = nodeModel(row)
		if err != nil {
			return err
		}
		return q.InsertAudit(ctx, db.InsertAuditParams{Action: "node.entry_proxy"})
	})
	return out, err
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

func entryProxyModel(p db.EntryProxy) model.EntryProxy {
	return model.EntryProxy{ID: id(p.ID), Name: p.Name, Host: p.Host, Port: int(p.Port),
		AuthType: p.AuthType, Enabled: p.Enabled, SupportsUDP: p.SupportsUdp}
}

type EntryProxyInput struct {
	Name        string `json:"name"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	AuthType    string `json:"auth_type"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	Enabled     bool   `json:"enabled"`
	SupportsUDP bool   `json:"supports_udp"`
}

func validSOCKSSecret(value string) bool {
	if len(value) < 1 || len(value) > 255 || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}

func (s *Store) EntryProxies(ctx context.Context) ([]model.EntryProxy, error) {
	rows, err := s.queries.ListEntryProxies(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]model.EntryProxy, 0, len(rows))
	for _, row := range rows {
		out = append(out, entryProxyModel(row))
	}
	return out, nil
}

func (s *Store) CreateEntryProxy(ctx context.Context, input EntryProxyInput) (model.EntryProxy, error) {
	if !validName.MatchString(strings.TrimSpace(input.Name)) || !validEndpoint(input.Host, input.Port) {
		return model.EntryProxy{}, ErrInvalid
	}
	if input.AuthType != "none" && input.AuthType != "password" {
		return model.EntryProxy{}, ErrInvalid
	}
	proxyID := uuid.NewString()
	auth := input.AuthType
	var username, password []byte
	var err error
	if auth == "password" {
		if !validSOCKSSecret(input.Username) || !validSOCKSSecret(input.Password) {
			return model.EntryProxy{}, ErrInvalid
		}
		username, err = s.cipher.Encrypt(input.Username, proxyID+":username")
		if err != nil {
			return model.EntryProxy{}, err
		}
		password, err = s.cipher.Encrypt(input.Password, proxyID+":password")
		if err != nil {
			return model.EntryProxy{}, err
		}
	}
	var out model.EntryProxy
	err = s.transaction(ctx, func(q *db.Queries) error {
		row, err := q.InsertEntryProxy(ctx, db.InsertEntryProxyParams{ID: uid(proxyID), Name: strings.TrimSpace(input.Name), Host: input.Host, Port: int32(input.Port), AuthType: auth, UsernameCiphertext: username, PasswordCiphertext: password, Enabled: input.Enabled, SupportsUdp: input.SupportsUDP})
		if err != nil {
			return err
		}
		out = entryProxyModel(row)
		return q.InsertAudit(ctx, db.InsertAuditParams{Action: "entry_proxy.create"})
	})
	return out, err
}

// UpdateEntryProxy сохраняет прежние реквизиты, когда оба поля пусты.
func (s *Store) UpdateEntryProxy(ctx context.Context, proxyID string, input EntryProxyInput) (model.EntryProxy, error) {
	if _, err := uuid.Parse(proxyID); err != nil {
		return model.EntryProxy{}, ErrNotFound
	}
	if !validName.MatchString(strings.TrimSpace(input.Name)) || !validEndpoint(input.Host, input.Port) {
		return model.EntryProxy{}, ErrInvalid
	}
	if input.AuthType != "none" && input.AuthType != "password" {
		return model.EntryProxy{}, ErrInvalid
	}
	var out model.EntryProxy
	err := s.transaction(ctx, func(q *db.Queries) error {
		previous, err := q.GetEntryProxy(ctx, uid(proxyID))
		if err != nil {
			return err
		}
		auth, username, password := input.AuthType, previous.UsernameCiphertext, previous.PasswordCiphertext
		if auth == "none" {
			username, password = nil, nil
		}
		if auth == "password" && (input.Username != "" || input.Password != "") {
			if !validSOCKSSecret(input.Username) || !validSOCKSSecret(input.Password) {
				return ErrInvalid
			}
			auth = "password"
			username, err = s.cipher.Encrypt(input.Username, proxyID+":username")
			if err != nil {
				return err
			}
			password, err = s.cipher.Encrypt(input.Password, proxyID+":password")
			if err != nil {
				return err
			}
		}
		if auth == "password" && (username == nil || password == nil) {
			return ErrInvalid
		}
		row, err := q.UpdateEntryProxy(ctx, db.UpdateEntryProxyParams{ID: uid(proxyID), Name: strings.TrimSpace(input.Name), Host: input.Host, Port: int32(input.Port), AuthType: auth, UsernameCiphertext: username, PasswordCiphertext: password, Enabled: input.Enabled, SupportsUdp: input.SupportsUDP})
		if err != nil {
			return err
		}
		out = entryProxyModel(row)
		return q.InsertAudit(ctx, db.InsertAuditParams{Action: "entry_proxy.update"})
	})
	return out, err
}

func (s *Store) DeleteEntryProxy(ctx context.Context, proxyID string) error {
	if _, err := uuid.Parse(proxyID); err != nil {
		return ErrNotFound
	}
	return s.transaction(ctx, func(q *db.Queries) error {
		count, err := q.DeleteEntryProxy(ctx, uid(proxyID))
		if err != nil {
			return err
		}
		if count != 1 {
			return ErrConflict
		}
		return q.InsertAudit(ctx, db.InsertAuditParams{Action: "entry_proxy.delete"})
	})
}

func (s *Store) ChainBundle(ctx context.Context, deviceID string) (model.ChainBundle, error) {
	if _, err := uuid.Parse(deviceID); err != nil {
		return model.ChainBundle{}, ErrNotFound
	}
	var out model.ChainBundle
	err := s.transaction(ctx, func(q *db.Queries) error {
		row, err := q.GetDevice(ctx, uid(deviceID))
		if err != nil {
			return err
		}
		if row.RevokedAt.Valid {
			return ErrRevoked
		}
		out, err = s.chainBundle(ctx, q, row)
		if err != nil {
			return err
		}
		token := s.cipher.ChainSubscriptionToken(id(row.ID), row.ChainSubscriptionGeneration)
		hash := secure.SubscriptionHash(token)
		if err = q.SetChainSubscriptionHash(ctx, db.SetChainSubscriptionHashParams{ID: row.ID, ChainSubscriptionHash: hash}); err != nil {
			return err
		}
		out.SubscriptionURL = ChainSubscriptionPathPrefix + token
		return q.InsertAudit(ctx, db.InsertAuditParams{Action: "device.chain_export", DeviceID: row.ID})
	})
	return out, err
}

func (s *Store) RotateChainSubscription(ctx context.Context, deviceID string) (string, error) {
	if _, err := uuid.Parse(deviceID); err != nil {
		return "", ErrNotFound
	}
	var path string
	err := s.transaction(ctx, func(q *db.Queries) error {
		row, err := q.AdvanceChainSubscription(ctx, uid(deviceID))
		if err != nil {
			return err
		}
		if _, err := s.chainBundle(ctx, q, row); err != nil {
			return err
		}
		token := s.cipher.ChainSubscriptionToken(id(row.ID), row.ChainSubscriptionGeneration)
		if err := q.SetChainSubscriptionHash(ctx, db.SetChainSubscriptionHashParams{ID: row.ID, ChainSubscriptionHash: secure.SubscriptionHash(token)}); err != nil {
			return err
		}
		path = ChainSubscriptionPathPrefix + token
		return q.InsertAudit(ctx, db.InsertAuditParams{Action: "device.chain_subscription_rotate", DeviceID: row.ID})
	})
	return path, err
}

func (s *Store) ChainSubscription(ctx context.Context, token string) (model.ChainBundle, error) {
	if len(token) != 43 {
		return model.ChainBundle{}, ErrNotFound
	}
	row, err := s.queries.GetActiveDeviceByChainHash(ctx, secure.SubscriptionHash(token))
	if err != nil {
		return model.ChainBundle{}, classify(err)
	}
	out, err := s.chainBundle(ctx, s.queries, row)
	if err != nil {
		return model.ChainBundle{}, err
	}
	out.SubscriptionURL = ChainSubscriptionPathPrefix + token
	return out, nil
}

func (s *Store) chainBundle(ctx context.Context, q *db.Queries, row db.Device) (model.ChainBundle, error) {
	node, err := q.GetNode(ctx, row.NodeID)
	if err != nil {
		return model.ChainBundle{}, err
	}
	if node.Kind == "local" {
		if err := s.checkConfigHash(node.ConfigHash); err != nil {
			return model.ChainBundle{}, err
		}
	}
	if !node.EntryProxyID.Valid || node.Revision != node.AppliedRevision {
		return model.ChainBundle{}, ErrChainUnavailable
	}
	proxy, err := q.GetEntryProxy(ctx, node.EntryProxyID)
	if err != nil {
		return model.ChainBundle{}, err
	}
	if !proxy.Enabled {
		return model.ChainBundle{}, ErrChainUnavailable
	}
	credential, err := s.cipher.Decrypt(row.CredentialCiphertext, id(row.ID))
	if err != nil {
		return model.ChainBundle{}, err
	}
	public, err := nodeModel(node)
	if err != nil {
		return model.ChainBundle{}, err
	}
	entry := model.ChainEntryProxy{Type: "socks5", Host: proxy.Host, Port: int(proxy.Port), SupportsUDP: proxy.SupportsUdp}
	if proxy.AuthType == "password" {
		entry.Username, err = s.cipher.Decrypt(proxy.UsernameCiphertext, id(proxy.ID)+":username")
		if err != nil {
			return model.ChainBundle{}, err
		}
		entry.Password, err = s.cipher.Decrypt(proxy.PasswordCiphertext, id(proxy.ID)+":password")
		if err != nil {
			return model.ChainBundle{}, err
		}
	}
	profiles := []model.ChainProfile{{ID: id(row.ID) + ":tcp", Name: row.Name + " · TCP", Transport: "tcp", Host: public.Host,
		Port: public.Port, UUID: credential, Security: "reality", SNI: public.ServerName,
		PublicKey: public.PublicKey, ShortID: public.ShortID, Fingerprint: "chrome", Flow: "xtls-rprx-vision", SpiderX: "/"}}
	if public.XHTTP != nil {
		profiles = append(profiles, model.ChainProfile{ID: id(row.ID) + ":xhttp", Name: row.Name + " · XHTTP", Transport: "xhttp", Host: public.Host,
			Port: public.XHTTP.Port, UUID: credential, Security: "reality", SNI: public.ServerName,
			PublicKey: public.XHTTP.PublicKey, ShortID: public.XHTTP.ShortID, Fingerprint: "chrome", Path: public.XHTTP.Path, Mode: public.XHTTP.Mode})
	}
	// H3 не выдаётся через цепочку до проверки UDP ASSOCIATE и поддержки клиента.
	return model.ChainBundle{Version: 1, DeviceID: id(row.ID), NodeID: public.ID, EntryProxy: entry, Profiles: profiles}, nil
}
