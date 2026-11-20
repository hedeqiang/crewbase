# Open Agent Docs

[Open Agent Docs](https://github.com/kadaliao/open-agent-docs) is a self-hosted document server where people and agents
write pages together: block-level edits, comments, databases, version history, and an MCP endpoint. Crewbase connects to
it as an app (`features/agentdocs`), and can run it for you.

## 一键启动 (Crewbase runs it)

On macOS and Linux (the platforms upstream publishes builds for) the dialog's 一键启动 asks for the email and password the
person will sign in to the docs with, then:

1. downloads the pinned release (`agentdocs.Release`, v0.2.0) from GitHub and checks it against the SHA-256 written in
   `local.go` (taken from the release's SHA256SUMS when it was pinned); a different file is refused and nothing is installed;
2. unpacks only the server binary to `<root>/apps/open-agent-docs/<version>/` and runs it on `127.0.0.1` (port 8848, or a
   free one) with its data in `<root>/apps/open-agent-docs/data` and its log in `server.log` there; the first start creates
   the person's account;
3. signs in once with that account to create Crewbase's admin agent (`Crewbase`, or `Crewbase-2` … when an earlier one
   exists), then connects as below. The password is not kept; the admin token is in the vault.

Crewbase restarts the server if it exits, starts it again when Crewbase starts, and stops it on quit. Disconnecting stops it
too; its data stays, so starting again asks for the account used before. Upgrading means pinning a new release (version
and hashes) in a Crewbase release.

## Connecting an existing server

设置 › 插件与技能 › 应用 › Open Agent Docs. The person gives:

- the server's address (`http://127.0.0.1:8848`; a trailing `/mcp` is accepted);
- an **admin** agent token (`oad_…`), made in the docs web app under Agents with the scope `admin`.

The token is checked (`GET /api/v1/me`, then listing the workspace's agents, which only admin tokens may do) and kept in
the vault as `vault:crewbase/agentdocs-admin`. It is used only to create and revoke identities.

## One identity per agent

For every Crewbase agent (plain chats excepted) the connector creates an agent on the docs server
(`POST /workspaces/{ws}/agents`, scope `write`) named after the Crewbase agent (spaces and mention characters become `-`;
a taken name gets `-2`, `-3` …). Its token goes into the vault, and an MCP server `docs-<hash>` with the scope of that one
agent points at `<url>/mcp` with it. So pages and comments say who wrote them, and each agent sees only its own
`docs-<hash>__…` tools.

New agents get an identity when they are created; deleted agents lose it (revoked on the server). A revoked or lost token
is replaced on the next sync (at start, on 重新同步, after a settings change).

## Comments become tasks

With 评论 @ 助理时让它处理 on (the default), the connector long-polls each identity's notifications
(`GET /api/v1/agent/notifications?after=…&wait=30`). Each one — someone @mentioned the agent, replied in a thread it is
in, or commented on a block it wrote — becomes an input for that agent in its own conversation (`From: agentdocs`),
quoting the comment, the passage and the server's ready-made prompt, and naming the tools to read the page and reply in
the thread. It is acknowledged only once it is queued, so nothing is lost when Crewbase is down; the last handled id is
kept in `agentdocs.json`.

Long polling needs no inbound address, unlike the server's webhooks (which only reach public addresses by default).

## Disconnecting

Revokes every identity on the server (what they wrote stays), removes the MCP servers and deletes the tokens from the
vault.
