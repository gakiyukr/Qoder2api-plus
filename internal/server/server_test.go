package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/J-York/QoderProxy/internal/config"
	"github.com/J-York/QoderProxy/internal/credential"
	"github.com/J-York/QoderProxy/internal/pool"
	"github.com/J-York/QoderProxy/internal/protocol"
	"github.com/J-York/QoderProxy/internal/qoder"
)

type fakeRefresh struct{ calls atomic.Int64 }

func (f *fakeRefresh) Refresh(_ context.Context, a credential.Account) (credential.Account, error) {
	f.calls.Add(1)
	a.AccessToken = "fresh"
	a.ExpiresAtMS = time.Now().Add(time.Hour).UnixMilli()
	return a, nil
}

type fakeTransport struct {
	name     string
	probeErr error
	chat     func(context.Context, credential.Account, qoder.ChatRequest, qoder.Model) (*qoder.UpstreamResponse, error)
	calls    atomic.Int64
}

func (f *fakeTransport) Name() string                                    { return f.name }
func (f *fakeTransport) Probe(context.Context, credential.Account) error { return f.probeErr }
func (f *fakeTransport) Chat(c context.Context, a credential.Account, r qoder.ChatRequest, m qoder.Model) (*qoder.UpstreamResponse, error) {
	f.calls.Add(1)
	return f.chat(c, a, r, m)
}

func testServer(t *testing.T, accounts []credential.Account, refresh pool.Refresher, tr qoder.ChatTransport) (*Server, *pool.Pool) {
	t.Helper()
	catalogServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"chat":[{"key":"lite","enable":true,"display_name":"Lite","max_input_tokens":180000,"is_vl":false,"is_reasoning":false,"source":"system"}]}`)
	}))
	t.Cleanup(catalogServer.Close)
	catalog := qoder.NewCatalog(catalogServer.Client(), time.Hour)
	catalog.Endpoints = func(protocol.Region) (protocol.Endpoints, bool) {
		return protocol.Endpoints{ModelList: catalogServer.URL}, true
	}
	p := pool.New(accounts, nil, refresh)
	cfg := config.Default()
	cfg.Transport = "bearer"
	cfg.TotalRequestTimeout.Duration = time.Second
	cfg.StreamIdleTimeout.Duration = time.Second
	s := New(cfg, p, catalog, tr, tr, "", log.New(io.Discard, "", 0))
	return s, p
}
func serverAccount(id string) credential.Account {
	return credential.Account{ID: id, Region: "global", TokenKind: "device", AccessToken: "valid", RefreshToken: "r", ExpiresAtMS: time.Now().Add(time.Hour).UnixMilli(), UserID: id, MachineID: "m"}
}
func chatRequest(ctx context.Context, stream bool) *http.Request {
	body := `{"model":"lite","messages":[{"role":"user","content":"hello"}],"stream":` + map[bool]string{true: "true", false: "false"}[stream] + `}`
	return httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)).WithContext(ctx)
}
func okBody() io.ReadCloser {
	return io.NopCloser(strings.NewReader("data: {\"id\":\"x\",\"model\":\"lite\",\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
}

func Test401RefreshesExactlyOnce(t *testing.T) {
	ref := &fakeRefresh{}
	tr := &fakeTransport{name: "bearer"}
	tr.chat = func(_ context.Context, a credential.Account, _ qoder.ChatRequest, _ qoder.Model) (*qoder.UpstreamResponse, error) {
		if a.AccessToken != "fresh" {
			return nil, &qoder.HTTPError{Status: 401, Message: "expired"}
		}
		return &qoder.UpstreamResponse{Status: 200, Body: okBody()}, nil
	}
	s, _ := testServer(t, []credential.Account{serverAccount("a")}, ref, tr)
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, chatRequest(context.Background(), false))
	if rr.Code != 200 {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if ref.calls.Load() != 1 || tr.calls.Load() != 2 {
		t.Fatalf("refresh=%d chat=%d", ref.calls.Load(), tr.calls.Load())
	}
}

func Test429PutsAccountInCooldownAndBoundsAttempts(t *testing.T) {
	tr := &fakeTransport{name: "bearer", chat: func(context.Context, credential.Account, qoder.ChatRequest, qoder.Model) (*qoder.UpstreamResponse, error) {
		return nil, &qoder.HTTPError{Status: 429, Message: "quota", RetryAfter: time.Minute}
	}}
	s, p := testServer(t, []credential.Account{serverAccount("a"), serverAccount("b")}, nil, tr)
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, chatRequest(context.Background(), false))
	if tr.calls.Load() != 2 {
		t.Fatalf("attempts=%d", tr.calls.Load())
	}
	if rr.Code != 503 {
		t.Fatalf("code=%d", rr.Code)
	}
	for _, st := range p.Statuses() {
		if st.State != "cooldown" {
			t.Fatalf("state=%+v", st)
		}
	}
}

func TestPartialStreamNeverFailsOver(t *testing.T) {
	tr := &fakeTransport{name: "bearer", chat: func(context.Context, credential.Account, qoder.ChatRequest, qoder.Model) (*qoder.UpstreamResponse, error) {
		body := "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\ndata: {bad}\n\n"
		return &qoder.UpstreamResponse{Status: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	}}
	s, _ := testServer(t, []credential.Account{serverAccount("a"), serverAccount("b")}, nil, tr)
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, chatRequest(context.Background(), true))
	if tr.calls.Load() != 1 {
		t.Fatalf("stream replayed: calls=%d", tr.calls.Load())
	}
	if !strings.Contains(rr.Body.String(), "partial") || !strings.Contains(rr.Body.String(), "upstream_stream_error") {
		t.Fatalf("body=%s", rr.Body.String())
	}
}

type blockingBody struct {
	once   sync.Once
	closed chan struct{}
}

func (b *blockingBody) Read([]byte) (int, error) { <-b.closed; return 0, io.EOF }
func (b *blockingBody) Close() error             { b.once.Do(func() { close(b.closed) }); return nil }
func TestClientCancellationClosesUpstream(t *testing.T) {
	body := &blockingBody{closed: make(chan struct{})}
	started := make(chan struct{})
	tr := &fakeTransport{name: "bearer", chat: func(context.Context, credential.Account, qoder.ChatRequest, qoder.Model) (*qoder.UpstreamResponse, error) {
		close(started)
		return &qoder.UpstreamResponse{Status: 200, Body: body}, nil
	}}
	s, _ := testServer(t, []credential.Account{serverAccount("a")}, nil, tr)
	ctx, cancel := context.WithCancel(context.Background())
	rr := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { s.ServeHTTP(rr, chatRequest(ctx, true)); close(done) }()
	<-started
	cancel()
	select {
	case <-body.closed:
	case <-time.After(time.Second):
		t.Fatal("upstream body not closed")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler did not exit")
	}
}

func TestAPIKeyProtection(t *testing.T) {
	tr := &fakeTransport{name: "bearer", chat: func(context.Context, credential.Account, qoder.ChatRequest, qoder.Model) (*qoder.UpstreamResponse, error) {
		return nil, errors.New("unused")
	}}
	s, _ := testServer(t, []credential.Account{serverAccount("a")}, nil, tr)
	s.APIKey = "secret"
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, chatRequest(context.Background(), false))
	if rr.Code != 401 {
		t.Fatalf("code=%d", rr.Code)
	}
	req := chatRequest(context.Background(), false)
	req.Header.Set("Authorization", "Bearer secret")
	rr = httptest.NewRecorder()
	s.ServeHTTP(rr, req)
	if rr.Code == 401 {
		t.Fatal("valid key rejected")
	}
}

func TestNonStreamPreservesUsageFinishAndTools(t *testing.T) {
	sse := "data: {\"id\":\"x\",\"model\":\"lite\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c\",\"type\":\"function\",\"function\":{\"name\":\"ping\",\"arguments\":\"\"}}]},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1,\"total_tokens\":3}}\n\ndata: [DONE]\n\n"
	tr := &fakeTransport{name: "bearer", chat: func(context.Context, credential.Account, qoder.ChatRequest, qoder.Model) (*qoder.UpstreamResponse, error) {
		return &qoder.UpstreamResponse{Status: 200, Body: io.NopCloser(strings.NewReader(sse))}, nil
	}}
	s, _ := testServer(t, []credential.Account{serverAccount("a")}, nil, tr)
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, chatRequest(context.Background(), false))
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	choices := out["choices"].([]any)
	if choices[0].(map[string]any)["finish_reason"] != "tool_calls" {
		t.Fatalf("out=%s", rr.Body.String())
	}
	if out["usage"].(map[string]any)["total_tokens"].(float64) != 3 {
		t.Fatalf("out=%s", rr.Body.String())
	}
}

func TestAutoUsesCosyForConcreteModelsWithoutFailedBearerGeneration(t *testing.T) {
	bearer := &fakeTransport{name: "bearer", chat: func(context.Context, credential.Account, qoder.ChatRequest, qoder.Model) (*qoder.UpstreamResponse, error) {
		return nil, errors.New("must not be called")
	}}
	cosy := &fakeTransport{name: "cosy", chat: bearer.chat}
	a := serverAccount("a")
	p := pool.New([]credential.Account{a}, nil, nil)
	entry, _ := p.Select("", nil)
	cfg := config.Default()
	cfg.Transport = "auto"
	s := New(cfg, p, nil, bearer, cosy, "", log.New(io.Discard, "", 0))
	got, err := s.pickTransport(context.Background(), entry, a, "qmodel_38max")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name() != "cosy" {
		t.Fatalf("transport=%s", got.Name())
	}
	if bearer.calls.Load() != 0 {
		t.Fatal("bearer inference was attempted")
	}
}
