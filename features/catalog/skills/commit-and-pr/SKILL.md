---
name: commit-and-pr
description: Write a clear git commit message or pull request description from the staged diff or a branch.
---
# Commit messages and PR descriptions

## Gather
- Commit: `git diff --staged` (and `git status` for new files).
- PR: `git log --oneline <base>..HEAD` and `git diff <base>...HEAD --stat`,
  then read the important hunks.
- Check the repository's own convention first: `git log -20 --format=%s`
  and any CONTRIBUTING file. Follow it over the defaults below.

## Commit message
```
<type>(<area>): <what changed, imperative, under ~70 chars>

<why the change was needed, and anything a reader of `git log` must know:
behaviour changes, migrations, follow-ups. Wrap at ~72 columns.>
```
- type: feat, fix, refactor, perf, docs, test, build, chore.
- Subject says what the code now does ("fix(auth): refresh expired tokens
  before retrying"), not what you did ("updated auth.go").
- One logical change per commit; if the diff mixes two, suggest splitting.
- Never include secrets, internal URLs or personal data from the diff.

## PR description
1. **Summary**: one or two sentences: the problem and the outcome.
2. **Changes**: short bullets grouped by area; mention anything removed.
3. **How to test**: exact commands or clicks, and what to expect.
4. **Risk / rollout**: migrations, flags, config, backwards compatibility.
5. **Screenshots** for UI changes, **links** to issues (`Fixes #123`).

Keep it skimmable: a reviewer should know in 30 seconds what to look at.
