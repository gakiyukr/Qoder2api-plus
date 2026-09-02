package qoder

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/J-York/QoderProxy/internal/config"
	"github.com/J-York/QoderProxy/internal/protocol"
)

type hostGuard struct{ next http.RoundTripper }

func (g hostGuard) RoundTrip(req *http.Request) (*http.Response, error) {
	if _, ok := protocol.AllowedUpstreamHosts[req.URL.Hostname()]; !ok {
		return nil, errors.New("upstream host is not allowlisted")
	}
	return g.next.RoundTrip(req)
}

func NewHTTPClient(cfg config.Config) (*http.Client, error) {
	dialer := &net.Dialer{Timeout: cfg.ConnectTimeout.Duration, KeepAlive: 30 * time.Second}
	t := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: cfg.ResponseTimeout.Duration,
	}
	if cfg.ProxyURL != "" {
		u, err := url.Parse(cfg.ProxyURL)
		if err != nil {
			return nil, err
		}
		if u.Scheme == "socks5" {
			return nil, errors.New("socks5 proxy requires a non-standard dependency; use an HTTP CONNECT proxy")
		}
		t.Proxy = http.ProxyURL(u)
	}
	return &http.Client{
		Transport: hostGuard{next: t},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

func withTotalTimeout(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, d)
}
