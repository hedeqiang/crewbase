# Open Artifact: things agents make that you edit

[Open Artifact](https://github.com/JimLiu/open-artifact) turns what an agent makes into a live page instead of an answer you read once: a slide deck, a design canvas, a design system, a whiteboard, a sheet, a data dashboard, a doc or a short animated film. Each is a plain folder of files. You open it, retype it, drag things, comment, export PDF / PPTX / PNG, and what you ask from the page goes back to the conversation that made it.

The plugin is `@crewbase/open-artifact`. Like Open Agent Docs it is set up under Settings › 插件与技能 › 应用 (Open Artifact › 添加), and it is idle until you install the runtime there. Once it is installed, Settings › Open Artifact shows the same controls.

## Setting it up

| Needs | |
|---|---|
| Node.js 22.18 or later | found on `PATH`; the page says what it found |
| The runtime | the npm package `@open-artifact/cli` (no dependencies, about 60 MB), installed with 安装并启用 |

The runtime is downloaded from the npm registry and installed only when its bytes match the sha512 digest npm publishes for that version. It is unpacked under `<data dir>/apps/open-artifact/<version>`; regular files only, nothing outside that folder. 检查更新 asks npm for a newer one; the version in use and the one before it are kept.

Open Artifact runs one daemon per computer, shared with your other agent apps (Claude Code, Codex …), with its library in `~/.agents/open-artifact` (or `OPEN_ARTIFACT_HOME`). Crewbase talks to it through `open-artifact harness --stdio --harness crewbase`. That bridge starts the daemon when needed and holds its token, so Crewbase never reads it.

## What agents get

Three tools, loaded on demand as the `artifact` family (agents find them with `tool_search`), plus the `/open-artifact <type> <what to make>` skill:

| Tool | Actions | Approval category |
|---|---|---|
| `Artifact` | `quickstart` `list` `read` | read |
| | `publish` (make or change) `call` (a type's own actions, e.g. `check`) | write |
| | `open` | none |
| | `delete` `adopt` | delete |
| | `share` | publish (always the person's decision) |
| `ArtifactData` | `get` `list` `query` / `set` `update` `str_replace` `batch` `delete` | read / write |
| `ArtifactComments` | `read` `wait` / `reply` `resolve` `reopen` | read / write |

Rules that keep it inside what the agent may do:
- An artifact's folder is a path argument (`folder`, and `file_path` / `out_file` for data), confined to the workspace like any other.
- Outside host mode an agent only works on artifacts whose folder is in its workspace (or folders it holds, such as its git worktrees). Others can still be opened for the person.
- A read-only agent's conversation may not create, change or remove artifacts; a sub-agent's may not remove them.

## In the conversation

- A turn that makes or changes an artifact shows a card: its type, title and folder. A click opens it in the 预览 panel; the arrow opens it in the browser.
- After the agent's first write, the daemon asks Crewbase to show the page, and it opens in the 预览 panel by itself (loopback pages only).
- Files the agent writes in the artifact's folder with its own file tools are reported to the daemon, so they are not mistaken for your edits.

## From the page back to the conversation

| Who | Arrives as |
|---|---|
| You (the owner): 问 Crewbase, or comments you send | your message: it starts a turn in the conversation that made the artifact |
| A collaborator on a shared page | a notice the agent reads with its next turn, marked as not your words |
| A finished task (export and the like) | a notice |

A conversation from before a restart is still found: Crewbase answers "resumed" and the message lands there.

## Turning it off

The switch (in its dialog under 插件与技能, or Settings › Open Artifact) takes the tools away from every agent and stops the bridge. The daemon keeps running for your other apps, and your artifacts stay where they are.
