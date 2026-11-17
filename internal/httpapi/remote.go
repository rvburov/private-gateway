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

func (s *Server) assignEntryProxy(w http.ResponseWriter, r *http.Request) {
	var in struct {
		EntryProxyID json.RawMessage `json:"entry_proxy_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.EntryProxyID == nil {
		fail(w, 400, "entry_proxy_id is required")
		return
	}
	var proxyID *string
	if err := json.Unmarshal(in.EntryProxyID, &proxyID); err != nil {
		fail(w, 400, "invalid entry_proxy_id")
		return
	}
	node, err := s.store.AssignEntryProxy(r.Context(), chi.URLParam(r, "id"), proxyID)
	if err != nil {
		s.error(w, err)
		return
	}
	respond(w, 200, map[string]any{"node": node})
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

func (s *Server) entryProxies(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.EntryProxies(r.Context())
	if err != nil {
		s.error(w, err)
		return
	}
	respond(w, 200, map[string]any{"entry_proxies": rows})
}

func (s *Server) createEntryProxy(w http.ResponseWriter, r *http.Request) {
	var in storage.EntryProxyInput
	if !decode(w, r, &in) {
		return
	}
	out, err := s.store.CreateEntryProxy(r.Context(), in)
	if err != nil {
		s.error(w, err)
		return
	}
	respond(w, 201, out)
}

func (s *Server) updateEntryProxy(w http.ResponseWriter, r *http.Request) {
	var in storage.EntryProxyInput
	if !decode(w, r, &in) {
		return
	}
	out, err := s.store.UpdateEntryProxy(r.Context(), chi.URLParam(r, "id"), in)
	if err != nil {
		s.error(w, err)
		return
	}
	respond(w, 200, out)
}

func (s *Server) deleteEntryProxy(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteEntryProxy(r.Context(), chi.URLParam(r, "id")); err != nil {
		s.error(w, err)
		return
	}
	respond(w, 200, map[string]string{"status": "deleted"})
}

func (s *Server) exportChain(w http.ResponseWriter, r *http.Request) {
	if s.absoluteSubscription(storage.ChainSubscriptionPathPrefix+strings.Repeat("A", 43)) == "" {
		fail(w, 503, "canonical panel URL is unavailable")
		return
	}
	bundle, err := s.store.ChainBundle(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.error(w, err)
		return
	}
	bundle.SubscriptionURL = s.absoluteSubscription(bundle.SubscriptionURL)
	if bundle.SubscriptionURL == "" {
		fail(w, 503, "canonical panel URL is unavailable")
		return
	}
	respond(w, 200, bundle)
}

func (s *Server) rotateChainSubscription(w http.ResponseWriter, r *http.Request) {
	if s.absoluteSubscription(storage.ChainSubscriptionPathPrefix+strings.Repeat("A", 43)) == "" {
		fail(w, 503, "canonical panel URL is unavailable")
		return
	}
	path, err := s.store.RotateChainSubscription(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.error(w, err)
		return
	}
	url := s.absoluteSubscription(path)
	if url == "" {
		fail(w, 503, "canonical panel URL is unavailable")
		return
	}
	respond(w, 200, map[string]string{"subscription_path": path, "subscription_url": url})
}

func (s *Server) chainSubscription(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	token := chi.URLParam(r, "token")
	if !subscriptionToken.MatchString(token) {
		fail(w, 404, "not found")
		return
	}
	bundle, err := s.store.ChainSubscription(r.Context(), token)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) || errors.Is(err, storage.ErrRevoked) || errors.Is(err, storage.ErrChainUnavailable) {
			fail(w, 404, "not found")
			return
		}
		fail(w, 503, "subscription unavailable")
		return
	}
	bundle.SubscriptionURL = s.absoluteSubscription(bundle.SubscriptionURL)
	if bundle.SubscriptionURL == "" {
		fail(w, 503, "canonical panel URL is unavailable")
		return
	}
	respond(w, 200, bundle)
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
