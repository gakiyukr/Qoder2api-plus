package qoder

import (
	"io"
	"strings"
	"testing"
)

// buildQueueSSEHead 构造真实形态的排队 SSE 流首：
// 上游以 HTTP 200 建流后，在流内投递一个 statusCodeValue=403 的错误信封。
func buildQueueSSEHead() string {
	// 内层 body（上游的错误体）：含 10605 / isQueued / queueType / retryAfterSeconds
	inner := `{\"code\":\"403\",\"message\":\"{\\\"code\\\":\\\"10605\\\",\\\"message\\\":\\\"{\\\\\\\"isQueued\\\\\\\":true,\\\\\\\"modelKey\\\\\\\":\\\\\\\"dfmodel\\\\\\\",\\\\\\"queueType\\\\\\\":\\\\\\\"p3\\\\\\\",\\\\\\"retryAfterSeconds\\\\\\\":30\"}\"}`
	return "data: {\"statusCodeValue\":403,\"body\":\"" + inner + "\"}\n\n"
}

func TestProbeQueueEnvelopeDetectsQueue(t *testing.T) {
	head := buildQueueSSEHead()
	up := &UpstreamResponse{Body: io.NopCloser(strings.NewReader(head + "data: {\"more\":1}\n\n"))}
	qerr, queued := probeQueueEnvelope(up)
	if !queued {
		t.Fatalf("expected queued=true for head=%q", head)
	}
	if qerr == nil || qerr.Status != 403 {
		t.Fatalf("unexpected error: %+v", qerr)
	}
	if !IsQueueError(qerr.Message) {
		t.Fatalf("queue marker lost in message: %q", qerr.Message)
	}
}

func TestProbeQueueEnvelopePassesThroughNormalStream(t *testing.T) {
	body := "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\"}\n\ndata: {\"more\":1}\n\n"
	up := &UpstreamResponse{Body: io.NopCloser(strings.NewReader(body))}
	if _, queued := probeQueueEnvelope(up); queued {
		t.Fatalf("expected queued=false for normal stream")
	}
	// 关键：探查后流内容必须完整保留（不能吞数据）。
	got, err := io.ReadAll(up.Body)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != body {
		t.Fatalf("stream corrupted:\n got: %q\nwant: %q", got, body)
	}
}
