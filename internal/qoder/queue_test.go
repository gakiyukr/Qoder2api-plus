package qoder

import (
	"testing"
	"time"
)

func TestIsQueueError(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want bool
	}{
		{"空訊息", "", false},
		{"配額拒絕不是排隊", `{"code":"112","pricingUrl":"https://x"}`, false},
		{"未轉義的排隊體", `{"code":"10605","message":"{\"isQueued\":true}"}`, true},
		{"轉義一层的排隊體", `envelope upstream status 403: {"code":"403","message":"{\"code\":\"10605\"`, true},
		{"轉義兩层的排隊體", `{"code":"403","message":"{\\\"code\\\":\\\"10605\\\",\\\"message\\\":\\\"{\\\\\\\"isQueued\\\\\\\":true`, true},
		{"僅 queueType 特徵", `{\"queueType\":\"p3\"}`, true},
		{"普通 403 非排隊", `envelope upstream status 403: {"code":"403","message":"forbidden"}`, false},
	}
	for _, c := range cases {
		if got := IsQueueError(c.msg); got != c.want {
			t.Errorf("%s: IsQueueError = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestQueueWaitSeconds(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want time.Duration
	}{
		{"非排隊", `{"code":"112"}`, 0},
		{"帶明確時長", `{\"code\":\"10605\",\"retryAfterSeconds\":45}`, 45 * time.Second},
		{"轉義形態的明確時長", `{\"code\":\"10605\",\"retryAfterSeconds\\\":30`, 30 * time.Second},
		{"排隊但無時長", `{\"code\":\"10605\",\"isQueued\":true}`, 30 * time.Second},
	}
	for _, c := range cases {
		if got := QueueWaitSeconds(c.msg); got != c.want {
			t.Errorf("%s: QueueWaitSeconds = %v, want %v", c.name, got, c.want)
		}
	}
}
