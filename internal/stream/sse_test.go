package stream

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseStandardSSEPreservesReasoningUsageFinish(t *testing.T) {
	b, err := os.ReadFile("testdata/bearer_standard.sse")
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	err = ParseSSE(context.Background(), io.NopCloser(strings.NewReader(string(b))), time.Second, func(e Event) error { events = append(events, e); return nil })
	if err != nil {
		t.Fatal(err)
	}
	acc := NewAccumulator()
	for _, e := range events {
		acc.Add(e)
	}
	if acc.Reasoning != "plan" || acc.Content != "OK" {
		t.Fatalf("content=%q reasoning=%q", acc.Content, acc.Reasoning)
	}
	if acc.FinishReason == nil || *acc.FinishReason != "length" {
		t.Fatalf("finish=%v", acc.FinishReason)
	}
	if acc.Usage == nil || acc.Usage.PromptTokensDetails.CachedTokens != 3 || acc.Usage.PromptTokensDetails.CacheWriteTokens != 1 {
		t.Fatalf("usage=%+v", acc.Usage)
	}
	if events[len(events)-1].Kind != "done" {
		t.Fatal("missing done")
	}
}

func TestParseEnvelopeSSE(t *testing.T) {
	b, _ := os.ReadFile("testdata/envelope.sse")
	acc := NewAccumulator()
	err := ParseSSE(context.Background(), io.NopCloser(strings.NewReader(string(b))), time.Second, func(e Event) error { acc.Add(e); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if acc.Content != "OK" || acc.Usage == nil || acc.Usage.PromptTokensDetails.CacheWriteTokens != 3 {
		t.Fatalf("acc=%+v usage=%+v", acc, acc.Usage)
	}
}

func TestDoneEndsEvenIfConnectionStaysOpen(t *testing.T) {
	r, w := io.Pipe()
	returned := make(chan error, 1)
	go func() { returned <- ParseSSE(context.Background(), r, time.Second, func(Event) error { return nil }) }()
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	select {
	case err := <-returned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("parser waited for connection close after DONE")
	}
	_ = w.Close()
}

func TestSSELineAcrossChunksAndLastLineWithoutNewline(t *testing.T) {
	r, w := io.Pipe()
	go func() {
		_, _ = io.WriteString(w, "da")
		_, _ = io.WriteString(w, "ta: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]")
		_ = w.Close()
	}()
	var content string
	err := ParseSSE(context.Background(), r, time.Second, func(e Event) error { content += e.Content; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if content != "x" {
		t.Fatalf("content=%q", content)
	}
}

func TestMalformedSSEIsNotSwallowed(t *testing.T) {
	err := ParseSSE(context.Background(), io.NopCloser(strings.NewReader("data: {nope}\n\n")), time.Second, func(Event) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "malformed SSE JSON") {
		t.Fatalf("err=%v", err)
	}
}

func TestIncompleteJSONIsReassembledAcrossSSEFrames(t *testing.T) {
	s := "data: {\"id\":\"chat-1\",\"model\":\"performance\",\"choices\":[{\"delta\":{\"content\":\"hel\n\n" +
		"data: lo\"},\"finish_reason\":null}]}\n\n" +
		"data: [DONE]\n\n"
	var content string
	err := ParseSSE(context.Background(), io.NopCloser(strings.NewReader(s)), time.Second, func(e Event) error {
		content += e.Content
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if content != "hello" {
		t.Fatalf("content=%q", content)
	}
}

func TestQoderDataLinesWithoutBlankSeparators(t *testing.T) {
	s := "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"b\"}}]}\n" +
		"data: [DONE]\n"
	var content string
	err := ParseSSE(context.Background(), io.NopCloser(strings.NewReader(s)), time.Second, func(e Event) error {
		content += e.Content
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if content != "ab" {
		t.Fatalf("content=%q", content)
	}
}

func TestIncompleteJSONBeforeDoneIsRejected(t *testing.T) {
	s := "data: {\"choices\":[\n\ndata: [DONE]\n\n"
	err := ParseSSE(context.Background(), io.NopCloser(strings.NewReader(s)), time.Second, func(Event) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "incomplete SSE JSON") {
		t.Fatalf("err=%v", err)
	}
}

func TestEnvelopeErrorPropagates(t *testing.T) {
	s := "data: {\"statusCodeValue\":429,\"body\":\"quota exceeded\"}\n\n"
	err := ParseSSE(context.Background(), io.NopCloser(strings.NewReader(s)), time.Second, func(Event) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("err=%v", err)
	}
}

func TestOversizeSSELineRejected(t *testing.T) {
	s := "data: " + strings.Repeat("x", maxSSEEventBytes+1) + "\n\n"
	err := ParseSSE(context.Background(), io.NopCloser(strings.NewReader(s)), time.Second, func(Event) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "16 MiB") {
		t.Fatalf("err=%v", err)
	}
}
