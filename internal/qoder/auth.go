package qoder

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/J-York/QoderProxy/internal/credential"
	"github.com/J-York/QoderProxy/internal/protocol"
)

type AuthClient struct {
	HTTP      *http.Client
	Endpoints func(protocol.Region) (protocol.Endpoints, bool)
	Now       func() time.Time
}

func NewAuthClient(httpClient *http.Client) *AuthClient {
	return &AuthClient{HTTP: httpClient, Endpoints: protocol.ForRegion, Now: time.Now}
}

type tokenResponse struct {
	Token        string          `json:"token"`
	DeviceToken  string          `json:"device_token"`
	RefreshToken string          `json:"refresh_token"`
	UserID       string          `json:"user_id"`
	ExpiresAt    json.RawMessage `json:"expires_at"`
	ExpiresIn    int64           `json:"expires_in"`
}

type UserInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username"`
	Email    string `json:"email"`
}

func (c *AuthClient) ExchangePAT(ctx context.Context, region protocol.Region, pat, machineID string) (credential.Account, error) {
	ep, ok := c.Endpoints(region)
	if !ok {
		return credential.Account{}, errors.New("unsupported region")
	}
	body, _ := json.Marshal(map[string]string{"personal_token": pat})
	var tr tokenResponse
	if err := c.doJSON(ctx, http.MethodPost, ep.PATExchange, "", body, &tr, map[string]string{
		"Cosy-Version":    protocol.OpenAPICosyVersion,
		"Cosy-ClientType": protocol.ClientType,
	}); err != nil {
		return credential.Account{}, fmt.Errorf("PAT exchange: %w", err)
	}
	if tr.Token == "" {
		return credential.Account{}, errors.New("PAT exchange returned no token")
	}
	info, err := c.UserInfo(ctx, region, tr.Token)
	if err != nil {
		return credential.Account{}, err
	}
	expires, err := ParseExpiry(c.Now(), tr.ExpiresAt, tr.ExpiresIn)
	if err != nil {
		return credential.Account{}, fmt.Errorf("PAT exchange expiry: %w", err)
	}
	if machineID == "" {
		machineID, err = randomUUID()
		if err != nil {
			return credential.Account{}, err
		}
	}
	return credential.Account{
		ID:           string(region) + "-" + info.ID,
		Region:       string(region),
		TokenKind:    "job",
		AccessToken:  tr.Token,
		RefreshToken: tr.RefreshToken,
		ExpiresAtMS:  expires,
		UserID:       info.ID,
		Name:         firstNonEmpty(info.Name, info.Username),
		Email:        info.Email,
		MachineID:    machineID,
	}, nil
}

type DeviceFlow struct {
	VerificationURL string
	PollURL         string
	MachineID       string
}

func (c *AuthClient) BeginDeviceFlow(region protocol.Region) (DeviceFlow, error) {
	if region != protocol.Global {
		return DeviceFlow{}, errors.New("browser device login is currently supported only for global region")
	}
	ep, _ := c.Endpoints(region)
	verifierBytes := make([]byte, 32)
	if _, err := rand.Read(verifierBytes); err != nil {
		return DeviceFlow{}, err
	}
	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes)
	challengeBytes := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeBytes[:])
	nonce, err := randomUUID()
	if err != nil {
		return DeviceFlow{}, err
	}
	machine, err := randomUUID()
	if err != nil {
		return DeviceFlow{}, err
	}
	v := url.Values{"challenge": {challenge}, "challenge_method": {"S256"}, "nonce": {nonce}, "machine_id": {machine}, "client_id": {protocol.DeviceClientID}}
	p := url.Values{"nonce": {nonce}, "verifier": {verifier}, "challenge_method": {"S256"}}
	return DeviceFlow{VerificationURL: ep.DeviceLogin + "?" + v.Encode(), PollURL: ep.DevicePoll + "?" + p.Encode(), MachineID: machine}, nil
}

func (c *AuthClient) PollDeviceFlow(ctx context.Context, flow DeviceFlow) (credential.Account, error) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	deadline := time.NewTimer(5 * time.Minute)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return credential.Account{}, ctx.Err()
		case <-deadline.C:
			return credential.Account{}, errors.New("device authorization timed out")
		case <-ticker.C:
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, flow.PollURL, nil)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", protocol.UserAgent)
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return credential.Account{}, err
		}
		b, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return credential.Account{}, readErr
		}
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusAccepted {
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return credential.Account{}, statusError("device poll", resp.StatusCode, b)
		}
		var tr tokenResponse
		if err := json.Unmarshal(b, &tr); err != nil {
			return credential.Account{}, err
		}
		token := firstNonEmpty(tr.Token, tr.DeviceToken)
		if token == "" || tr.UserID == "" {
			return credential.Account{}, errors.New("device poll returned incomplete credentials")
		}
		expires, err := ParseExpiry(c.Now(), tr.ExpiresAt, tr.ExpiresIn)
		if err != nil {
			return credential.Account{}, err
		}
		info, err := c.UserInfo(ctx, protocol.Global, token)
		if err != nil {
			return credential.Account{}, err
		}
		return credential.Account{ID: "global-" + tr.UserID, Region: "global", TokenKind: "device", AccessToken: token, RefreshToken: tr.RefreshToken, ExpiresAtMS: expires, UserID: tr.UserID, Name: firstNonEmpty(info.Name, info.Username), Email: info.Email, MachineID: flow.MachineID}, nil
	}
}

func (c *AuthClient) UserInfo(ctx context.Context, region protocol.Region, token string) (UserInfo, error) {
	ep, ok := c.Endpoints(region)
	if !ok {
		return UserInfo{}, errors.New("unsupported region")
	}
	var info UserInfo
	if err := c.doJSON(ctx, http.MethodGet, ep.UserInfo, token, nil, &info, nil); err != nil {
		return UserInfo{}, fmt.Errorf("userinfo: %w", err)
	}
	if info.ID == "" {
		return UserInfo{}, errors.New("userinfo returned no user id")
	}
	return info, nil
}

func (c *AuthClient) Refresh(ctx context.Context, account credential.Account) (credential.Account, error) {
	if account.RefreshToken == "" {
		return account, errors.New("token is expired and no refresh token is available")
	}
	ep, ok := c.Endpoints(protocol.Region(account.Region))
	if !ok {
		return account, errors.New("unsupported region")
	}
	endpoint := ep.JobRefresh
	if account.TokenKind == "device" {
		endpoint = ep.DeviceRefresh
	}
	body, _ := json.Marshal(map[string]string{"refresh_token": account.RefreshToken})
	var tr tokenResponse
	if err := c.doJSON(ctx, http.MethodPost, endpoint, "", body, &tr, nil); err != nil {
		return account, fmt.Errorf("refresh: %w", err)
	}
	token := firstNonEmpty(tr.Token, tr.DeviceToken)
	if token == "" {
		return account, errors.New("refresh returned no access token")
	}
	expires, err := ParseExpiry(c.Now(), tr.ExpiresAt, tr.ExpiresIn)
	if err != nil {
		return account, err
	}
	account.AccessToken = token
	if tr.RefreshToken != "" {
		account.RefreshToken = tr.RefreshToken
	}
	account.ExpiresAtMS = expires
	return account, nil
}

func (c *AuthClient) Quota(ctx context.Context, account credential.Account) (json.RawMessage, error) {
	ep, _ := c.Endpoints(protocol.Region(account.Region))
	var raw json.RawMessage
	if err := c.doJSON(ctx, http.MethodGet, ep.QuotaUsage, account.AccessToken, nil, &raw, nil); err != nil {
		return nil, err
	}
	return raw, nil
}

func (c *AuthClient) doJSON(ctx context.Context, method, endpoint, token string, body []byte, out any, extra map[string]string) error {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", protocol.UserAgent)
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return statusError("upstream", resp.StatusCode, b)
	}
	if out == nil {
		return nil
	}
	if raw, ok := out.(*json.RawMessage); ok {
		*raw = append((*raw)[:0], b...)
		return nil
	}
	return json.Unmarshal(b, out)
}

func ParseExpiry(now time.Time, raw json.RawMessage, expiresIn int64) (int64, error) {
	if len(raw) > 0 && string(raw) != "null" && string(raw) != `""` {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			if t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(s)); err == nil {
				return t.UnixMilli(), nil
			}
			if ms, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil && ms > 0 {
				return normalizeEpoch(ms), nil
			}
		}
		var n int64
		if json.Unmarshal(raw, &n) == nil && n > 0 {
			return normalizeEpoch(n), nil
		}
		return 0, errors.New("unrecognized expires_at")
	}
	if expiresIn > 0 {
		// Observed Qoder responses use milliseconds (86400000 / 2591999998).
		// Small values are accepted as seconds for backward compatibility.
		if expiresIn <= 31_536_000 {
			return now.Add(time.Duration(expiresIn) * time.Second).UnixMilli(), nil
		}
		return now.UnixMilli() + expiresIn, nil
	}
	return 0, errors.New("token response did not include a usable expiry")
}

func normalizeEpoch(v int64) int64 {
	if v < 100_000_000_000 {
		return v * 1000
	}
	return v
}

func statusError(op string, status int, body []byte) error {
	return fmt.Errorf("%s failed with HTTP %d: %s", op, status, SanitizeSnippet(body, 256))
}

func SanitizeSnippet(b []byte, max int) string {
	s := string(b)
	s = regexp.MustCompile(`(?i)Bearer\s+[^\s"',}]+`).ReplaceAllString(s, "Bearer <redacted>")
	s = regexp.MustCompile(`\b(?:pt|jt|jrt|dt|drt)-[A-Za-z0-9._-]+`).ReplaceAllString(s, "<redacted-token>")
	s = regexp.MustCompile(`(?i)("?(?:authorization|security_oauth_token|access_token|refresh_token|personal_token|cosy-key)"?\s*[:=]\s*)"?[^",}\s]+"?`).ReplaceAllString(s, `${1}<redacted>`)
	if len(s) > max {
		s = s[:max] + "…"
	}
	return strings.TrimSpace(s)
}

func randomUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
