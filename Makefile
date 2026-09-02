.PHONY: fmt vet test race build check

fmt:
	gofmt -w .

vet:
	go vet ./...

test:
	go test ./...

race:
	go test -race ./...

build:
	go build ./...

check: fmt vet test race build
