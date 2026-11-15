.PHONY: build test vet check web desktop notices

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

# Crewbase.app + .dmg (macOS), installer (Windows) or packages (Linux) in
# apps/desktop/build/<os>-<arch>/, with the web UI embedded. MyGo needs no
# cgo; pass PLATFORM=darwin/universal,windows/amd64,linux/amd64 to cross-build.
desktop: web
	CGO_ENABLED=0 go tool mygo build $(if $(PLATFORM),-platform $(PLATFORM)) apps/desktop

# THIRD_PARTY_NOTICES.txt (root and apps/web/public): rerun after changing
# Go or web dependencies; CI fails when it is out of date.
notices:
	go run ./internal/notices
