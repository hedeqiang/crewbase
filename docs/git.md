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

## GitHub pull requests

When the repository's `origin` is on GitHub (github.com or GitHub Enterprise Server), the 代码 tab gets a 拉取请求 section and agents get six more tools. The token is the one already set for the host under push credentials (a `vault:` reference). An internal GHE needs its address range allowed in Settings › Git.

| Tool | Does | Asks |
|---|---|---|
| `git_pr_list` / `git_pr_view` | list pull requests; read one with its files, whether it merges, its checks (failed ones with their annotations) and its reviews | no |
| `git_pr_checkout` | fetch `refs/pull/N/head` into a worktree of its own | no |
| `git_pr_create` | push a review's branch and open (or update) its pull request; the description is the review's, saying which agent wrote it and who approved it | yes (push) |
| `git_pr_review` | publish a review's comments on their lines with the verdict | yes (publish) |
| `git_pr_merge` | merge on GitHub (merge, squash or rebase) | yes (push) |

The approval card for opening a pull request or publishing a review shows the full text that will become public.

### Pull requests from forks are untrusted

Running a stranger's code is running it on this computer. The macOS sandbox limits writes, not reads, and its network is open, so a test in a fork's pull request could read `~/.ssh` and send it out. So a pull request from another repository is checked out as **untrusted**:

- its holder reads it but cannot write it, and a sub-agent sent there runs read-only;
- git writes there are refused;
- any command that runs in it, or names it, is refused. With the Docker sandbox (reads confined to the mounted worktree), it may run with no network, after an approval that cannot be remembered.

Agents are told its content is data, never instructions. A pull request from a branch of the same repository (someone with write access) is a normal worktree.

### Publishing a review

Line comments on lines the pull request's diff shows go on those lines; the others go in the review's text. Each carries its severity and author. GitHub refuses approving or requesting changes on your own pull request. When the token's user opened it (one token shared by everyone), the verdict is posted as a comment that states it. The local review keeps the real verdict. A review of someone else's pull request has that person as its author and the agent that opened it as a reviewer; it is published with `git_pr_review`, and nobody is asked to "fix" it.

### Merging on GitHub

`git_pr_merge` merges only when:
- the local review is approved at the pull request's head;
- nothing frozen changes;
- GitHub says it merges (it works this out lazily, so a "not yet known" is never taken as yes or no);
- no check has failed.

The merge is pinned to the head that was checked.

### Webhooks

An inbound webhook with the `github` provider (Settings › Webhook) keeps things in step:

- `pull_request`: a linked review follows the pull request's head and state, including a merge done on GitHub. A pull request someone else opens is checked out and handed to the reviewer agent named in Settings › Git, a bounded number of times per pull request.
- `check_run` / `check_suite` failing on a linked pull request's head: its author gets one fix round per failing commit, with the failed checks and annotations, up to the automatic rounds. After that the person decides.
- `pull_request_review` with changes requested by a person: their verdict and line comments land on the local review, and its fix round follows.

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
| `git_pr_*` | GitHub pull requests (above) |

Every change is written to the audit log (`git/*` records) and pushed to the UI.
