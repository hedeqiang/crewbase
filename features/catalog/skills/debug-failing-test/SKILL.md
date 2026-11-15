---
name: debug-failing-test
description: Systematically reproduce, narrow down, explain and fix a failing or flaky test.
---
# Debugging a failing test

Work from evidence. Do not change code until you can say why it fails.

## 1. Reproduce
- Run only the failing test, with verbose output (e.g. `go test -run
  TestName -v ./pkg`, `pytest path::test -x -vv`, `npx vitest run -t name`).
- Record the exact failure: assertion, values, stack trace.
- If it passes alone but fails in the suite, suspect shared state or order;
  if it fails sometimes, run it many times (`-count=50`, `--repeat`) and
  with the race detector or equivalent.

## 2. Narrow down
- Read the test: what does it set up, do, and expect? Is the expectation
  still right, or did the requirement change?
- Find the last good state: `git log` on the files involved, or
  `git bisect run <test command>`.
- Add temporary prints or a debugger breakpoint at the boundary between
  "values still right" and "values wrong"; move the boundary until it is
  one function.

## 3. Explain
Write one or two sentences: "X happens because Y, introduced by Z." If you
cannot write it, keep narrowing.

## 4. Fix
- Fix the cause in the code under test; change the test only when the test
  itself is wrong, and say so.
- Never "fix" by adding sleeps, retries, wider timeouts or skipping, unless
  the timing is genuinely the contract; then explain why.
- Remove temporary prints. Run the test repeatedly, then the whole package.

## 5. Report
Cause, fix, how you verified it, and any related weak spots you noticed.
