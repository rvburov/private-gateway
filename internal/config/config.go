package config

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

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
	IP2LocationAPIKey  string
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
	vpnPort, err := integer("GATEWAY_PORT", 8443)
	if err != nil || vpnPort < 1 || vpnPort > 65535 {
		return c, errors.New("GATEWAY_PORT must be a valid public port (1-65535)")
	}
	xrayPort, err := integer("GATEWAY_XRAY_PORT", 443)
	if err != nil || xrayPort < 1 || xrayPort > 65535 {
		return c, errors.New("GATEWAY_XRAY_PORT must be a valid internal port (1-65535)")
	}
	statsPort, err := integer("GATEWAY_XRAY_STATS_PORT", 0)
	if err != nil {
		return c, err
	}
	privateKey := os.Getenv("GATEWAY_REALITY_PRIVATE_KEY")
	publicKey, err := xray.PublicKey(privateKey)
	if err != nil {
		return c, errors.New("GATEWAY_REALITY_PRIVATE_KEY must be an X25519 private key")
	}
	c.Node = model.Node{ID: storage.LocalNodeID, Name: "local", Host: value("GATEWAY_HOST", "vpn.example.com"), Port: vpnPort, ServerName: value("GATEWAY_SERVER_NAME", "www.cloudflare.com"), PublicKey: publicKey, ShortID: os.Getenv("GATEWAY_SHORT_ID")}
	if raw := os.Getenv("GATEWAY_SERVER_EXPECTED_IP"); raw != "" {
		var valid bool
		c.Node.ExpectedServerIP, valid = publicExpectedIP(raw)
		if !valid {
			return c, errors.New("GATEWAY_SERVER_EXPECTED_IP must be a public IPv4 or IPv6 address")
		}
	} else {
		// Адрес из настройки узла — ожидание, а не результат сетевого измерения.
		c.Node.ExpectedServerIP, _ = publicExpectedIP(c.Node.Host)
	}
	settings := xray.Settings{PrivateKey: privateKey, Listen: value("GATEWAY_XRAY_LISTEN", "0.0.0.0"), Port: xrayPort, StatsPort: statsPort, ServerName: c.Node.ServerName, Target: value("GATEWAY_REALITY_TARGET", "www.cloudflare.com:443"), ShortID: c.Node.ShortID}
	switch value("GATEWAY_XHTTP_ENABLED", "0") {
	case "0":
	case "1":
		xhttpPort, err := integer("GATEWAY_XHTTP_PORT", 8444)
		if err != nil || xhttpPort < 1 || xhttpPort > 65535 || xhttpPort == vpnPort ||
			xhttpPort == xrayPort || xhttpPort == statsPort || xhttpPort == p ||
			xhttpPort == 80 || xhttpPort == 443 || xhttpPort == 8443 {
			return c, errors.New("GATEWAY_XHTTP_PORT must be valid and distinct from existing service ports")
		}
		xhttpPrivate := os.Getenv("GATEWAY_XHTTP_PRIVATE_KEY")
		xhttpPublic, err := xray.PublicKey(xhttpPrivate)
		if err != nil || xhttpPublic == publicKey {
			return c, errors.New("GATEWAY_XHTTP_PRIVATE_KEY must be a separate X25519 private key")
		}
		xhttpShortID := strings.ToLower(os.Getenv("GATEWAY_XHTTP_SHORT_ID"))
		xhttpPath := os.Getenv("GATEWAY_XHTTP_PATH")
		settings.XHTTP = &xray.XHTTPSettings{Port: xhttpPort, PrivateKey: xhttpPrivate, ShortID: xhttpShortID, Path: xhttpPath}
		c.Node.XHTTP = &model.XHTTPNode{Port: xhttpPort, PublicKey: xhttpPublic, ShortID: xhttpShortID, Path: xhttpPath, Mode: "auto"}
	default:
		return c, errors.New("GATEWAY_XHTTP_ENABLED must be 0 or 1")
	}
	switch value("GATEWAY_PROXY_ENABLED", "0") {
	case "0":
	case "1":
		proxyPort, portErr := integer("GATEWAY_PROXY_PORT", 0)
		username, userErr := base64.StdEncoding.Strict().DecodeString(os.Getenv("GATEWAY_PROXY_USERNAME_B64"))
		password, passErr := base64.StdEncoding.Strict().DecodeString(os.Getenv("GATEWAY_PROXY_PASSWORD_B64"))
		auth := os.Getenv("GATEWAY_PROXY_AUTH")
		settings.Proxy = &xray.ProxySettings{
			Address: os.Getenv("GATEWAY_PROXY_HOST"), Port: proxyPort,
			Username: string(username), Password: string(password),
		}
		if portErr != nil || userErr != nil || passErr != nil || !utf8.Valid(username) || !utf8.Valid(password) ||
			(auth != "password" && auth != "none") ||
			(auth == "password" && (len(username) == 0 || len(password) == 0)) ||
			(auth == "none" && (len(username) != 0 || len(password) != 0)) {
			return c, errors.New("SOCKS5 proxy settings are incomplete or invalid")
		}
		c.Node.ProxyAvailable = true
		if raw := os.Getenv("GATEWAY_PROXY_EXPECTED_IP"); raw != "" {
			var valid bool
			c.Node.ExpectedProxyIP, valid = publicExpectedIP(raw)
			if !valid {
				return c, errors.New("GATEWAY_PROXY_EXPECTED_IP must be a public IPv4 or IPv6 address")
			}
		}
	default:
		return c, errors.New("GATEWAY_PROXY_ENABLED must be 0 or 1")
	}
	rendered, err := xray.Render(settings, nil)
	if err != nil {
		if settings.Proxy != nil {
			return c, errors.New("invalid SOCKS5 proxy settings; verify address, port and authentication length")
		}
		if settings.XHTTP != nil {
			return c, errors.New("invalid Xray settings; verify TCP and XHTTP ports, separate REALITY keys, short IDs and XHTTP path")
		}
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
	c.IP2LocationAPIKey = os.Getenv("GATEWAY_IP2LOCATION_API_KEY")
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

// Ожидаемый выход — только внешний адрес. DNS-имя или адрес входа в прокси его не доказывает.
// Диапазоны исключений совпадают с проверкой публичных адресов в API справки об IP.
func publicExpectedIP(raw string) (string, bool) {
	ip, err := netip.ParseAddr(raw)
	if err != nil || ip.Zone() != "" {
		return "", false
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() ||
		ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return "", false
	}
	for _, prefix := range expectedIPExcluded {
		if prefix.Contains(ip) {
			return "", false
		}
	}
	return ip.String(), true
}

var expectedIPExcluded = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
}
