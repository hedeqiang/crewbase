# Git: agents side by side, reviewed before it lands

When an agent works in a Git repository (its project folder is one), Crewbase keeps the agents that change code apart and has them review each other's work before it reaches a branch that matters. The plugins are `@crewbase/vcs-gitcli` (the `vcs` capability over the git binary) and `@crewbase/git`. The 代码 tab of the right panel shows all of it.

## Worktrees

A sub-agent that may write files gets its own branch and working tree, outside the project folder:

```
<data dir>/worktrees/<repo id>/<worktree id>     branch crew/<sub-agent>-<id>, from the parent's branch
```

So two sub-agents working in parallel never share a folder or an index, and neither touches the parent's checkout. Settings › 代码 (the gear in the tab) picks the policy:

| Policy | Who gets a worktree |
|---|---|
| `worktree-on-write` (default) | sub-agents that may write; read-only ones look at the parent's folder |
| `isolated-worktree` | every sub-agent |
| `shared` | none: sub-agents work in the parent's folder |

When the sub-agent's task ends:
- **Nothing in the worktree:** it is removed, branch and all.
- **Holds work:** it passes to the parent, which is told where the work is and how to diff, commit, review or remove it.
- **A fix round:** use `delegate(task, worktree: "wt…")` to continue in the same worktree.

A worktree with uncommitted changes, or commits that were not merged, is never removed silently. Removing it needs `discard`, and the work is archived first under `worktrees/.archive/<id>/`:
- uncommitted changes, untracked files included, as a `.patch`
- the branch's commits as a `.bundle`

## Freeze locks

`git_lock` (or 锁定 in the tab) freezes a file, or a range of its lines, for everyone except:
- its holder
- agents working for the same agent as the holder, such as its sub-agents

What a lock refuses (with `GIT_LOCK_FROZEN`):
- A write, edit or delete through the file tools that touches the frozen lines. The write-intent chain now carries the lines a change touches.
- A merge whose hunks touch them.

A person can lift any lock. Otherwise only the holder's side can.

## Code review

`git_review_open` opens a review of a branch against its target, usually the worktree's base. A review moves through these states:

```
draft → open → changes_requested → open … → approved → merged
                                                     ↘ closed
```

Rules:
- **Nobody reviews their own work.** A sub-agent counts as the agent it works for, so that agent cannot approve its sub-agent's branch either (`GIT_SELF_APPROVAL_DENIED`). A person can always review agents' work.
- **Comments sit on lines.** Each has a severity: blocker, major, minor, nit or note. An unresolved blocker keeps the review at `changes_requested`.
- **Agent reviewers are asked at once.** When changes are requested, the author's side gets the comments and a fix round. Rounds are bounded (3 by default). When they run out, the review waits for a person.
- **New commits make earlier approvals stale.** Only an approved review merges, and only at the commit that was approved (`GIT_REVIEW_STALE`).

## Merging and pushing

Merges are previewed and recorded without touching any working tree:
- `git merge-tree --write-tree` computes the result.
- `commit-tree` records it, and a compare-and-swap moves the branch.
- If the target is checked out in a clean folder, that folder is brought along.
- Strategies: merge commit (default), squash, fast-forward only.

Conflicts are resolved in the worktree with `git_conflict_resolve`:
- `start` merges the target in and returns the conflict blocks.
- Fix each file, then `resolve` it, choosing ours, theirs or manual.
- `conclude` commits the merge.

Pushes always ask the person. Committing or merging into a protected branch also asks; protected branches are `main`, `master` and `release/*` by default. A push uses the credential configured for the remote's host. That is a vault reference: Crewbase resolves it for that one command and hands it to git through a one-off askpass. A remote on another host never sees it, and the token is scrubbed from all output.

## What keeps repository content from running code

Crewbase runs git in its own process for these tools. Every call:
- points hooks at an empty folder
- switches off fsmonitor, external diff and textconv drivers, signing, and the file:// and ext transports

Settings that could still run programs (filter and merge drivers) live in `.git/config`. In sandbox mode an agent cannot write it, nor any of these (`agent.GitControlNames`):
- `.git/config.worktree`
- `.git/hooks`
- `.git/worktrees`
- `.git/info`
- `.gitmodules`
- a linked worktree's `.git` file

The command sandbox and the file tools both enforce this. In a worktree, bash can read the repository but cannot write to its `.git`, so agents commit with `git_commit`.

## Tools

The tools load on demand as the `git` family:

| Tool | Does |
|---|---|
| `git_status` | branch, ahead/behind, changes, the repository's worktrees and open reviews |
| `git_diff` | a diff with stable hunk ids (work, staged, head, range, or a review's) |
| `git_hunk_apply` | stage, unstage or discard one hunk |
| `git_commit` | commit everything or what is staged |
| `git_worktree_spawn` / `git_worktree_remove` | make or remove a worktree |
| `git_review_open` / `git_review_submit` | open a review or ask for another round; comment and give a verdict |
| `git_merge` | preview or merge an approved review |
| `git_conflict_resolve` | start, resolve, conclude or abort a merge in a worktree |
| `git_push` | push a branch (always asks) |
| `git_lock` | freeze lines or a file, or lift a lock |

Every change is written to the audit log (`git/*` records) and pushed to the UI.
