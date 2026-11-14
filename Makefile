.PHONY: build test vet check web

build: web
	go build -o bin/crewd ./apps/crewd
	go build -o bin/crew ./apps/cli

test:
	go test -race ./...

vet:
	go vet ./...

check: vet test

web:
	cd apps/web && (pnpm install --frozen-lockfile || pnpm install) && pnpm build
