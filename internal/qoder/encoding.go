package qoder

import (
	"encoding/base64"
	"errors"
	"strings"
)

const (
	standardAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	customAlphabet   = "_doRTgHZBKcGVjlvpC,@aFSx#DPuNJme&i*MzLOEn)sUrthbf%Y^w.(kIQyXqWA!"
)

var encodeTable = strings.NewReplacer(buildPairs(standardAlphabet+"=", customAlphabet+"$")...)
var decodeTable = strings.NewReplacer(buildPairs(customAlphabet+"$", standardAlphabet+"=")...)

func buildPairs(from, to string) []string {
	pairs := make([]string, 0, len(from)*2)
	for i := 0; i < len(from); i++ {
		pairs = append(pairs, from[i:i+1], to[i:i+1])
	}
	return pairs
}

func EncodeBody(plain []byte) string {
	if len(plain) == 0 {
		return ""
	}
	std := base64.StdEncoding.EncodeToString(plain)
	a := len(std) / 3
	reordered := std[len(std)-a:] + std[a:len(std)-a] + std[:a]
	return encodeTable.Replace(reordered)
}

func DecodeBody(encoded string) ([]byte, error) {
	if encoded == "" {
		return nil, nil
	}
	mapped := decodeTable.Replace(encoded)
	a := len(mapped) / 3
	if a == 0 {
		return nil, errors.New("encoded body is too short")
	}
	standard := mapped[len(mapped)-a:] + mapped[a:len(mapped)-a] + mapped[:a]
	return base64.StdEncoding.DecodeString(standard)
}
