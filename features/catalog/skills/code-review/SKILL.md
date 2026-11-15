---
name: code-review
description: Review a code change (diff, branch or PR) for bugs first and style second, and report actionable findings.
---
# Code review

Goal: find the problems that would hurt after merge, and say each one so
the author can fix it without asking a follow-up question.

## 1. Understand the change
- Get the full diff (`git diff <base>...HEAD`, or the PR's files) and read
  the description or linked issue: what is this change supposed to do?
- For each changed function, open the surrounding code and at least one
  caller. A diff alone hides most bugs.

## 2. Look for defects, in this order
1. **Correctness**: wrong conditions, off-by-one, nil/empty/missing cases,
   error paths that are swallowed or leave state half-updated, wrong units.
2. **Concurrency and lifetimes**: shared state without locks, goroutines or
   promises that outlive their owner, resources not closed on every path.
3. **Security**: unvalidated input reaching a shell, SQL, file path or
   HTML; secrets in logs; permission checks that can be skipped.
4. **Behaviour changes**: public API, config, file formats or defaults that
   change for existing users without a migration or note.
5. **Tests**: does a test fail without this change? Are the edge cases from
   step 1 covered?
6. Only then: naming, duplication, simpler ways to write the same thing.

Run the tests or a quick reproduction when you are not sure; say so when
you could not.

## 3. Report
For each finding give:
- **Where**: `path:line`.
- **What goes wrong**: the concrete input or sequence and its result.
- **Fix**: the smallest change that addresses it.
- **Severity**: blocker / should fix / nit.

Lead with blockers. Skip praise and restating the diff. If you found
nothing serious, say that plainly and list what you checked.
