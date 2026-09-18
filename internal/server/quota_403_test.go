package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/J-York/QoderProxy/internal/credential"
	"github.com/J-York/QoderProxy/internal/qoder"
)

// quotaBody 是實測的 Free 層帳號請求付費模型的 403 響應體
// （2026-09-18 hytron 生產環境，FORK-PLAN.md §1）。
const quotaBody = `{"code":"112","message":"{\"pricingUrl\":\"https://qoder.com/pricing?client=qoder\"}"}`

// TestQuota403KeepsAccountHealthy：配額型 403 不得停用帳號，
// 客戶端收到 403 quota_exceeded，同帳號後續請求仍可用
// （FORK-PLAN.md §2.1 —— 實際咬到人的 bug）。
func TestQuota403KeepsAccountHealthy(t *testing.T) {
	tr := &fakeTransport{name: "bearer"}
	tr.chat = func(_ context.Context, _ credential.Account, _ qoder.ChatRequest, _ qoder.Model) (*qoder.UpstreamResponse, error) {
		if tr.calls.Load() == 1 {
			return nil, &qoder.HTTPError{Status: http.StatusForbidden, Message: quotaBody}
		}
		return &qoder.UpstreamResponse{Status: http.StatusOK, Body: okBody()}, nil
	}
	s, p := testServer(t, []credential.Account{serverAccount("a")}, nil, tr)

	// 第一次：premium 模型（catalog 內沒有也無妨——先到 transport 才 403 的路徑用
	// catalog 內的 lite，靠呼叫次數區分），收到 403 quota_exceeded。
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, chatRequest(context.Background(), false))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "quota_exceeded") {
		t.Fatalf("body missing quota_exceeded: %s", rr.Body.String())
	}

	// 帳號必須仍然健康。
	if got := p.Available(); got != 1 {
		t.Fatalf("available=%d, want 1 (account must not be disabled)", got)
	}

	// 第二次：同帳號的請求仍應成功（修復前會 503 accounts_exhausted）。
	rr2 := httptest.NewRecorder()
	s.ServeHTTP(rr2, chatRequest(context.Background(), false))
	if rr2.Code != http.StatusOK {
		t.Fatalf("second request code=%d body=%s", rr2.Code, rr2.Body.String())
	}
}

// TestNonQuota403DisablesAccount：非配額型 403（帳號級拒絕）維持停用行為，
// 且回 503 accounts_exhausted（候選耗盡）而非 403。
func TestNonQuota403DisablesAccount(t *testing.T) {
	tr := &fakeTransport{name: "bearer", chat: func(context.Context, credential.Account, qoder.ChatRequest, qoder.Model) (*qoder.UpstreamResponse, error) {
		return nil, &qoder.HTTPError{Status: http.StatusForbidden, Message: "account suspended"}
	}}
	s, p := testServer(t, []credential.Account{serverAccount("a")}, nil, tr)

	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, chatRequest(context.Background(), false))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "accounts_exhausted") {
		t.Fatalf("body missing accounts_exhausted: %s", rr.Body.String())
	}
	if got := p.Available(); got != 0 {
		t.Fatalf("available=%d, want 0 (account-level 403 disables)", got)
	}
}

// TestQuotaErrorCodesAcrossCandidates：多帳號下配額錯誤應繼續嘗試下一候選，
// 全部失敗後以 403 quota_exceeded 收尾（而非 503）。
func TestQuotaErrorCodesAcrossCandidates(t *testing.T) {
	tr := &fakeTransport{name: "bearer", chat: func(context.Context, credential.Account, qoder.ChatRequest, qoder.Model) (*qoder.UpstreamResponse, error) {
		return nil, &qoder.HTTPError{Status: http.StatusForbidden, Message: quotaBody}
	}}
	s, _ := testServer(t, []credential.Account{serverAccount("a"), serverAccount("b")}, nil, tr)

	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, chatRequest(context.Background(), false))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "quota_exceeded") {
		t.Fatalf("body missing quota_exceeded: %s", rr.Body.String())
	}
	if tr.calls.Load() != 2 {
		t.Fatalf("transport calls=%d, want 2 (both candidates tried)", tr.calls.Load())
	}
}

// TestStreamEnvelopeQuotaErrorIsTyped：串流路徑下信封內嵌的配額 403
// 應以 SSE 錯誤幀回報 quota_exceeded（實測的生產路徑——上游恆以
// stream=true 開流，配額錯誤在流內到達，見 FORK-PLAN.md §1）。
func TestStreamEnvelopeQuotaErrorIsTyped(t *testing.T) {
	tr := &fakeTransport{name: "bearer", chat: func(context.Context, credential.Account, qoder.ChatRequest, qoder.Model) (*qoder.UpstreamResponse, error) {
		env, mErr := json.Marshal(map[string]any{"statusCodeValue": 403, "body": quotaBody})
		if mErr != nil {
			t.Fatalf("marshal envelope: %v", mErr)
		}
		stream := "data: " + string(env) + "\n\n"
		return &qoder.UpstreamResponse{Status: http.StatusOK, Body: io.NopCloser(strings.NewReader(stream))}, nil
	}}
	s, _ := testServer(t, []credential.Account{serverAccount("a")}, nil, tr)

	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, chatRequest(context.Background(), true))
	body := rr.Body.String()
	if !strings.Contains(body, "quota_exceeded") {
		t.Fatalf("body missing quota_exceeded: %s", body)
	}
	if strings.Contains(body, "upstream_stream_error") {
		t.Fatalf("legacy generic error type leaked: %s", body)
	}
}
