package model

import "time"

type Node struct {
	ID                string     `json:"id"`
	Name              string     `json:"name"`
	Host              string     `json:"host"`
	Port              int        `json:"port"`
	ServerName        string     `json:"server_name"`
	PublicKey         string     `json:"public_key"`
	ShortID           string     `json:"short_id"`
	XHTTP             *XHTTPNode `json:"xhttp,omitempty"`
	ProxyAvailable    bool       `json:"proxy_available,omitempty"`
	ExpectedServerIP  string     `json:"expected_server_ip,omitempty"`
	ExpectedProxyIP   string     `json:"expected_proxy_ip,omitempty"`
	Kind              string     `json:"kind,omitempty"`
	EntryProxyID      *string    `json:"entry_proxy_id,omitempty"`
	DesiredRevision   int64      `json:"desired_revision,omitempty"`
	AppliedRevision   int64      `json:"applied_revision,omitempty"`
	RequestedRevision int64      `json:"requested_revision,omitempty"`
	LastSeenAt        *time.Time `json:"last_seen_at,omitempty"`
	AgentError        string     `json:"agent_error,omitempty"`
	H3                *H3Node    `json:"h3,omitempty"`
	ConfigHash        string     `json:"-"`
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
	ExpectedExitIP string `json:"expected_exit_ip,omitempty"`
	Port           int    `json:"port"`
	URI            string `json:"uri"`
	SingBoxJSON    string `json:"sing_box_json"`
	MihomoYAML     string `json:"mihomo_yaml"`
	XHTTPURI       string `json:"xhttp_uri,omitempty"`
	XHTTPPort      int    `json:"xhttp_port,omitempty"`
	H3URI          string `json:"h3_uri,omitempty"`
	H3Port         int    `json:"h3_port,omitempty"`
	// Путь ссылки-подписки устройства на домене панели: /sub/ и её токен.
	SubscriptionPath string `json:"subscription_path,omitempty"`
	SubscriptionURL  string `json:"subscription_url,omitempty"`
}

// EntryProxy содержит только открытые параметры входного SOCKS5.
// Учётные данные сохраняются шифрованными и появляются лишь в ChainBundle.
type EntryProxy struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	AuthType    string `json:"auth_type"`
	Enabled     bool   `json:"enabled"`
	SupportsUDP bool   `json:"supports_udp"`
}

type ChainEntryProxy struct {
	Type        string `json:"type"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username,omitempty"`
	Password    string `json:"password,omitempty"`
	SupportsUDP bool   `json:"supports_udp"`
}

type ChainProfile struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Transport   string   `json:"transport"`
	Host        string   `json:"host"`
	Port        int      `json:"port"`
	UUID        string   `json:"uuid"`
	Security    string   `json:"security"`
	SNI         string   `json:"sni"`
	PublicKey   string   `json:"public_key,omitempty"`
	ShortID     string   `json:"short_id,omitempty"`
	Fingerprint string   `json:"fingerprint,omitempty"`
	Flow        string   `json:"flow,omitempty"`
	SpiderX     string   `json:"spider_x,omitempty"`
	Path        string   `json:"path,omitempty"`
	Mode        string   `json:"mode,omitempty"`
	ALPN        []string `json:"alpn,omitempty"`
}

// ChainBundle — полный секретный профиль устройства. Он не годится для общей
// VLESS URI: внешний SOCKS5 должен быть настроен отдельным outbound клиента.
type ChainBundle struct {
	Version         int             `json:"version"`
	DeviceID        string          `json:"device_id"`
	NodeID          string          `json:"node_id"`
	EntryProxy      ChainEntryProxy `json:"entry_proxy"`
	Profiles        []ChainProfile  `json:"profiles"`
	SubscriptionURL string          `json:"subscription_url,omitempty"`
}

type AuditEvent struct {
	ID        int64     `json:"id"`
	Action    string    `json:"action"`
	DeviceID  *string   `json:"device_id"`
	CreatedAt time.Time `json:"created_at"`
}
