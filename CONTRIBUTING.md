# Contributing to Crewbase

Thank you for helping. This page covers how to build, test and send a change.
To report a security problem, follow [SECURITY.md](SECURITY.md) instead of
opening an issue.

## Before you start

- Bugs and small fixes: open a pull request directly. A short issue first
  helps when the cause is not obvious.
- Features, new plugins and changes to the UI: open an issue first so we can
  agree on the shape before you spend time on it. Crewbase is built as a
  kernel plus plugins (see [architecture](docs/04-architecture.md) and the
  [plugin system](docs/05-plugin-system.md)). Most features belong in a
  plugin, not in the kernel.

## Building

You need Go (the version in `go.mod`), Node.js and pnpm.

```sh
make build        # the web UI, then bin/crewd and bin/crew
./bin/crewd       # the daemon on 127.0.0.1:7788 (token in ~/.crewbase/token)
make desktop      # Crewbase.app / installer, with the web UI embedded
```

The web UI is embedded in `crewd`, so rebuild `crewd` after changing
`apps/web`. For the UI alone, `cd apps/web && pnpm dev` serves it with hot
reload against a running daemon.

To try a change without touching your own data, give the daemon a throwaway
root and port:

```sh
./bin/crewd -root /tmp/crew-dev -addr 127.0.0.1:7799
```

## Testing

```sh
make check                        # go vet + go test -race ./...
cd apps/web && pnpm build         # i18n check, type check, build
cd apps/web && pnpm test
```

CI runs the same checks on Linux, macOS and Windows, and cross-compiles for
Windows. Please run them before you push.

## Conventions

- **Go**: `gofmt`, and only on the files you change. Keep packages small and
  doc comments in plain sentences. Add tests next to the code.
- **Web UI**: React and TypeScript. Icons come from `lucide-react`; don't use
  emoji as icons. Every string the user sees goes through `t()`, with the key
  in both `src/i18n.ts` (Chinese) and `src/i18n.en.ts`. The build fails if a
  key is missing. Check light and dark themes and a phone-width window
  (about 390px).
- **Dependencies**: prefer the official SDK of a service, at its newest
  version. Pin exact versions in `apps/web/package.json`.
- **Security**: new tools state their category so the rule engine and
  approvals apply. Never log or show secrets; credentials go through the
  vault.

## Commits and pull requests

- One change per pull request, with a message that says what changed and
  why.
- Describe user-visible changes in the pull request; the maintainers write
  the release notes (`CHANGELOG.md`).
- For UI changes, include screenshots (light and dark).

By contributing you agree that your work is released under the project's
[MIT license](LICENSE).
