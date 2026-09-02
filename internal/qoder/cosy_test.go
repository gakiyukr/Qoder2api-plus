package qoder

import (
	"crypto/md5"
	"encoding/hex"
	"testing"
	"time"

	"github.com/J-York/QoderProxy/internal/credential"
)

func TestCosySignatureFixedVector(t *testing.T) {
	got := CosySignature("payload", "key", "1700000000", []byte("body"), "/api/v2/model/list")
	const want = "2b5c2017bfa0d840aa8cb20535abbac4"
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}
func TestCosyHeadersBindExactBodyAndPath(t *testing.T) {
	ids := []string{"11111111-2222-4333-8444-555555555555", "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", "99999999-8888-4777-8666-555555555555"}
	i := 0
	s := &CosySigner{Now: func() time.Time { return time.Unix(1700000000, 0) }, UUID: func() (string, error) { v := ids[i]; i++; return v, nil }}
	body := []byte("encoded-body")
	h, err := s.Headers(body, "https://api3.qoder.sh/algo/api/v2/model/list?Encode=1", credential.Account{UserID: "u", AccessToken: "dt-secret", Name: "n", Email: "e", MachineID: "m"})
	if err != nil {
		t.Fatal(err)
	}
	sum := md5.Sum(body)
	if h.Get("Cosy-Bodyhash") != hex.EncodeToString(sum[:]) || h.Get("Cosy-Bodylength") != "12" || h.Get("Cosy-Sigpath") != "/api/v2/model/list" {
		t.Fatalf("headers=%v", h)
	}
	if h.Get("Authorization")[:12] != "Bearer COSY." {
		t.Fatal("missing COSY authorization")
	}
}
