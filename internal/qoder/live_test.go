package qoder

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/J-York/QoderProxy/internal/config"
	"github.com/J-York/QoderProxy/internal/credential"
	qstream "github.com/J-York/QoderProxy/internal/stream"
)

func TestLiveBearerOptIn(t *testing.T) {
	if os.Getenv("QODER_LIVE_TEST") != "1" {
		t.Skip("set QODER_LIVE_TEST=1 to enable")
	}
	path := os.Getenv("QODER_LIVE_CREDENTIALS")
	if path == "" {
		t.Fatal("QODER_LIVE_CREDENTIALS must point to a test-only 0600 credential file")
	}
	accounts, err := credential.NewStore(path).Load()
	if err != nil || len(accounts) == 0 {
		t.Fatalf("load live credentials: accounts=%d err=%v", len(accounts), err)
	}
	cfg := config.Default()
	client, err := NewHTTPClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	account := accounts[0]
	if account.ExpiresAtMS < time.Now().Add(5*time.Minute).UnixMilli() {
		account, err = NewAuthClient(client).Refresh(ctx, account)
		if err != nil {
			t.Fatal(err)
		}
	}
	catalog := NewCatalog(client, time.Hour)
	snap, err := catalog.Get(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Models) == 0 || snap.Degraded {
		t.Fatalf("live catalog unavailable: %+v", snap)
	}
	model := snap.Models[0]
	req, err := ParseChatRequest([]byte(`{"model":"` + model.ID + `","messages":[{"role":"system","content":"Return exactly LIVE_OK."},{"role":"user","content":"live transport check"}],"stream":true,"stream_options":{"include_usage":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&BearerTransport{HTTP: client}).Chat(ctx, account, req, model)
	if err != nil {
		t.Fatal(err)
	}
	sawDelta, sawDone := false, false
	err = qstream.ParseSSE(ctx, resp.Body, 30*time.Second, func(e qstream.Event) error {
		if e.Content != "" || e.Reasoning != "" {
			sawDelta = true
		}
		if e.Kind == "done" {
			sawDone = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sawDelta || !sawDone {
		t.Fatalf("live response incomplete delta=%v done=%v", sawDelta, sawDone)
	}
}
