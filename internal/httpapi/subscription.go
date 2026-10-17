package httpapi

import (
	"encoding/base64"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rvburov/private-gateway/internal/storage"
)

var (
	// A token is 32 bytes of unpadded base64url; nothing else is looked up.
	subscriptionToken = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	requestHost       = regexp.MustCompile(`^[A-Za-z0-9.-]+(:[0-9]{1,5})?$`)
	subscriptionPage  = template.Must(template.New("subscription").Parse(subscriptionHTML))
)

// subscription serves a device profile to whoever holds its link, without a session: the link
// itself is the secret. Neither the token nor the path is logged, and a revoked device or a
// replaced link answers exactly like a link that never existed.
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
		s.subscriptionPage(w, r, token, name)
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
		// The list of share links that v2rayN, v2rayNG, Hiddify, Streisand and Shadowrocket read.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString([]byte(profile.URI + "\n"))))
	}
}

// subscriptionFormat follows an explicit ?format= first, then the client's User-Agent, then a
// browser's Accept header. Everything else gets the base64 list of share links.
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
		// Hiddify names Clash and sing-box in its User-Agent but reads share links best.
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

// subscriptionPage helps a person who opened the link in a phone browser to import it.
func (s *Server) subscriptionPage(w http.ResponseWriter, r *http.Request, token, name string) {
	if !requestHost.MatchString(r.Host) {
		fail(w, 400, "invalid host")
		return
	}
	scheme := "https"
	if r.TLS == nil && r.Header.Get("X-Forwarded-Proto") == "" {
		scheme = "http"
	}
	link := scheme + "://" + r.Host + storage.SubscriptionPathPrefix + token
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The token is validated, the host is checked above and the device name is escaped, so the
	// app links may carry their own schemes.
	_ = subscriptionPage.Execute(w, map[string]any{
		"Name":    name,
		"Link":    link,
		"Hiddify": template.URL("hiddify://import/" + link + "#" + url.PathEscape(name)),
		"Clash": template.URL("clash://install-config?url=" + url.QueryEscape(link+"?format=mihomo") +
			"&name=" + url.QueryEscape(name)),
	})
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
.actions { display: grid; gap: 10px; margin: 20px 0; }
.button { display: block; padding: 12px 16px; border-radius: 10px; background: #1f6feb; color: #fff; text-align: center; text-decoration: none; font-weight: 600; }
.button.secondary { background: #e6ebf1; color: #17202a; }
textarea { box-sizing: border-box; width: 100%; padding: 10px; border: 1px solid #c8d1dc; border-radius: 8px; font: 14px ui-monospace, monospace; word-break: break-all; resize: none; }
.warning { margin-top: 20px; padding: 12px; border-radius: 8px; background: #fff4e5; color: #6b4300; }
@media (prefers-color-scheme: dark) {
  body { background: #0f1419; color: #e6edf3; }
  .name { color: #9aa7b4; }
  .button.secondary { background: #273340; color: #e6edf3; }
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
<div class="actions">
<a class="button" href="{{.Hiddify}}">Открыть в Hiddify</a>
<a class="button secondary" href="{{.Clash}}">Открыть в Clash Meta / Mihomo</a>
</div>
<p>В v2rayNG, v2rayN, Streisand, Shadowrocket и других приложениях выберите добавление подписки по ссылке и вставьте:</p>
<textarea readonly rows="3" aria-label="Ссылка-подписка">{{.Link}}</textarea>
<p class="warning">Ссылка даёт доступ к VPN. Передавайте её только тому, кто будет подключаться, и не публикуйте. Если она попала не туда, владелец выдаст новую — прежняя сразу перестанет работать.</p>
</main>
</body>
</html>
`
