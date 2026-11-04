package httpapi

import (
	"net/http"
	"net/url"
)

// validProfilePortQuery принимает прежние параметры ссылки, но больше не выбирает
// альтернативный вход. Профиль всегда использует текущий публичный порт своего узла.
func validProfilePortQuery(r *http.Request) bool {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return false
	}
	values, exists := query["port"]
	return !exists || (len(values) == 1 && (values[0] == "primary" || values[0] == "8443"))
}

// profileExit сохраняет прежний профиль по умолчанию и отклоняет ошибочный выбор,
// чтобы запрос прокси никогда не возвращал незаметно профиль с выходом через сервер.
func profileExit(r *http.Request) (string, bool) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return "", false
	}
	values := query["exit"]
	if len(values) == 0 {
		return "server", true
	}
	if len(values) != 1 || (values[0] != "server" && values[0] != "proxy") {
		return "", false
	}
	return values[0], true
}
