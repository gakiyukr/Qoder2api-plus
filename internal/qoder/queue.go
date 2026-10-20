package qoder

import (
	"regexp"
	"strconv"
	"time"
)

// 上游「模型級容量排隊」信號識別。
//
// 背景：Qoder 免費層的模型（dfmodel/qfmodel 等）在高峰會返回 403，body 形如
//
//	{"code":"10605","message":"{\"isQueued\":true,\"modelKey\":\"dfmodel\",
//	 \"queueCount\":1380,\"queueType\":\"p3\",\"retryAfterSeconds\":30,
//	 \"serviceAvailable\":false}"}
//
// 這是**模型容量**信號（不是帳號問題）：同一模型的所有帳號共享同一個算力池，
// 換號不會縮短等待。官方客戶端的做法是進入獨立的隊列輪詢協議、每次按
// retryAfterSeconds 等待後重試（總等待上限 60 分鐘）。
//
// 本實現採用「同步等待 + 重試同一帳號」：把上游的排隊語義封裝在網關內，
// 避免秒失敗讓客戶端把排隊誤判成請求被拒。
//
// 參考：qoder2api-hub（262★/71 fork）docs/risk-control-and-queueing.md §7.1。
// 該文檔同時指出：排隊是**模型維度**的，換帳號無助於縮短等待，
// 快速失敗 + 客戶端立即重試反而會放大失敗率。

// queueMarkerRe 匹配排隊信號的業務特徵。
// 注意：上游錯誤體在信封層會被 JSON 轉義一次（形如 \\\"code\\\":\\\"10605\\\"），
// 因此匹配需同時容忍轉義與未轉義兩種形態。
var queueMarkerRe = regexp.MustCompile(`(?:\\+")?code(?:\\+")?\s*:\s*(?:\\+")?10605(?:\\+")?|(?:\\+")?isQueued(?:\\+")?\s*:\s*(?:\\+")?true|(?:\\+")?queueType(?:\\+")?`)

// queueRetryAfterRe 從排隊錯誤體裡解析建議等待秒數（同樣容忍轉義形態）。
var queueRetryAfterRe = regexp.MustCompile(`retryAfterSeconds(?:\\+")?\s*:\s*(?:\\+")?(\d+)`)

// IsQueueError 判斷錯誤訊息是否為上游模型級排隊。
func IsQueueError(message string) bool {
	if message == "" {
		return false
	}
	return queueMarkerRe.MatchString(message)
}

// QueueWaitSeconds 從上游排隊錯誤體解析「等多久再來」；不是排隊信號則返回 0。
//
// 上游可能給出明確時長；未給出時按 30s 保守處理（與官方 retryAfterSeconds
// 的典型值一致）。返回值恆為正數。
func QueueWaitSeconds(message string) time.Duration {
	if !IsQueueError(message) {
		return 0
	}
	if m := queueRetryAfterRe.FindStringSubmatch(message); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 30 * time.Second
}
