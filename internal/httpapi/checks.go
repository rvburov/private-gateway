package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rvburov/private-gateway/internal/storage"
	"github.com/rvburov/private-gateway/internal/xray"
)

// ServerChecks configures the server side of the panel's "Проверка" section. A check without its
// address is skipped, as Caddy is in a local stack.
type ServerChecks struct {
	XrayBinary        string
	XrayConfigPath    string // the Xray worker writes xray.status.json beside it
	XrayServer        string // the Xray inbound as the API reaches it, xray:443 in Docker
	RealityTarget     string
	RealityServerName string
	PanelAddress      string // Caddy as the API reaches it, caddy:443 in Docker
	PanelDomain       string
	TrafficURL        string         // answers with ip=... lines, like Cloudflare's /cdn-cgi/trace
	RootCAs           *x509.CertPool // nil means the system roots
}

// checkServices — внешние сервисы, к которым обращается браузерная часть «Проверки»: ipleak.net
// сообщает адреса и сети по IPv4 и IPv6, а через уникальные поддомены — DNS-серверы браузера.
var checkServices = []string{"https://ipleak.net", "https://*.ipleak.net"}

type checkResult struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"` // ok, warn, fail or skip
	Detail string `json:"detail"`
	// The address sites see behind the gateway, from the traffic check.
	IP string `json:"ip,omitempty"`
}

func (s *Server) serverChecks(w http.ResponseWriter, r *http.Request) {
	// Each run starts an Xray client; a second tab waits for the first one's result instead.
	if !s.checksMu.TryLock() {
		fail(w, 409, "a server check is already running")
		return
	}
	defer s.checksMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	checks := []func(context.Context) checkResult{
		s.checkDatabase, s.checkConfiguration, s.checkXrayProcess,
		s.checkRealityTarget, s.checkPanelCertificate, s.checkTraffic,
	}
	results := make([]checkResult, len(checks))
	var group sync.WaitGroup
	for index, check := range checks {
		group.Add(1)
		go func() {
			defer group.Done()
			results[index] = check(ctx)
		}()
	}
	group.Wait()
	respond(w, 200, map[string]any{"checks": results})
}

func (s *Server) checkDatabase(ctx context.Context) checkResult {
	result := checkResult{ID: "database", Title: "База данных"}
	if err := s.store.Ping(ctx); err != nil {
		result.Status, result.Detail = "fail", "PostgreSQL не отвечает."
	} else {
		result.Status, result.Detail = "ok", "PostgreSQL отвечает."
	}
	return result
}

func (s *Server) checkConfiguration(ctx context.Context) checkResult {
	result := checkResult{ID: "configuration", Title: "Конфигурация Xray"}
	if !s.options.XrayConfigured {
		result.Status, result.Detail = "skip", "В этом запуске API не применяет конфигурацию Xray."
		return result
	}
	status, err := s.store.Status(ctx)
	switch {
	case err != nil:
		result.Status, result.Detail = "fail", "Не удалось прочитать версии конфигурации."
	case status.AppliedRevision == status.DesiredRevision:
		result.Status, result.Detail = "ok", fmt.Sprintf("Применена последняя версия, %d.", status.AppliedRevision)
	default:
		result.Status = "warn"
		result.Detail = fmt.Sprintf("Применена версия %d, а последняя — %d: новые устройства и отзывы ещё не действуют. Нажмите «Применить конфигурацию» на «Обзоре».",
			status.AppliedRevision, status.DesiredRevision)
	}
	return result
}

func (s *Server) checkXrayProcess(context.Context) checkResult {
	result := checkResult{ID: "xray", Title: "Процесс Xray"}
	path := s.options.Checks.XrayConfigPath
	if path == "" {
		result.Status, result.Detail = "skip", "Путь к конфигурации Xray не задан."
		return result
	}
	statusData, err := os.ReadFile(filepath.Join(filepath.Dir(path), "xray.status.json"))
	if err != nil {
		result.Status, result.Detail = "skip", "Состояние процесса Xray в этом запуске недоступно."
		return result
	}
	var state struct {
		MtimeNS int64  `json:"mtime_ns"`
		SHA256  string `json:"sha256"`
		Running bool   `json:"running"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(statusData, &state) != nil {
		result.Status, result.Detail = "fail", "Файл состояния Xray повреждён."
		return result
	}
	config, configErr := os.ReadFile(path)
	info, statErr := os.Stat(path)
	switch {
	case errors.Is(configErr, os.ErrNotExist):
		result.Status, result.Detail = "warn", "Конфигурация Xray ещё не применялась: добавьте устройство и примените её."
	case configErr != nil || statErr != nil:
		result.Status, result.Detail = "fail", "Не удалось прочитать конфигурацию Xray."
	case !state.Running:
		result.Status, result.Detail = "fail", "Xray не запущен."
		if state.Error != "" {
			result.Detail = "Xray не запущен (" + state.Error + ")."
		}
	default:
		sum := sha256.Sum256(config)
		if state.SHA256 == hex.EncodeToString(sum[:]) && state.MtimeNS == info.ModTime().UnixNano() {
			result.Status, result.Detail = "ok", "Xray работает с применённой конфигурацией."
		} else {
			result.Status, result.Detail = "warn", "Xray ещё не перечитал последнюю конфигурацию — повторите проверку через несколько секунд."
		}
	}
	return result
}

func (s *Server) checkRealityTarget(ctx context.Context) checkResult {
	checks := s.options.Checks
	result := checkResult{ID: "reality", Title: "Маскировка REALITY"}
	if checks.RealityTarget == "" {
		result.Status, result.Detail = "skip", "Сайт маскировки не задан."
		return result
	}
	state, err := handshake(ctx, checks.RealityTarget, checks.RealityServerName, checks.RootCAs, false)
	switch {
	case err != nil:
		result.Status = "fail"
		result.Detail = fmt.Sprintf("%s недоступен (%s): без него REALITY не может выдавать подключения за обращения к этому сайту.", checks.RealityTarget, reason(err))
	case state.Version != tls.VersionTLS13:
		result.Status, result.Detail = "fail", fmt.Sprintf("%s отвечает без TLS 1.3, а REALITY нужен TLS 1.3.", checks.RealityTarget)
	default:
		result.Status = "ok"
		result.Detail = fmt.Sprintf("%s отвечает по TLS 1.3: для наблюдателя подключения к серверу выглядят как обращения к %s.", checks.RealityTarget, checks.RealityServerName)
	}
	return result
}

func (s *Server) checkPanelCertificate(ctx context.Context) checkResult {
	checks := s.options.Checks
	result := checkResult{ID: "certificate", Title: "Сертификат панели"}
	if checks.PanelAddress == "" || checks.PanelDomain == "" {
		result.Status, result.Detail = "skip", "В этом запуске панель открывается без Caddy и HTTPS."
		return result
	}
	// The chain is read without verification and verified here, so an invalid certificate is
	// described instead of failing the connection.
	state, err := handshake(ctx, checks.PanelAddress, checks.PanelDomain, checks.RootCAs, true)
	if err != nil {
		result.Status, result.Detail = "fail", "Caddy не ответил по TLS ("+reason(err)+")."
		return result
	}
	leaf := state.PeerCertificates[0]
	intermediates := x509.NewCertPool()
	for _, certificate := range state.PeerCertificates[1:] {
		intermediates.AddCert(certificate)
	}
	_, verifyErr := leaf.Verify(x509.VerifyOptions{DNSName: checks.PanelDomain, Roots: checks.RootCAs, Intermediates: intermediates})
	days := int(time.Until(leaf.NotAfter).Hours() / 24)
	until := leaf.NotAfter.UTC().Format("02.01.2006")
	switch {
	case verifyErr != nil:
		result.Status, result.Detail = "fail", fmt.Sprintf("Браузеры не примут сертификат %s (действует до %s).", checks.PanelDomain, until)
	case days < 14:
		result.Status, result.Detail = "warn", fmt.Sprintf("Сертификат истекает %s, через %d дн.: Caddy должен был его уже продлить — посмотрите его журнал.", until, days)
	default:
		result.Status, result.Detail = "ok", fmt.Sprintf("Действует до %s, ещё %d дн.; Caddy продлевает его сам.", until, days)
	}
	return result
}

func (s *Server) checkTraffic(ctx context.Context) checkResult {
	checks := s.options.Checks
	result := checkResult{ID: "traffic", Title: "VLESS-трафик через сервер"}
	if checks.XrayServer == "" || checks.XrayBinary == "" || checks.TrafficURL == "" {
		result.Status, result.Detail = "skip", "Адрес Xray для проверки не задан."
		return result
	}
	node, credential, err := s.store.ProbeCredential(ctx)
	if errors.Is(err, storage.ErrNotFound) {
		result.Status, result.Detail = "skip", "Нет активных устройств: добавьте устройство и примените конфигурацию."
		return result
	}
	if err != nil {
		result.Status, result.Detail = "fail", "Не удалось получить профиль устройства для проверки."
		return result
	}
	body, err := xray.TrafficProbe{
		Binary: checks.XrayBinary, Server: checks.XrayServer, Node: node, Credential: credential,
		URL: checks.TrafficURL, RootCAs: checks.RootCAs,
	}.Run(ctx)
	var status xray.ProbeStatusError
	switch {
	case errors.Is(err, xray.ErrProbeStart):
		result.Status, result.Detail = "fail", "Не удалось запустить клиент Xray для проверки."
		return result
	case errors.As(err, &status):
		result.Status, result.Detail = "warn", fmt.Sprintf("Трафик проходит через сервер, но сайт проверки ответил кодом %d.", status.Code)
		return result
	case err != nil:
		result.Status = "fail"
		result.Detail = "Сервер не пропустил трафик устройства (" + reason(err) + "). Посмотрите «Процесс Xray» выше; если устройство только что добавлено, примените конфигурацию."
		return result
	}
	fields := map[string]string{}
	for _, line := range strings.Split(body, "\n") {
		if key, value, found := strings.Cut(strings.TrimSpace(line), "="); found {
			fields[key] = value
		}
	}
	result.IP = fields["ip"]
	seen := "сайты видят IP " + result.IP
	if country := fields["loc"]; country != "" {
		seen += " (" + country + ")"
	}
	switch matched := serverAddress(ctx, node.Host, result.IP); {
	case result.IP == "":
		result.Status, result.Detail = "ok", "Настоящий VLESS-трафик проходит через сервер."
	case matched:
		result.Status, result.Detail = "ok", "Настоящий VLESS-трафик проходит через сервер; "+seen+" — адрес сервера."
	default:
		result.Status = "warn"
		result.Detail = fmt.Sprintf("Трафик проходит через сервер, но %s, а профили ведут на %s: проверьте адрес в настройках сервера.", seen, node.Host)
	}
	return result
}

// serverAddress reports whether ip is the address of host, which profiles name as an IP address
// or a domain. An unresolvable domain is not held against the check.
func serverAddress(ctx context.Context, host, ip string) bool {
	seen := net.ParseIP(ip)
	if seen == nil {
		return false
	}
	if literal := net.ParseIP(host); literal != nil {
		return literal.Equal(seen)
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return true
	}
	for _, address := range addresses {
		if address.IP.Equal(seen) {
			return true
		}
	}
	return false
}

func handshake(ctx context.Context, address, serverName string, roots *x509.CertPool, skipVerify bool) (tls.ConnectionState, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	dialer := &tls.Dialer{Config: &tls.Config{
		ServerName: serverName, RootCAs: roots, InsecureSkipVerify: skipVerify, MinVersion: tls.VersionTLS12,
	}}
	connection, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return tls.ConnectionState{}, err
	}
	defer connection.Close()
	return connection.(*tls.Conn).ConnectionState(), nil
}

// reason shortens an error for the panel. These errors name addresses and causes, never secrets.
func reason(err error) string {
	var netError net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netError) && netError.Timeout() {
		return "истекло время ожидания"
	}
	return err.Error()
}
