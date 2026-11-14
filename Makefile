.PHONY: build test vet check web

build:
	go build -o bin/crewd ./apps/crewd
	go build -o bin/crew ./apps/cli

test:
	go test -race ./...

vet:
	go vet ./...

check: vet test
