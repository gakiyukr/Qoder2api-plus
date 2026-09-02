package qoder

import (
	"bytes"
	"testing"
)

func TestBodyEncodingRoundTrip(t *testing.T) {
	cases := [][]byte{nil, []byte("a"), []byte("hello world"), []byte(`{"messages":[{"role":"user","content":"UNION SELECT"}]}`), {0, 255, 128, 1}}
	for _, in := range cases {
		encoded := EncodeBody(in)
		if bytes.Contains([]byte(encoded), []byte("=")) {
			t.Fatalf("encoded padding leaked: %q", encoded)
		}
		got, err := DecodeBody(encoded)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, in) {
			t.Fatalf("got=%v want=%v", got, in)
		}
	}
}
