package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"strconv"

	"github.com/rvburov/private-gateway/internal/adminauth"
	"github.com/rvburov/private-gateway/internal/model"
	"github.com/rvburov/private-gateway/internal/secure"
	"github.com/rvburov/private-gateway/internal/storage"
	"github.com/rvburov/private-gateway/internal/xray"
)

type Config struct {
	DSN                string
	Token              string
	Admin              adminauth.Credentials
	Listen             string
	WebDir             string
	Cipher             *secure.Cipher
	Node               model.Node
	Xray               xray.Options
	BrowserWorkerURL   string
	BrowserWorkerToken string
	BrowserPublicURL   string
}

func Load() (Config, error) {
	c := Config{DSN: os.Getenv("GATEWAY_DATABASE_URL"), Token: os.Getenv("GATEWAY_API_TOKEN"), Listen: value("GATEWAY_LISTEN", "127.0.0.1:8080"), WebDir: value("GATEWAY_WEB_DIR", "web/dist")}
	c.Admin = adminauth.Credentials{Username: os.Getenv("GATEWAY_ADMIN_USERNAME"), PasswordHash: os.Getenv("GATEWAY_ADMIN_PASSWORD_HASH"), TOTPSecret: os.Getenv("GATEWAY_ADMIN_TOTP_SECRET")}
	if err := c.Admin.Validate(); err != nil {
		return c, err
	}
	if c.DSN == "" {
		return c, errors.New("GATEWAY_DATABASE_URL is required")
	}
	if len(c.Token) < 32 {
		return c, errors.New("GATEWAY_API_TOKEN must contain at least 32 characters")
	}
	host, port, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return c, errors.New("GATEWAY_LISTEN must be a loopback IP:port")
	}
	ip := net.ParseIP(host)
	p, e := strconv.Atoi(port)
	containerListen := os.Getenv("GATEWAY_ALLOW_CONTAINER_LISTEN") == "1" && host == "0.0.0.0"
	if ip == nil || (!ip.IsLoopback() && !containerListen) || e != nil || p < 1 || p > 65535 {
		return c, errors.New("GATEWAY_LISTEN must be loopback, or 0.0.0.0 with GATEWAY_ALLOW_CONTAINER_LISTEN=1 behind a private Docker network")
	}
	c.Cipher, err = secure.New(os.Getenv("GATEWAY_MASTER_KEY"))
	if err != nil {
		return c, err
	}
	vpnPort, err := integer("GATEWAY_PORT", 443)
	if err != nil {
		return c, err
	}
	privateKey := os.Getenv("GATEWAY_REALITY_PRIVATE_KEY")
	publicKey, err := xray.PublicKey(privateKey)
	if err != nil {
		return c, errors.New("GATEWAY_REALITY_PRIVATE_KEY must be an X25519 private key")
	}
	c.Node = model.Node{ID: storage.LocalNodeID, Name: "local", Host: value("GATEWAY_HOST", "vpn.example.com"), Port: vpnPort, ServerName: value("GATEWAY_SERVER_NAME", "www.cloudflare.com"), PublicKey: publicKey, ShortID: os.Getenv("GATEWAY_SHORT_ID")}
	settings := xray.Settings{PrivateKey: privateKey, Listen: value("GATEWAY_XRAY_LISTEN", "0.0.0.0"), Port: vpnPort, ServerName: c.Node.ServerName, Target: value("GATEWAY_REALITY_TARGET", "www.cloudflare.com:443"), ShortID: c.Node.ShortID}
	rendered, err := xray.Render(settings, nil)
	if err != nil {
		return c, errors.New("invalid Xray settings; verify REALITY target, port, listen address and short ID")
	}
	hash := sha256.Sum256(rendered)
	c.Node.ConfigHash = hex.EncodeToString(hash[:])
	if _, err = xray.Profile(c.Node, "00000000-0000-4000-8000-000000000000", "config-validation"); err != nil {
		return c, errors.New("invalid node public parameters")
	}
	c.Xray = xray.Options{Settings: settings, ConfigPath: value("GATEWAY_XRAY_CONFIG", ".local/xray.json"), Binary: value("GATEWAY_XRAY_BINARY", "xray")}
	if raw := os.Getenv("GATEWAY_XRAY_RESTART"); raw != "" {
		if err = json.Unmarshal([]byte(raw), &c.Xray.RestartCommand); err != nil || len(c.Xray.RestartCommand) == 0 || c.Xray.RestartCommand[0] == "" {
			return c, errors.New("GATEWAY_XRAY_RESTART must be a nonempty JSON argv array")
		}
	}
	c.BrowserWorkerURL = os.Getenv("GATEWAY_BROWSER_WORKER_URL")
	c.BrowserWorkerToken = os.Getenv("GATEWAY_BROWSER_WORKER_TOKEN")
	c.BrowserPublicURL = os.Getenv("GATEWAY_BROWSER_PUBLIC_URL")
	return c, nil
}
func value(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
func integer(name string, fallback int) (int, error) {
	if v := os.Getenv(name); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0, errors.New(name + " must be an integer")
		}
		return n, nil
	}
	return fallback, nil
}
