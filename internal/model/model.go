package model

import "time"

type Node struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	ServerName string `json:"server_name"`
	PublicKey  string `json:"public_key"`
	ShortID    string `json:"short_id"`
	ConfigHash string `json:"-"`
}

type Device struct {
	ID        string     `json:"id"`
	NodeID    string     `json:"node_id"`
	Name      string     `json:"name"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at"`
}

type Profile struct {
	URI         string `json:"uri"`
	SingBoxJSON string `json:"sing_box_json"`
	MihomoYAML  string `json:"mihomo_yaml"`
}

type AuditEvent struct {
	ID        int64     `json:"id"`
	Action    string    `json:"action"`
	DeviceID  *string   `json:"device_id"`
	CreatedAt time.Time `json:"created_at"`
}
