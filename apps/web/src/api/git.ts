// Git and GitHub (features/git): repositories, status and diffs, reviews,
// worktrees, pull requests and settings.

// ---------------------------------------------------------------- git (features/git)

export interface GitRepoInfo {
  id: string;
  root: string;
  main: string;
  name: string;
  branch?: string;
  head?: string;
  linked?: boolean;
}
export interface GitFileStatus {
  path: string;
  index: string;
  work: string;
  status: "modified" | "added" | "deleted" | "renamed" | "untracked" | "conflicted" | "typechange";
  staged?: boolean;
  unstaged?: boolean;
}
export interface GitStatus {
  branch?: string;
  head?: string;
  upstream?: string;
  ahead: number;
  behind: number;
  files: GitFileStatus[];
  clean: boolean;
  merging?: boolean;
}
export interface GitLine {
  k: " " | "+" | "-";
  t: string;
  o?: number;
  n?: number;
}
export interface GitHunk {
  id: string;
  header: string;
  oldStart: number;
  oldLines: number;
  newStart: number;
  newLines: number;
  lines: GitLine[];
}
export interface GitFileDiff {
  path: string;
  status: GitFileStatus["status"];
  binary?: boolean;
  added: number;
  removed: number;
  hunks: GitHunk[];
}
export interface GitCommit {
  hash: string;
  short: string;
  subject: string;
  author: string;
  when: string;
  parents?: string[];
}
export interface GitWorktree {
  id: string;
  repo: string;
  path: string;
  branch: string;
  base: string;
  holder: string;
  parent: string;
  child?: string;
  session?: string;
  task?: string;
  origin: "subagent" | "tool" | "person";
  state: "active" | "archived" | "removed" | "merged";
  archive?: { patch?: string; bundle?: string; at: string };
  created: string;
  updated: string;
  status?: GitStatus;
  ahead: number;
  behind: number;
  commits?: GitCommit[];
  review?: string;
  reviewState?: GitReviewState;
  missing?: boolean;
  untrusted?: boolean;
  pr?: number;
  prRepo?: string;
}
export type GitReviewState = "draft" | "open" | "changes_requested" | "approved" | "merged" | "closed";
export type GitSeverity = "blocker" | "major" | "minor" | "nit" | "note";
export interface GitComment {
  id: string;
  author: string;
  path?: string;
  line?: number;
  side?: "new" | "old";
  severity: GitSeverity;
  body: string;
  commit: string;
  round: number;
  resolved?: boolean;
  created: string;
}
export interface GitVerdict {
  reviewer: string;
  decision: "approve" | "request_changes" | "comment";
  body?: string;
  commit: string;
  round: number;
  at: string;
}
export interface GitReview {
  id: string;
  repo: string;
  worktree?: string;
  source: string;
  target: string;
  title: string;
  body?: string;
  author: string;
  origin: string;
  reviewers: string[];
  state: GitReviewState;
  head: string;
  base: string;
  round: number;
  maxRounds: number;
  comments: GitComment[];
  verdicts: GitVerdict[];
  session?: string;
  merge?: { commit: string; strategy: string; by: string; at: string };
  pr?: GitPRLink;
  created: string;
  updated: string;
}
export interface GitMergePreview {
  target: string;
  source: string;
  clean: boolean;
  conflicts?: string[];
  fastForward: boolean;
  upToDate?: boolean;
  ahead: number;
  behind: number;
  files?: string[];
  checkedOutIn?: string;
}
export interface GitLock {
  id: string;
  repo: string;
  path: string;
  start?: number;
  end?: number;
  holder: string;
  reason?: string;
  created: string;
}
export interface GitRepoView {
  repo: GitRepoInfo | null;
  reason?: string;
  status?: GitStatus;
  worktrees?: GitWorktree[];
  reviews?: GitReview[];
  locks?: GitLock[];
  log?: GitCommit[];
  protected?: boolean;
  here?: string;
  /** the branch pull requests go into (main, master …) */
  defaultBranch?: string;
  /** where origin points; null: no origin remote */
  origin?: GitOrigin | null;
}
export interface GitOrigin {
  host: string;
  /** owner/name */
  repo?: string;
  github: boolean;
  /** whether the local GitHub CLI reaches the host: ok, missing (gh not
   *  installed), login (not signed in to it); GitHub repositories only */
  gh?: "ok" | "missing" | "login";
}
export interface GitPRLink {
  number: number;
  url: string;
  host: string;
  repo: string;
  state: "open" | "closed" | "merged" | string;
  draft?: boolean;
  headSha?: string;
  base?: string;
  mergeable?: boolean | null;
  checks?: "success" | "failure" | "pending" | "";
  posted?: string[];
  ciRounds?: number;
  updated: string;
}
export interface GitPRSummary {
  number: number;
  title: string;
  url: string;
  author: string;
  state: string;
  draft?: boolean;
  fork?: boolean;
  head: string;
  base: string;
  headSha: string;
  updated: string;
  review?: string;
  worktree?: string;
  untrusted?: boolean;
}
export interface GitCheck {
  name: string;
  status: string;
  conclusion: string;
  url?: string;
  summary?: string;
  annotations?: string[];
}
export interface GitPRView extends GitPRSummary {
  body: string;
  additions: number;
  deletions: number;
  files: { filename: string; status: string; additions: number; deletions: number }[];
  mergeable: boolean | null;
  mergeableState?: string;
  checks: { state: "success" | "failure" | "pending" | "none"; runs: GitCheck[] };
  reviews: { id: number; user: { login: string }; state: string; body: string; submitted_at: string; html_url: string }[];
  comments: { id: number; user: { login: string }; path: string; line: number; body: string; html_url?: string; created_at?: string }[];
  merged?: boolean;
  me?: string;
}
export interface GitSettings {
  isolation: "shared" | "isolated-worktree" | "worktree-on-write";
  protected: string[];
  maxAutoRounds: number;
  approvals: number;
  autoReview: boolean;
  remotes: { host: string; credential: string; username?: string }[];
  prReviewer?: string;
}
export interface GitCommentInput {
  path?: string;
  line?: number;
  side?: "new" | "old";
  severity?: GitSeverity;
  body: string;
}

export const gq = (o: Record<string, string | undefined>) => {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(o)) if (v) p.set(k, v);
  return p.toString();
};
