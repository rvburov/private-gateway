package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rvburov/private-gateway/internal/model"
	"github.com/rvburov/private-gateway/internal/storage"
)

// Адрес подписки строится из доверенного домена панели, а не Host запроса.
func (s *Server) absoluteSubscription(path string) string {
	origin, err := url.Parse(s.options.PanelOrigin)
	if err != nil || origin.Scheme != "https" || origin.Hostname() == "" ||
		origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || origin.Path != "" ||
		!strings.HasPrefix(path, "/sub/") {
		return ""
	}
	return origin.String() + path
}

type nodeRequest struct {
	Name       string          `json:"name"`
	Host       string          `json:"host"`
	Port       int             `json:"port"`
	ServerName string          `json:"server_name"`
	PublicKey  string          `json:"public_key"`
	ShortID    string          `json:"short_id"`
	XHTTP      json.RawMessage `json:"xhttp"`
	H3         json.RawMessage `json:"h3"`
}

func (in nodeRequest) node(existing model.Node) (model.Node, error) {
	existing.Name, existing.Host, existing.Port = in.Name, in.Host, in.Port
	existing.ServerName, existing.PublicKey, existing.ShortID = in.ServerName, in.PublicKey, in.ShortID
	if in.XHTTP != nil {
		if err := json.Unmarshal(in.XHTTP, &existing.XHTTP); err != nil {
			return model.Node{}, storage.ErrInvalid
		}
	}
	if in.H3 != nil {
		if err := json.Unmarshal(in.H3, &existing.H3); err != nil {
			return model.Node{}, storage.ErrInvalid
		}
	}
	return existing, nil
}

func (s *Server) createNode(w http.ResponseWriter, r *http.Request) {
	var in nodeRequest
	if !decode(w, r, &in) {
		return
	}
	node, err := in.node(model.Node{})
	if err != nil {
		s.error(w, err)
		return
	}
	out, token, expires, err := s.store.CreateRemoteNode(r.Context(), node)
	if err != nil {
		s.error(w, err)
		return
	}
	respond(w, http.StatusCreated, map[string]any{"node": out, "join_token": token, "expires_at": expires})
}

func (s *Server) updateNode(w http.ResponseWriter, r *http.Request) {
	previous, err := s.store.Node(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.error(w, err)
		return
	}
	if previous.Kind != "remote" {
		fail(w, 400, "local node metadata is managed by server configuration")
		return
	}
	var in nodeRequest
	if !decode(w, r, &in) {
		return
	}
	node, err := in.node(previous)
	if err != nil {
		s.error(w, err)
		return
	}
	out, err := s.store.UpdateRemoteNode(r.Context(), node)
	if err != nil {
		s.error(w, err)
		return
	}
	respond(w, 200, map[string]any{"node": out})
}

func (s *Server) deleteNode(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteRemoteNode(r.Context(), chi.URLParam(r, "id")); err != nil {
		s.error(w, err)
		return
	}
	respond(w, 200, map[string]string{"status": "deleted"})
}

func (s *Server) rotateJoin(w http.ResponseWriter, r *http.Request) {
	token, expires, err := s.store.RotateAgentJoin(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.error(w, err)
		return
	}
	respond(w, 200, map[string]any{"join_token": token, "expires_at": expires})
}

func (s *Server) applyNode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == storage.LocalNodeID {
		s.apply(w, r)
		return
	}
	node, err := s.store.RequestRemoteApply(r.Context(), id)
	if err != nil {
		s.error(w, err)
		return
	}
	respond(w, 202, map[string]any{"node": node})
}

func bearerToken(r *http.Request) string {
	parts := strings.SplitN(r.Header.Get("Authorization"), " ", 2)
	if len(parts) != 2 || parts[0] != "Bearer" {
		return ""
	}
	return parts[1]
}

func (s *Server) agentEnroll(w http.ResponseWriter, r *http.Request) {
	var in struct {
		NodeID    string `json:"node_id"`
		JoinToken string `json:"join_token"`
	}
	if !decode(w, r, &in) {
		return
	}
	token, err := s.store.EnrollAgent(r.Context(), in.NodeID, in.JoinToken)
	if err != nil {
		fail(w, 401, "unauthorized")
		return
	}
	respond(w, 200, map[string]string{"agent_token": token})
}

func (s *Server) agentDesired(w http.ResponseWriter, r *http.Request) {
	desired, err := s.store.AgentDesired(r.Context(), chi.URLParam(r, "id"), bearerToken(r))
	if errors.Is(err, storage.ErrUnauthorized) {
		fail(w, 401, "unauthorized")
		return
	}
	if err != nil {
		fail(w, 503, "desired configuration unavailable")
		return
	}
	if desired == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	respond(w, 200, desired)
}

func (s *Server) agentAck(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Revision int64  `json:"revision"`
		Error    string `json:"error"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Revision <= 0 {
		fail(w, 400, "invalid revision")
		return
	}
	err := s.store.AgentAck(r.Context(), chi.URLParam(r, "id"), bearerToken(r), in.Revision, in.Error)
	if errors.Is(err, storage.ErrUnauthorized) {
		fail(w, 401, "unauthorized")
		return
	}
	if errors.Is(err, storage.ErrConflict) {
		fail(w, 409, "stale revision")
		return
	}
	if err != nil {
		fail(w, 503, "acknowledgment unavailable")
		return
	}
	respond(w, 200, map[string]string{"status": "acknowledged"})
}
