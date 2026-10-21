package qoder

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
)

// 上游 Qoder 以 HTTP 200 建立 SSE 流，随后在**流内**投递一个错误信封
// （statusCodeValue != 200）来表示「模型容量排队」。此时 HTTP 头已发出、
// 客户端已收到 200，網關無法再「失敗重試」——只能把錯誤體轉成客戶端可見的
// 502/403。
//
// 但排隊本質上是可以等待後重試的（官方客戶端就是等隊列放行後再發真正的推理）。
// 因此在**交付給上層之前**先探查流首：若第一個 SSE 事件就是排隊信封，
// 就把它轉成一個可在 server 層重試的錯誤。
//
// 探查方式：讀取足夠的行直到遇到第一個完整事件（空行分隔），判斷是否為排隊；
// 讀到的內容原樣回放給後續的 ParseSSE（用 peekReader 實現）。

// queueProbeBytes 探查時最多預讀的位元組數。
// 排隊信封的特徵（10605 / isQueued / queueType）都出現在流首，256 字節足夠。
const queueProbeBytes = 4096

// peekReader 在前端緩衝已讀內容，支援 Read 與 Close。
type peekReader struct {
	src  io.ReadCloser
	buf  *bytes.Buffer
	done bool
}

func newPeekReader(src io.ReadCloser) *peekReader {
	return &peekReader{src: src, buf: bytes.NewBuffer(nil)}
}

func (p *peekReader) Read(b []byte) (int, error) {
	if p.buf.Len() > 0 {
		return p.buf.Read(b)
	}
	if p.done {
		return 0, io.EOF
	}
	return p.src.Read(b)
}

func (p *peekReader) Close() error { return p.src.Close() }

// probeQueueEnvelope 預讀流首並判斷是否為排隊信封。
//
// 返回值：
//   - body 非空且是排隊信封 → 回傳 true 與 HTTPError（供上層重試）
//   - 否則 → 回傳 false，且已把預讀內容放回 reader（不丟資料）
func probeQueueEnvelope(up *UpstreamResponse) (*HTTPError, bool) {
	if up == nil || up.Body == nil {
		return nil, false
	}
	br := bufio.NewReaderSize(up.Body, 64*1024)
	var head bytes.Buffer
	// 讀到第一個空行（事件邊界）或達到上限為止。
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			head.Write(line)
			if head.Len() > queueProbeBytes {
				break
			}
			if len(line) == 1 && line[0] == '\n' {
				break
			}
		}
		if err != nil {
			break
		}
	}
	peeked := head.Bytes()
	// 把預讀內容放回：後續 ParseSSE 仍能完整讀到流。
	rest, _ := io.ReadAll(br)
	replay := io.NopCloser(io.MultiReader(bytes.NewReader(peeked), bytes.NewReader(rest)))
	up.Body = replay

	if !IsQueueError(string(peeked)) {
		return nil, false
	}
	return &HTTPError{Status: http.StatusForbidden, Message: SanitizeSnippet(peeked, 256)}, true
}
