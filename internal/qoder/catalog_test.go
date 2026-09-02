package qoder

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/J-York/QoderProxy/internal/protocol"
)

func TestCatalogEnabledRawConfigAndContextPriority(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"chat":[{"key":"a","enable":true,"display_name":"A","max_input_tokens":180000,"context_config":{"small":{"token_count":200000},"large":{"token_count":400000}},"is_vl":true,"thinking_config":{"enabled":{"efforts":{"high":{}}}},"source":"system","unknown_field":{"keep":true}},{"key":"disabled","enable":false,"display_name":"No"},{"key":"b","enable":true,"display_name":"B","max_input_tokens":123456}]}`)
	}))
	defer srv.Close()
	c := NewCatalog(srv.Client(), time.Hour)
	c.Endpoints = func(protocol.Region) (protocol.Endpoints, bool) { return protocol.Endpoints{ModelList: srv.URL}, true }
	snap, err := c.Get(context.Background(), testAccount())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Models) != 2 {
		t.Fatalf("models=%d", len(snap.Models))
	}
	a, _ := c.Find("a")
	if a.ContextWindow != 400000 || !a.IsVL || !a.IsReasoning {
		t.Fatalf("a=%+v", a)
	}
	if string(a.RawConfig) == "" || !contains(string(a.RawConfig), "unknown_field") {
		t.Fatal("raw model_config not preserved")
	}
	b, _ := c.Find("b")
	if b.ContextWindow != 123456 {
		t.Fatalf("fallback context=%d", b.ContextWindow)
	}
}

func TestCatalogFailureUsesDegradedFallbackWithoutInflation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", 500) }))
	defer srv.Close()
	c := NewCatalog(srv.Client(), time.Hour)
	c.Endpoints = func(protocol.Region) (protocol.Endpoints, bool) { return protocol.Endpoints{ModelList: srv.URL}, true }
	snap, err := c.Get(context.Background(), testAccount())
	if err == nil || !snap.Degraded || len(snap.Models) != 1 {
		t.Fatalf("snap=%+v err=%v", snap, err)
	}
	if snap.Models[0].ContextWindow != 180000 {
		t.Fatalf("inflated context=%d", snap.Models[0].ContextWindow)
	}
}
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
