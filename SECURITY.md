# Security policy

Crewbase runs commands, edits files and calls services for you. It does this
with your credentials, on your machine or a host you control. We treat
security reports as urgent.

## Reporting a vulnerability

Please report privately: do not open a public issue, discussion or pull
request.

- Use [GitHub security advisories](https://github.com/hedeqiang/crewbase/security/advisories/new)
  (Security › Report a vulnerability).
- Say which version you ran (设置 › 通用 shows it) and how it was
  installed (the desktop app, Homebrew, the install script, Docker or a
  source build).
- Give the steps to reproduce, and what an attacker gains.

You will get an acknowledgement within 3 working days and an assessment
within 10. We fix confirmed issues in a patch release, normally the next one.
Reporters are credited in the release notes unless they prefer not to be.

## Supported versions

Only the latest release gets security fixes. The desktop app updates itself;
with Homebrew, run `brew upgrade --cask crewbase`.

## What is in scope

Anything that breaks the guarantees in the
[security model](docs/06-security-model.md). In particular:

- **Escaping the sandbox.** An agent in 只读调研 or 沙箱 mode writes outside
  its workspace, reaches the network when the mode forbids it, or changes a
  protected path. The sandbox backends are `sandbox-seatbelt` (macOS),
  `sandbox-namespace` (Linux, bubblewrap) and `sandbox-docker`.
- **Getting past approvals.** An action that needs approval (a push, a
  publish, a payment, a command a rule asks about) runs without one, or is
  approved from a place that may not approve it.
- **The gateway.** Unauthenticated access to the API on `127.0.0.1:7788`, a
  paired device or relay doing more than its tier allows, or token leakage.
- **Credentials.** Secrets from the vault (保管库), provider keys or OAuth
  tokens that end up in logs, session files, prompts, the UI, or another
  agent's or team's reach.
- **Content from outside.** Prompt injection from web pages, files, MCP
  servers, webhooks or IM messages that leads to one of the above. (An
  injection that only changes what an agent says is not, by itself, a
  vulnerability.)
- **Rendering.** Script execution from Markdown, Mermaid, previews or files
  shown in the UI.
- **Updates and supply chain.** Update packages that are not verified
  against the release signature, or dependencies with known vulnerabilities
  that our code reaches.

## What is not in scope

- Anything an agent does in 完全访问 (host) mode, which runs without
  isolation by design.
- Anything you have explicitly approved.
- Issues that need an attacker who already controls your user account or
  the machine.
- Third-party services, models, MCP servers and plugins themselves. Report
  those to their maintainers. How Crewbase isolates them is in scope.
