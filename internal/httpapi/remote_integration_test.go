package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvburov/private-gateway/internal/model"
	"github.com/rvburov/private-gateway/internal/storage"
)

func TestRemoteAgentLifecyclePostgreSQL(t *testing.T) {
	store, _, local, _ := integrationStore(t)
	ctx := context.Background()
	remote := model.Node{Name: "Remote", Host: "198.51.100.7", Port: 8443,
		ServerName: local.ServerName, PublicKey: local.PublicKey, ShortID: local.ShortID}
	node, join, _, err := store.CreateRemoteNode(ctx, remote)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnrollAgent(ctx, node.ID, "wrong"); !errors.Is(err, storage.ErrUnauthorized) {
		t.Fatal("invalid join accepted")
	}
	agentToken, err := store.EnrollAgent(ctx, node.ID, join)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnrollAgent(ctx, node.ID, join); !errors.Is(err, storage.ErrUnauthorized) {
		t.Fatal("join token reused")
	}
	other := remote
	other.Name = "Other remote"
	otherNode, otherJoin, _, err := store.CreateRemoteNode(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	otherToken, err := store.EnrollAgent(ctx, otherNode.ID, otherJoin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AgentDesired(ctx, otherNode.ID, agentToken); !errors.Is(err, storage.ErrUnauthorized) {
		t.Fatal("node A agent read node B desired configuration")
	}
	if err := store.AgentAck(ctx, otherNode.ID, agentToken, 1, ""); !errors.Is(err, storage.ErrUnauthorized) {
		t.Fatal("node A agent acknowledged node B revision")
	}
	if _, err := store.AgentDesired(ctx, node.ID, otherToken); !errors.Is(err, storage.ErrUnauthorized) {
		t.Fatal("node B agent read node A desired configuration")
	}
	const adminToken = "remote-agent-boundary-admin-token-32characters"
	handler := New(store, &testXray{}, &testBrowser{}, Options{Token: adminToken, PanelOrigin: "https://panel.example:8443"})
	call := func(method, path, token, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, "https://panel.example:8443"+path, strings.NewReader(body))
		request.RemoteAddr = "127.0.0.1:12345"
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	if response := call(http.MethodGet, "/api/v1/agent/nodes/"+otherNode.ID+"/desired", agentToken, ""); response.Code != http.StatusUnauthorized {
		t.Fatal("HTTP desired route accepted another node's token")
	}
	if response := call(http.MethodPost, "/api/v1/agent/nodes/"+otherNode.ID+"/ack", agentToken, `{"revision":1}`); response.Code != http.StatusUnauthorized {
		t.Fatal("HTTP ACK route accepted another node's token")
	}
	if response := call(http.MethodGet, "/api/v1/nodes", agentToken, ""); response.Code != http.StatusUnauthorized {
		t.Fatal("agent token granted administrator access")
	}
	if response := call(http.MethodGet, "/api/v1/agent/nodes/"+otherNode.ID+"/desired", otherToken, ""); response.Code != http.StatusNoContent {
		t.Fatal("own agent token could not poll unrequested node")
	}
	device, err := store.Create(ctx, "Remote phone", node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if desired, err := store.AgentDesired(ctx, node.ID, agentToken); err != nil || desired != nil {
		t.Fatal("unrequested revision sent")
	}
	if _, err := store.RequestRemoteApply(ctx, node.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AgentDesired(ctx, node.ID, "incorrect"); !errors.Is(err, storage.ErrUnauthorized) {
		t.Fatal("wrong agent token accepted")
	}
	desired, err := store.AgentDesired(ctx, node.ID, agentToken)
	if err != nil || desired == nil || len(desired.Clients) != 1 || desired.Node.ID != node.ID {
		t.Fatal("remote desired config missing")
	}
	if err := store.AgentAck(ctx, node.ID, agentToken, desired.Revision-1, ""); !errors.Is(err, storage.ErrConflict) {
		t.Fatal("stale ACK accepted")
	}
	if err := store.AgentAck(ctx, node.ID, agentToken, desired.Revision, "secret error user-secret"); !errors.Is(err, storage.ErrInvalid) {
		t.Fatal("unredacted failure accepted")
	}
	if err := store.AgentAck(ctx, node.ID, agentToken, desired.Revision, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Revoke(ctx, device.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteRevoked(ctx, device.ID); !errors.Is(err, storage.ErrPendingConfig) {
		t.Fatal("revoked device deleted before remote apply ACK")
	}
	if _, err := store.RequestRemoteApply(ctx, node.ID); err != nil {
		t.Fatal(err)
	}
	revokedDesired, err := store.AgentDesired(ctx, node.ID, agentToken)
	if err != nil || revokedDesired == nil || len(revokedDesired.Clients) != 0 || revokedDesired.Revision <= desired.Revision {
		t.Fatal("revocation was not sent as empty remote client set")
	}
	if err := store.AgentAck(ctx, node.ID, agentToken, revokedDesired.Revision, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteRevoked(ctx, device.ID); err != nil {
		t.Fatal(err)
	}
	newJoin, _, err := store.RotateAgentJoin(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AgentDesired(ctx, node.ID, agentToken); !errors.Is(err, storage.ErrUnauthorized) {
		t.Fatal("rotated join did not invalidate prior permanent agent token")
	}
	if _, err := store.EnrollAgent(ctx, node.ID, join); !errors.Is(err, storage.ErrUnauthorized) {
		t.Fatal("old join token became usable again")
	}
	newAgentToken, err := store.EnrollAgent(ctx, node.ID, newJoin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AgentDesired(ctx, node.ID, newAgentToken); err != nil {
		t.Fatal("newly enrolled agent cannot poll its own node")
	}
	if err := store.DeleteRemoteNode(ctx, node.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteRemoteNode(ctx, otherNode.ID); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteDirectSubscriptionWaitsForAppliedMetadataPostgreSQL(t *testing.T) {
	store, _, local, _ := integrationStore(t)
	ctx := context.Background()
	node, join, _, err := store.CreateRemoteNode(ctx, model.Node{
		Name: "Direct remote", Host: "198.51.100.7", Port: 8443,
		ServerName: local.ServerName, PublicKey: local.PublicKey, ShortID: local.ShortID,
	})
	if err != nil {
		t.Fatal(err)
	}
	agentToken, err := store.EnrollAgent(ctx, node.ID, join)
	if err != nil {
		t.Fatal(err)
	}
	device, err := store.Create(ctx, "Direct phone", node.ID)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.Export(ctx, device.ID)
	if err != nil {
		t.Fatal(err)
	}
	handler := New(store, &testXray{}, &testBrowser{}, Options{PanelOrigin: "https://panel.example:8443"})
	getSubscription := func() *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
			"https://panel.example:8443"+profile.SubscriptionPath+"?format=uri", nil))
		return response
	}
	if response := getSubscription(); response.Code != http.StatusNotFound {
		t.Fatalf("unapplied remote subscription returned %d", response.Code)
	}
	apply := func() {
		t.Helper()
		if _, err := store.RequestRemoteApply(ctx, node.ID); err != nil {
			t.Fatal(err)
		}
		desired, err := store.AgentDesired(ctx, node.ID, agentToken)
		if err != nil || desired == nil {
			t.Fatalf("agent did not receive requested revision: %v", err)
		}
		if err := store.AgentAck(ctx, node.ID, agentToken, desired.Revision, ""); err != nil {
			t.Fatal(err)
		}
	}
	apply()
	if response := getSubscription(); response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), "@198.51.100.7:8443") {
		t.Fatalf("applied remote subscription missing original endpoint: %d", response.Code)
	}
	node.Host = "198.51.100.8"
	node.Port = 9443
	if _, err := store.UpdateRemoteNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	if response := getSubscription(); response.Code != http.StatusNotFound {
		t.Fatalf("subscription advertised pending remote endpoint: %d", response.Code)
	}
	apply()
	if response := getSubscription(); response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), "@198.51.100.8:9443") {
		t.Fatalf("subscription did not advertise ACKed endpoint: %d", response.Code)
	}
}
