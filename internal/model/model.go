package model

import "time"

type Node struct {
	ID                string      `json:"id"`
	Name              string      `json:"name"`
	Host              string      `json:"host"`
	Port              int         `json:"port"`
	ServerName        string      `json:"server_name"`
	PublicKey         string      `json:"public_key"`
	ShortID           string      `json:"short_id"`
	XHTTP             *XHTTPNode  `json:"xhttp,omitempty"`
	ProxyAvailable    bool        `json:"proxy_available,omitempty"`
	ExpectedServerIP  string      `json:"expected_server_ip,omitempty"`
	ExpectedProxyIP   string      `json:"expected_proxy_ip,omitempty"`
	Kind              string      `json:"kind,omitempty"`
	ConnectionMode    string      `json:"connection_mode"`
	Bridge            *BridgeNode `json:"bridge,omitempty"`
	DesiredRevision   int64       `json:"desired_revision,omitempty"`
	AppliedRevision   int64       `json:"applied_revision,omitempty"`
	RequestedRevision int64       `json:"requested_revision,omitempty"`
	LastSeenAt        *time.Time  `json:"last_seen_at,omitempty"`
	AgentError        string      `json:"agent_error,omitempty"`
	H3                *H3Node     `json:"h3,omitempty"`
	ConfigHash        string      `json:"-"`
}

// BridgeNode задаёт публичные TCP-входы прозрачного моста. Ключи и SNI принадлежат выходному VPS.
type BridgeNode struct {
	Host      string `json:"host"`
	TCPPort   int    `json:"tcp_port"`
	XHTTPPort int    `json:"xhttp_port,omitempty"`
}

// H3Node описывает дополнительный вход XHTTP поверх HTTP/3.
type H3Node struct {
	Port       int    `json:"port"`
	ServerName string `json:"server_name"`
	Path       string `json:"path"`
	Mode       string `json:"mode"`
}

// XHTTPNode содержит параметры дополнительного входа; закрытый ключ хранится отдельно.
type XHTTPNode struct {
	Port      int    `json:"port"`
	PublicKey string `json:"public_key"`
	ShortID   string `json:"short_id"`
	Path      string `json:"path"`
	Mode      string `json:"mode"`
}

type Device struct {
	ID            string     `json:"id"`
	NodeID        string     `json:"node_id"`
	Name          string     `json:"name"`
	PreferredExit string     `json:"preferred_exit"`
	CreatedAt     time.Time  `json:"created_at"`
	RevokedAt     *time.Time `json:"revoked_at"`
}

type Profile struct {
	ConnectionMode string      `json:"connection_mode"`
	Bridge         *BridgeNode `json:"bridge,omitempty"`
	ExpectedExitIP string      `json:"expected_exit_ip,omitempty"`
	Port           int         `json:"port"`
	URI            string      `json:"uri"`
	SingBoxJSON    string      `json:"sing_box_json"`
	MihomoYAML     string      `json:"mihomo_yaml"`
	XHTTPURI       string      `json:"xhttp_uri,omitempty"`
	XHTTPPort      int         `json:"xhttp_port,omitempty"`
	H3URI          string      `json:"h3_uri,omitempty"`
	H3Port         int         `json:"h3_port,omitempty"`
	// Путь ссылки-подписки устройства на домене панели: /sub/ и её токен.
	SubscriptionPath string `json:"subscription_path,omitempty"`
	SubscriptionURL  string `json:"subscription_url,omitempty"`
}

type AuditEvent struct {
	ID        int64     `json:"id"`
	Action    string    `json:"action"`
	DeviceID  *string   `json:"device_id"`
	CreatedAt time.Time `json:"created_at"`
}
