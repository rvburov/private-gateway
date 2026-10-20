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
