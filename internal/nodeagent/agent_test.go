package nodeagent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/rvburov/private-gateway/internal/model"
	"github.com/rvburov/private-gateway/internal/storage"
	"github.com/rvburov/private-gateway/internal/xray"
)

type fakeApplier struct {
	count atomic.Int32
	fail  bool
}

func (a *fakeApplier) Apply(_ context.Context, clients []xray.Client) error {
	if len(clients) != 1 {
		panic("unexpected client count")
	}
	a.count.Add(1)
	if a.fail {
		return os.ErrPermission
	}
	return nil
}

func TestEnrollPollAndAckAfterApply(t *testing.T) {
	const nodeID = "00000000-0000-4000-8000-000000000123"
	const join = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	const token = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	expected := model.Node{ID: nodeID, Host: "198.51.100.7", Port: 8443,
		PublicKey: "public", ServerName: "example.com", ShortID: "aabb"}
	var ack atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/agent/enroll":
			var request struct {
				JoinToken string `json:"join_token"`
			}
			_ = json.NewDecoder(r.Body).Decode(&request)
			if request.JoinToken != join {
				t.Error("wrong join token")
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"agent_token": token})
		case "/api/v1/agent/nodes/" + nodeID + "/desired":
			if r.Header.Get("Authorization") != "Bearer "+token {
				t.Error("wrong agent authorization")
			}
			_ = json.NewEncoder(w).Encode(storage.AgentDesired{Revision: 2, Node: expected, Clients: []xray.Client{{ID: nodeID}}})
		case "/api/v1/agent/nodes/" + nodeID + "/ack":
			var request struct {
				Revision int64  `json:"revision"`
				Error    string `json:"error"`
			}
			_ = json.NewDecoder(r.Body).Decode(&request)
			if request.Revision != 2 || request.Error != "" {
				t.Error("unexpected ACK")
			}
			ack.Add(1)
			w.WriteHeader(200)
		default:
			t.Error("unexpected request")
		}
	}))
	defer server.Close()
	applier := &fakeApplier{}
	path := filepath.Join(t.TempDir(), "agent.token")
	agent, err := New(Options{ControlURL: server.URL, NodeID: nodeID, TokenFile: path,
		Expected: expected, Applier: applier, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.Enroll(context.Background(), join); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("agent token not protected")
	}
	if err := agent.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if applier.count.Load() != 1 || ack.Load() != 1 {
		t.Fatal("apply or ACK missing")
	}
}

func TestRejectInsecureControlURL(t *testing.T) {
	_, err := New(Options{ControlURL: "http://example.com", NodeID: "00000000-0000-4000-8000-000000000123",
		TokenFile: filepath.Join(t.TempDir(), "token"), Applier: &fakeApplier{}})
	if err == nil {
		t.Fatal("HTTP control URL accepted")
	}
}
