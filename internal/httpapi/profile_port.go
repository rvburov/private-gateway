package httpapi

import (
	"net"
	"net/http"
	"strconv"
)

// profilePort разрешает основной порт из базы или явно настроенный резервный вход.
// При открытии панели/подписки на резервном порту он выбирается по умолчанию.
// Явное port=primary сохраняет основной профиль даже на резервном адресе панели.
func (s *Server) profilePort(r *http.Request) (int, bool) {
	values, exists := r.URL.Query()["port"]
	if exists {
		if len(values) != 1 {
			return 0, false
		}
		if values[0] == "primary" {
			return 0, true
		}
		if s.options.FallbackPort > 0 && values[0] == strconv.Itoa(s.options.FallbackPort) {
			return s.options.FallbackPort, true
		}
		return 0, false
	}
	_, port, err := net.SplitHostPort(r.Host)
	if err == nil && s.options.FallbackPort > 0 && port == strconv.Itoa(s.options.FallbackPort) {
		return s.options.FallbackPort, true
	}
	return 0, true
}

func (s *Server) subscriptionPath(path string, port int) string {
	if s.options.FallbackPort == 0 {
		return path
	}
	if port == 0 {
		return path + "?port=primary"
	}
	return path + "?port=" + strconv.Itoa(port)
}
