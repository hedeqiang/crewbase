.PHONY: build test vet check web

build:
	go build -o bin/ ./apps/...

test:
	go test -race ./...

vet:
	go vet ./...

check: vet test
