package nodeagent

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rvburov/private-gateway/internal/model"
	"github.com/rvburov/private-gateway/internal/storage"
	"github.com/rvburov/private-gateway/internal/xray"
)

type Applier interface {
	Apply(context.Context, []xray.Client) error
}

type Options struct {
	ControlURL string
	NodeID     string
	TokenFile  string
	Expected   model.Node
	Applier    Applier
	HTTPClient *http.Client
}

type Agent struct {
	options Options
	client  *http.Client
	origin  string
}

func New(options Options) (*Agent, error) {
	parsed, err := url.Parse(options.ControlURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || options.TokenFile == "" ||
		options.Applier == nil {
		return nil, errors.New("invalid agent configuration")
	}
	if _, err := uuid.Parse(options.NodeID); err != nil {
		return nil, errors.New("invalid node ID")
	}
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{
			TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
			MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second,
		}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &Agent{options: options, client: client, origin: strings.TrimSuffix(options.ControlURL, "/")}, nil
}

func (a *Agent) request(ctx context.Context, method, path, token string, body any, dst any) (int, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, errors.New("invalid request")
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.origin+path, reader)
	if err != nil {
		return 0, errors.New("invalid control URL")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return 0, errors.New("control server unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return resp.StatusCode, nil
	}
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, fmt.Errorf("control server returned HTTP %d", resp.StatusCode)
	}
	if dst == nil {
		return resp.StatusCode, nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1024*1024)).Decode(dst); err != nil {
		return resp.StatusCode, errors.New("invalid control response")
	}
	return resp.StatusCode, nil
}

// Enroll получает одноразовый код только из stdin вызывающего процесса.
// Постоянный токен сохраняется в отдельный файл с правами 0600.
func (a *Agent) Enroll(ctx context.Context, join string) error {
	if len(join) != 43 {
		return errors.New("invalid join token")
	}
	var response struct {
		Token string `json:"agent_token"`
	}
	_, err := a.request(ctx, http.MethodPost, "/api/v1/agent/enroll", "",
		map[string]string{"node_id": a.options.NodeID, "join_token": join}, &response)
	if err != nil {
		return err
	}
	if len(response.Token) != 43 {
		return errors.New("invalid enrollment response")
	}
	return saveToken(a.options.TokenFile, response.Token)
}

func saveToken(path, token string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return errors.New("cannot create token directory")
	}
	file, err := os.CreateTemp(dir, ".agent-token-*")
	if err != nil {
		return errors.New("cannot create token file")
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(0600); err != nil {
		return errors.New("cannot protect token file")
	}
	if _, err := file.WriteString(token + "\n"); err != nil {
		return errors.New("cannot write token file")
	}
	if err := file.Sync(); err != nil {
		return errors.New("cannot sync token file")
	}
	if err := file.Close(); err != nil {
		return errors.New("cannot close token file")
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return errors.New("cannot install token file")
	}
	return nil
}

func readToken(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return "", errors.New("agent token file missing or unsafe")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("cannot read agent token")
	}
	token := strings.TrimSpace(string(data))
	if len(token) != 43 {
		return "", errors.New("invalid agent token")
	}
	return token, nil
}

func (a *Agent) compatible(node model.Node) bool {
	expected := a.options.Expected
	if node.ID != a.options.NodeID || node.Host != expected.Host || node.Port != expected.Port ||
		node.PublicKey != expected.PublicKey ||
		node.ShortID != expected.ShortID || node.ServerName != expected.ServerName {
		return false
	}
	if (node.XHTTP == nil) != (expected.XHTTP == nil) || (node.H3 == nil) != (expected.H3 == nil) {
		return false
	}
	if node.XHTTP != nil && (node.XHTTP.Port != expected.XHTTP.Port ||
		node.XHTTP.PublicKey != expected.XHTTP.PublicKey || node.XHTTP.ShortID != expected.XHTTP.ShortID ||
		node.XHTTP.Path != expected.XHTTP.Path || node.XHTTP.Mode != expected.XHTTP.Mode) {
		return false
	}
	if node.H3 != nil && (node.H3.Port != expected.H3.Port || node.H3.Path != expected.H3.Path ||
		node.H3.ServerName != expected.H3.ServerName || node.H3.Mode != expected.H3.Mode) {
		return false
	}
	return true
}

func (a *Agent) PollOnce(ctx context.Context) error {
	token, err := readToken(a.options.TokenFile)
	if err != nil {
		return err
	}
	path := "/api/v1/agent/nodes/" + a.options.NodeID
	var desired storage.AgentDesired
	status, err := a.request(ctx, http.MethodGet, path+"/desired", token, nil, &desired)
	if err != nil {
		return err
	}
	if status == http.StatusNoContent {
		return nil
	}
	code := ""
	if desired.Revision <= 0 || !a.compatible(desired.Node) {
		code = "metadata_mismatch"
	}
	if code == "" {
		applyCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		if err := a.options.Applier.Apply(applyCtx, desired.Clients); err != nil {
			code = "apply_failed"
		}
		cancel()
	}
	_, ackErr := a.request(ctx, http.MethodPost, path+"/ack", token,
		map[string]any{"revision": desired.Revision, "error": code}, nil)
	if ackErr != nil {
		return ackErr
	}
	if code != "" {
		return errors.New("node configuration could not be applied")
	}
	return nil
}

func (a *Agent) Serve(ctx context.Context, onError func(error)) error {
	if onError == nil {
		onError = func(error) {}
	}
	for {
		if err := a.PollOnce(ctx); err != nil {
			onError(err)
		}
		timer := time.NewTimer(10 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}
