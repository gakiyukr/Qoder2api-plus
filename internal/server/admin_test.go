package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/J-York/QoderProxy/internal/credential"
	"github.com/J-York/QoderProxy/internal/pool"
)

type fakeQuota struct {
	body  string
	err   error
	calls atomic.Int64
}

func (f *fakeQuota) Quota(context.Context, credential.Account) (json.RawMessage, error) {
	f.calls.Add(1)
	if f.err != nil {
		return nil, f.err
	}
	return json.RawMessage(f.body), nil
}

// 生產實測的 quota/usage 響應形狀（Free 層，2026-09-18 hytron）。
const quotaPayload = `{"userId":"u1","userType":"personal_standard","usageType":"credits","isQuotaExceeded":true,"userQuota":{"total":100,"used":20,"remaining":80,"percentage":20,"unit":"credits"},"outerProviders":[{"id":"pkg1","remaining":100}]}`

func adminRequest(path, key string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	return r
}

// TestAdminStatusServesPoolStates：/admin/status 需 key，返回每帳號狀態。
func TestAdminStatusServesPoolStates(t *testing.T) {
	s, _ := testServer(t, []credential.Account{serverAccount("a"), serverAccount("b")}, nil, &fakeTransport{name: "bearer"})
	s.APIKey = "testkey"

	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, adminRequest("/admin/status", "wrong"))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("wrong key: code=%d", rr.Code)
	}

	rr = httptest.NewRecorder()
	s.ServeHTTP(rr, adminRequest("/admin/status", "testkey"))
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"state":"healthy"`) {
		t.Fatalf("body missing healthy state: %s", rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "access_token") {
		t.Fatalf("token leaked in admin payload: %s", rr.Body.String())
	}
}

// TestAdminQuotaParsesUpstream：healthy 帳號查上游並解析餘額欄位。
func TestAdminQuotaParsesUpstream(t *testing.T) {
	fq := &fakeQuota{body: quotaPayload}
	s, _ := testServer(t, []credential.Account{serverAccount("a")}, nil, &fakeTransport{name: "bearer"})
	s.QuotaCli = fq
	s.APIKey = "testkey"

	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, adminRequest("/admin/quota", "testkey"))
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Accounts []struct {
			Account       string  `json:"account"`
			State         string  `json:"state"`
			UserType      string  `json:"user_type"`
			Total         float64 `json:"total"`
			Used          float64 `json:"used"`
			Remaining     float64 `json:"remaining"`
			Unit          string  `json:"unit"`
			QuotaExceeded bool    `json:"quota_exceeded"`
			Error         string  `json:"error"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, rr.Body.String())
	}
	if len(resp.Accounts) != 1 {
		t.Fatalf("rows=%d", len(resp.Accounts))
	}
	row := resp.Accounts[0]
	if row.State != "healthy" || row.Error != "" {
		t.Fatalf("row=%+v", row)
	}
	if row.UserType != "personal_standard" || row.Total != 100 || row.Used != 20 || row.Remaining != 80 || row.Unit != "credits" {
		t.Fatalf("quota fields wrong: %+v", row)
	}
	if !row.QuotaExceeded {
		t.Fatal("quota_exceeded should be true")
	}
	if !strings.Contains(rr.Body.String(), "add_on_packages") {
		t.Fatalf("add_on_packages missing: %s", rr.Body.String())
	}
	if fq.calls.Load() != 1 {
		t.Fatalf("upstream calls=%d", fq.calls.Load())
	}
	// token 不得出現在響應中
	if strings.Contains(rr.Body.String(), "valid") {
		t.Fatalf("token leaked: %s", rr.Body.String())
	}
}

// TestAdminQuotaSkipsUnhealthy：非 healthy 帳號不打上游，僅標記 skipped。
func TestAdminQuotaSkipsUnhealthy(t *testing.T) {
	fq := &fakeQuota{body: quotaPayload}
	s, p := testServer(t, []credential.Account{serverAccount("a"), serverAccount("b")}, nil, &fakeTransport{name: "bearer"})
	s.QuotaCli = fq
	s.APIKey = "testkey"

	entry, _ := p.Select("", nil)
	p.MarkDisabled(entry, "test")

	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, adminRequest("/admin/quota", "testkey"))
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if fq.calls.Load() != 1 {
		t.Fatalf("upstream calls=%d, want 1 (unhealthy skipped)", fq.calls.Load())
	}
	if !strings.Contains(rr.Body.String(), "skipped: account disabled") {
		t.Fatalf("skip marker missing: %s", rr.Body.String())
	}
}

// TestAdminQuotaWithoutClient：未配置 QuotaCli 時 503。
func TestAdminQuotaWithoutClient(t *testing.T) {
	s, _ := testServer(t, []credential.Account{serverAccount("a")}, nil, &fakeTransport{name: "bearer"})
	s.APIKey = "testkey"

	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, adminRequest("/admin/quota", "testkey"))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
}

// TestAdminEntriesCoversAllStates：AdminEntries 遍歷所有帳號（含非 healthy）。
func TestAdminEntriesCoversAllStates(t *testing.T) {
	p := pool.New([]credential.Account{serverAccount("a"), serverAccount("b")}, nil, nil)
	entry, _ := p.Select("", nil)
	p.MarkDisabled(entry, "test")

	seen := map[string]string{}
	p.AdminEntries(func(a credential.Account, state string) {
		seen[a.ID] = state
	})
	if len(seen) != 2 {
		t.Fatalf("seen=%v, want both entries", seen)
	}
	states := map[string]int{}
	for _, st := range seen {
		states[st]++
	}
	if states["healthy"] != 1 || states["disabled"] != 1 {
		t.Fatalf("states=%v, want 1 healthy + 1 disabled", states)
	}
}
