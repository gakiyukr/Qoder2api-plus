.PHONY: fmt vet test race build check release clean

# 版本與提交由環境注入；本機未設定時退回 dev。
VERSION ?= dev
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

fmt:
	gofmt -w .

vet:
	go vet ./...

test:
	go test ./...

race:
	go test -race ./...

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o qoder-proxy ./cmd/qoder-proxy

# 交叉編譯 Linux 雙架構靜態產物到 dist/（CGO 關閉；本專案零 C 依賴）。
# 本程序只在 Linux 運行，故不產出 darwin / windows 產物。
release: clean
	@mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/qoder-proxy-linux-amd64 ./cmd/qoder-proxy
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/qoder-proxy-linux-arm64 ./cmd/qoder-proxy
	@ls -la dist/

clean:
	rm -rf dist qoder-proxy

check: fmt vet test race build
