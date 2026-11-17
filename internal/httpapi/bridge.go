package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rvburov/private-gateway/internal/model"
)

func (s *Server) setNodeConnection(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Mode   string          `json:"mode"`
		Bridge json.RawMessage `json:"bridge"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Bridge == nil {
		fail(w, 400, "bridge is required; use null for direct connection")
		return
	}
	var bridge *model.BridgeNode
	decoder := json.NewDecoder(bytes.NewReader(in.Bridge))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&bridge); err != nil {
		fail(w, 400, "invalid bridge")
		return
	}
	node, err := s.store.SetNodeConnection(r.Context(), chi.URLParam(r, "id"), in.Mode, bridge)
	if err != nil {
		s.error(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{"node": node})
}

func (s *Server) bridgeConfig(w http.ResponseWriter, r *http.Request) {
	config, err := s.store.BridgeConfiguration(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.error(w, err)
		return
	}
	respond(w, http.StatusOK, config)
}

// Прежние пути остаются закрытыми независимо от токена и состояния устройства.
// Они не перенаправляют на обычную подписку и не выдают реквизиты прокси.
func (s *Server) removedEntryChain(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	fail(w, http.StatusGone, "client SOCKS5 chains have been removed")
}
