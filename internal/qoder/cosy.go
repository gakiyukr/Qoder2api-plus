package qoder

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/J-York/QoderProxy/internal/credential"
	"github.com/J-York/QoderProxy/internal/protocol"
)

const qoderRSAPublicKey = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDA8iMH5c02LilrsERw9t6Pv5Nc
4k6Pz1EaDicBMpdpxKduSZu5OANqUq8er4GM95omAGIOPOh+Nx0spthYA2BqGz+l
6HRkPJ7S236FZz73In/KVuLnwI8JJ2CbuJap8kvheCCZpmAWpb/cPx/3Vr/J6I17
XcW+ML9FoCI6AOvOzwIDAQAB
-----END PUBLIC KEY-----`

type cosyUserInfo struct {
	UID                string `json:"uid"`
	SecurityOAuthToken string `json:"security_oauth_token"`
	Name               string `json:"name"`
	AID                string `json:"aid"`
	Email              string `json:"email"`
}

type cosyPayload struct {
	Version     string `json:"version"`
	RequestID   string `json:"requestId"`
	Info        string `json:"info"`
	CosyVersion string `json:"cosyVersion"`
	IDEVersion  string `json:"ideVersion"`
}

type CosySigner struct {
	Now  func() time.Time
	UUID func() (string, error)
}

func NewCosySigner() *CosySigner { return &CosySigner{Now: time.Now, UUID: randomUUID} }

var (
	publicKeyOnce sync.Once
	publicKey     *rsa.PublicKey
	publicKeyErr  error
)

func parsePublicKey() (*rsa.PublicKey, error) {
	publicKeyOnce.Do(func() {
		block, _ := pem.Decode([]byte(qoderRSAPublicKey))
		if block == nil {
			publicKeyErr = errors.New("invalid COSY public key")
			return
		}
		var parsed any
		parsed, publicKeyErr = x509.ParsePKIXPublicKey(block.Bytes)
		if publicKeyErr != nil {
			return
		}
		var ok bool
		publicKey, ok = parsed.(*rsa.PublicKey)
		if !ok {
			publicKeyErr = errors.New("COSY key is not RSA")
		}
	})
	return publicKey, publicKeyErr
}

func (s *CosySigner) Headers(body []byte, endpoint string, account credential.Account) (http.Header, error) {
	if account.UserID == "" || account.AccessToken == "" {
		return nil, errors.New("COSY requires user id and access token")
	}
	uuid, err := s.UUID()
	if err != nil {
		return nil, err
	}
	aesKey := strings.ReplaceAll(uuid, "-", "")
	if len(aesKey) < 16 {
		return nil, errors.New("generated UUID is too short")
	}
	aesKey = aesKey[:16]
	infoJSON, _ := json.Marshal(cosyUserInfo{UID: account.UserID, SecurityOAuthToken: account.AccessToken, Name: account.Name, Email: account.Email})
	info, err := encryptAESCBC(infoJSON, []byte(aesKey))
	if err != nil {
		return nil, err
	}
	pub, err := parsePublicKey()
	if err != nil {
		return nil, err
	}
	keyCipher, err := rsa.EncryptPKCS1v15(rand.Reader, pub, []byte(aesKey))
	if err != nil {
		return nil, err
	}
	requestID, err := s.UUID()
	if err != nil {
		return nil, err
	}
	payloadJSON, _ := json.Marshal(cosyPayload{Version: "v1", RequestID: requestID, Info: base64.StdEncoding.EncodeToString(info), CosyVersion: protocol.GatewayCosyVersion, IDEVersion: ""})
	payload := base64.StdEncoding.EncodeToString(payloadJSON)
	cosyKey := base64.StdEncoding.EncodeToString(keyCipher)
	date := strconv.FormatInt(s.Now().Unix(), 10)
	sigPath, err := SignaturePath(endpoint)
	if err != nil {
		return nil, err
	}
	sig := CosySignature(payload, cosyKey, date, body, sigPath)
	bodyHash := md5.Sum(body)
	machineOS := "x86_64_linux"
	if runtime.GOOS == "windows" {
		machineOS = "x86_64_windows"
	} else if runtime.GOARCH == "arm64" {
		machineOS = "aarch64_linux"
	}
	h := make(http.Header)
	h.Set("Authorization", "Bearer COSY."+payload+"."+sig)
	h.Set("Cosy-Key", cosyKey)
	h.Set("Cosy-User", account.UserID)
	h.Set("Cosy-Date", date)
	h.Set("Cosy-Version", protocol.GatewayCosyVersion)
	h.Set("Cosy-Machineid", account.MachineID)
	h.Set("Cosy-Machinetoken", account.MachineID)
	h.Set("Cosy-Machinetype", protocol.ClientType)
	h.Set("Cosy-Machineos", machineOS)
	h.Set("Cosy-Clienttype", protocol.ClientType)
	h.Set("Cosy-Clientip", "127.0.0.1")
	h.Set("Cosy-Bodyhash", hex.EncodeToString(bodyHash[:]))
	h.Set("Cosy-Bodylength", strconv.Itoa(len(body)))
	h.Set("Cosy-Sigpath", sigPath)
	h.Set("Cosy-Data-Policy", protocol.DataPolicy)
	h.Set("Cosy-Organization-Id", "")
	h.Set("Cosy-Organization-Tags", "")
	h.Set("Login-Version", protocol.LoginVersion)
	xid, _ := s.UUID()
	h.Set("X-Request-Id", xid)
	return h, nil
}

func encryptAESCBC(plain, key []byte) ([]byte, error) {
	if len(key) != 16 {
		return nil, errors.New("AES-128 key must be 16 bytes")
	}
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	pad := b.BlockSize() - len(plain)%b.BlockSize()
	padded := append(append([]byte(nil), plain...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(b, key).CryptBlocks(out, padded)
	return out, nil
}

func SignaturePath(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	p := u.Path
	if strings.HasPrefix(p, "/algo") {
		p = strings.TrimPrefix(p, "/algo")
	}
	return p, nil
}

func CosySignature(payload, key, date string, body []byte, path string) string {
	sum := md5.Sum([]byte(fmt.Sprintf("%s\n%s\n%s\n%s\n%s", payload, key, date, body, path)))
	return hex.EncodeToString(sum[:])
}
