package qoder

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/J-York/QoderProxy/internal/credential"
	"github.com/J-York/QoderProxy/internal/protocol"
)

func TestParseExpiryUnits(t *testing.T) {
	now := time.Unix(1700000000, 0)
	ms, err := ParseExpiry(now, nil, 86400000)
	if err != nil || ms != now.UnixMilli()+86400000 {
		t.Fatalf("ms=%d err=%v", ms, err)
	}
	sec, err := ParseExpiry(now, nil, 3600)
	if err != nil || sec != now.Add(time.Hour).UnixMilli() {
		t.Fatalf("sec=%d err=%v", sec, err)
	}
	raw := json.RawMessage(`"2026-09-05T11:16:15Z"`)
	got, err := ParseExpiry(now, raw, 0)
	if err != nil || got != time.Date(2026, 9, 5, 11, 16, 15, 0, time.UTC).UnixMilli() {
		t.Fatalf("got=%d err=%v", got, err)
	}
}

func TestRefreshDoesNotFabricateExpiryOnFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, `{"refresh_token":"drt-secret"}`, 500) }))
	defer srv.Close()
	c := NewAuthClient(srv.Client())
	c.Endpoints = func(protocol.Region) (protocol.Endpoints, bool) {
		return protocol.Endpoints{DeviceRefresh: srv.URL}, true
	}
	a := credential.Account{Region: "global", TokenKind: "device", AccessToken: "old", RefreshToken: "drt-secret", ExpiresAtMS: 123}
	got, err := c.Refresh(context.Background(), a)
	if err == nil {
		t.Fatal("wanted refresh error")
	}
	if got.ExpiresAtMS != 123 || got.AccessToken != "old" {
		t.Fatalf("credentials changed on failure: %+v", got)
	}
	if strings.Contains(err.Error(), "drt-secret") {
		t.Fatalf("secret leaked: %v", err)
	}
}

func TestSanitizeSnippet(t *testing.T) {
	in := []byte(`Authorization: Bearer dt-supersecret {"refresh_token":"drt-refresh","personal_token":"pt-pat"}`)
	got := SanitizeSnippet(in, 256)
	for _, secret := range []string{"dt-supersecret", "drt-refresh", "pt-pat"} {
		if strings.Contains(got, secret) {
			t.Fatalf("secret %q leaked in %q", secret, got)
		}
	}
}
