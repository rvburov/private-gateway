package httpapi

import (
	"encoding/base64"
	"errors"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rvburov/private-gateway/internal/model"
	"github.com/rvburov/private-gateway/internal/storage"
)

var (
	// Токен — 32 байта в base64url без выравнивания; ничего другого не ищется.
	subscriptionToken = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	requestHost       = regexp.MustCompile(`^[A-Za-z0-9.-]+(:[0-9]{1,5})?$`)
	subscriptionPage  = template.Must(template.New("subscription").Parse(subscriptionHTML))
)

// subscription выдаёт профиль устройства любому, у кого есть его ссылка, без сеанса: секрет —
// сама ссылка. Ни токен, ни путь не журналируются, а отозванное устройство и заменённая ссылка
// отвечают в точности как ссылка, которой никогда не было.
func (s *Server) subscription(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	token := chi.URLParam(r, "token")
	if !subscriptionToken.MatchString(token) {
		fail(w, 404, "not found")
		return
	}
	format, ok := subscriptionFormat(r)
	if !ok {
		fail(w, 400, "unknown format; use base64, uri, sing-box, mihomo or html")
		return
	}
	if !validProfilePortQuery(r) {
		fail(w, 400, "unsupported profile port")
		return
	}
	profile, name, err := s.store.Subscription(r.Context(), token)
	if errors.Is(err, storage.ErrNotFound) {
		fail(w, 404, "not found")
		return
	}
	if err != nil {
		slog.Error("subscription lookup failed")
		fail(w, 500, "operation failed")
		return
	}
	w.Header().Set("Profile-Title", "base64:"+base64.StdEncoding.EncodeToString([]byte(name)))
	w.Header().Set("Profile-Update-Interval", "24")
	switch format {
	case "html":
		s.subscriptionPage(w, r, token, name, profile.Port)
	case "uri":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(profile.URI + "\n"))
	case "sing-box":
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", attachment(name, "json"))
		_, _ = w.Write([]byte(profile.SingBoxJSON))
	case "mihomo":
		w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
		w.Header().Set("Content-Disposition", attachment(name, "yaml"))
		_, _ = w.Write([]byte(profile.MihomoYAML))
	default:
		// Список ссылок, который читают v2rayN, v2rayNG, Hiddify, Streisand и Shadowrocket.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString([]byte(subscriptionURIs(profile, r)))))
	}
}

// subscriptionURIs сохраняет одну подписку устройства. Клиенты с собственными
// форматами и известные legacy-клиенты получают прежний TCP-профиль; общий список
// Xray содержит XHTTP первым, а TCP — запасным. Наличие двух записей не включает
// автоматическое переключение клиента и не подтверждает поддержку его версии.
func subscriptionURIs(profile model.Profile, r *http.Request) string {
	if profile.XHTTPURI == "" || legacySubscriptionClient(r.UserAgent()) {
		return profile.URI + "\n"
	}
	tcpURI := profile.URI
	if parsed, err := url.Parse(tcpURI); err == nil {
		parsed.Fragment += " · TCP Vision"
		tcpURI = parsed.String()
	}
	return profile.XHTTPURI + "\n" + tcpURI + "\n"
}

func legacySubscriptionClient(agent string) bool {
	agent = strings.ToLower(agent)
	for _, name := range []string{"hiddify", "sing-box", "singbox", "clash", "mihomo", "stash"} {
		if strings.Contains(agent, name) {
			return true
		}
	}
	return false
}

// subscriptionFormat смотрит сначала на явный ?format=, затем на User-Agent клиента, затем на
// заголовок Accept браузера. Все остальные получают список ссылок в base64.
func subscriptionFormat(r *http.Request) (string, bool) {
	switch format := strings.ToLower(r.URL.Query().Get("format")); format {
	case "":
	case "base64", "uri", "sing-box", "mihomo", "html":
		return format, true
	case "singbox":
		return "sing-box", true
	case "clash":
		return "mihomo", true
	default:
		return "", false
	}
	agent := strings.ToLower(r.UserAgent())
	switch {
	case strings.Contains(agent, "hiddify"):
		// Hiddify называет в User-Agent Clash и sing-box, но лучше всего читает ссылки.
		return "base64", true
	case strings.Contains(agent, "clash"), strings.Contains(agent, "mihomo"), strings.Contains(agent, "stash"):
		return "mihomo", true
	case strings.Contains(agent, "sing-box"):
		return "sing-box", true
	case strings.Contains(r.Header.Get("Accept"), "text/html"):
		return "html", true
	}
	return "base64", true
}

func attachment(name, extension string) string {
	return `attachment; filename="private-gateway.` + extension + `"; filename*=UTF-8''` +
		url.PathEscape(name+"."+extension)
}

// subscriptionPage помогает импортировать ссылку тому, кто открыл её в браузере телефона.
func (s *Server) subscriptionPage(w http.ResponseWriter, r *http.Request, token, name string, port int) {
	if !requestHost.MatchString(r.Host) {
		fail(w, 400, "invalid host")
		return
	}
	scheme := "https"
	if r.TLS == nil && r.Header.Get("X-Forwarded-Proto") == "" {
		scheme = "http"
	}
	host := r.Host
	if port != 0 {
		hostname := r.Host
		if h, _, err := net.SplitHostPort(r.Host); err == nil {
			hostname = h
		}
		host = net.JoinHostPort(hostname, strconv.Itoa(port))
	}
	link := scheme + "://" + host + (storage.SubscriptionPathPrefix + token)
	mihomoLink, _ := url.Parse(link)
	query := mihomoLink.Query()
	query.Set("format", "mihomo")
	mihomoLink.RawQuery = query.Encode()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Токен проверен, хост проверен выше, а имя устройства экранируется, поэтому ссылки импорта
	// могут нести собственные схемы приложений. Порядок — как в «Инструкциях» панели.
	type app struct {
		Name string
		URL  template.URL
	}
	apps := []app{
		{"Happ", template.URL("happ://add/" + link)},
		{"v2rayNG", template.URL("v2rayng://install-sub?url=" + url.QueryEscape(link) + "#" + url.PathEscape(name))},
		{"Hiddify", template.URL("hiddify://import/" + link + "#" + url.PathEscape(name))},
		{"Streisand", template.URL("streisand://import/" + link)},
		{"V2Box", template.URL("v2box://install-sub?url=" + url.QueryEscape(link) + "&name=" + url.QueryEscape(name))},
		{"Clash Meta / Mihomo", template.URL("clash://install-config?url=" + url.QueryEscape(mihomoLink.String()) +
			"&name=" + url.QueryEscape(name))},
	}
	_ = subscriptionPage.Execute(w, map[string]any{"Name": name, "Link": link, "Apps": apps})
}

const subscriptionHTML = `<!doctype html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex, nofollow">
<title>Подключение: {{.Name}}</title>
<style>
body { margin: 0; background: #f4f6f8; color: #17202a; font: 16px/1.5 system-ui, -apple-system, "Segoe UI", sans-serif; }
main { max-width: 34rem; margin: 0 auto; padding: 24px 16px 40px; }
h1 { font-size: 1.4rem; margin: 0 0 4px; }
.name { color: #4b5866; margin: 0 0 20px; }
.hint { color: #4b5866; margin: 16px 0 0; font-size: 0.9rem; }
.actions { display: grid; grid-template-columns: repeat(auto-fit, minmax(14rem, 1fr)); gap: 10px; margin: 10px 0 20px; }
.button { display: block; padding: 12px 16px; border-radius: 10px; background: #1f6feb; color: #fff; text-align: center; text-decoration: none; font-weight: 600; }
textarea { box-sizing: border-box; width: 100%; padding: 10px; border: 1px solid #c8d1dc; border-radius: 8px; font: 14px ui-monospace, monospace; word-break: break-all; resize: none; }
.warning { margin-top: 20px; padding: 12px; border-radius: 8px; background: #fff4e5; color: #6b4300; }
@media (prefers-color-scheme: dark) {
  body { background: #0f1419; color: #e6edf3; }
  .name, .hint { color: #9aa7b4; }
  textarea { background: #161d24; color: #e6edf3; border-color: #34414e; }
  .warning { background: #3a2a10; color: #f5d9a8; }
}
</style>
</head>
<body>
<main>
<h1>Подключение к Private Gateway</h1>
<p class="name">{{.Name}}</p>
<p>Это ссылка-подписка. Добавьте её в приложение VPN с поддержкой VLESS + REALITY: оно само получит профиль и будет его обновлять.</p>
<p class="hint">Кнопка сработает, если приложение уже установлено.</p>
<div class="actions">
{{range .Apps}}<a class="button" href="{{.URL}}">Открыть в {{.Name}}</a>
{{end}}</div>
<p>В других приложениях (v2rayN, Shadowrocket, Karing и других) выберите добавление подписки по ссылке и вставьте:</p>
<textarea readonly rows="3" aria-label="Ссылка-подписка">{{.Link}}</textarea>
<p class="warning">Ссылка даёт доступ к VPN. Передавайте её только тому, кто будет подключаться, и не публикуйте. Если она попала не туда, владелец выдаст новую — прежняя сразу перестанет работать.</p>
</main>
</body>
</html>
`
