package stream

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// TestEnvelopeErrorIsTyped：信封內嵌的非 200 狀態必須以 *EnvelopeError
// 返回（而非裸 error），供上層做配額分類（FORK-PLAN.md §2.1）。
func TestEnvelopeErrorIsTyped(t *testing.T) {
	stream := "data: {\"statusCodeValue\":403,\"body\":\"{\\\"code\\\":\\\"112\\\",\\\"message\\\":\\\"{\\\\\\\"pricingUrl\\\\\\\":\\\\\\\"https://qoder.com/pricing\\\\\\\"}\\\"}\"}\n\ndata: [DONE]\n\n"
	err := ParseSSE(context.Background(), io.NopCloser(strings.NewReader(stream)), time.Second, func(Event) error { return nil })
	if err == nil {
		t.Fatal("expected envelope error")
	}
	var envErr *EnvelopeError
	if !errors.As(err, &envErr) {
		t.Fatalf("error is %T, want *EnvelopeError", err)
	}
	if envErr.Status != 403 {
		t.Fatalf("status=%d, want 403", envErr.Status)
	}
	if !strings.Contains(envErr.Body, "pricingUrl") {
		t.Fatalf("body missing pricingUrl: %s", envErr.Body)
	}
}
