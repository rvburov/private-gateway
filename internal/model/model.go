package model

import "time"

type Node struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Host       string     `json:"host"`
	Port       int        `json:"port"`
	ServerName string     `json:"server_name"`
	PublicKey  string     `json:"public_key"`
	ShortID    string     `json:"short_id"`
	XHTTP      *XHTTPNode `json:"xhttp,omitempty"`
	ConfigHash string     `json:"-"`
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
	ID        string     `json:"id"`
	NodeID    string     `json:"node_id"`
	Name      string     `json:"name"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at"`
}

type Profile struct {
	Port        int    `json:"port"`
	URI         string `json:"uri"`
	SingBoxJSON string `json:"sing_box_json"`
	MihomoYAML  string `json:"mihomo_yaml"`
	XHTTPURI    string `json:"xhttp_uri,omitempty"`
	XHTTPPort   int    `json:"xhttp_port,omitempty"`
	// Путь ссылки-подписки устройства на домене панели: /sub/ и её токен.
	SubscriptionPath string `json:"subscription_path,omitempty"`
}

type AuditEvent struct {
	ID        int64     `json:"id"`
	Action    string    `json:"action"`
	DeviceID  *string   `json:"device_id"`
	CreatedAt time.Time `json:"created_at"`
}
