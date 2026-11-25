// 代码: what the agent changed in this conversation, and how it becomes a
// merged pull request. One view that follows where the work stands:
//
//   no pull request   the changes (a file opens to its diff; a line takes a
//                     comment for the agent), then 提交 · 推送; the agent
//                     opens the pull request, in the conversation
//   pull request open its state, checks (a failed one → the agent), review
//                     comments (→ the agent), 合并, and what changed since
//   merged / closed   done, and the worktree to clean up
//
// Below, folded to a line each: sub-agents' worktrees and frozen files, the
// repository's other pull requests, the recent commits. GitHub is reached
// through the local GitHub CLI (gh) and its login; Git settings live in
// 设置 › Git. Everything refreshes when the daemon says the repository
// changed (git.changed over SSE).

import { useEffect, useMemo, useState } from "react";
import { Check, Copy, FolderGit2, GitBranch, RefreshCw, Send, Settings2, ShieldCheck, Terminal, TriangleExclamationPoint, X } from "lucide-react";
import { API, type AgentView, type GitCommit, type GitPRSummary, type GitRepoView } from "../api";
import { t } from "../i18n";
import { openSettings, toast, useStore } from "../store";
import { Skeleton } from "./Skeleton";
import { Changes, useDiffs } from "./code/Changes";
import { LogFold, OtherPRsFold, TeamFold } from "./code/Elsewhere";
import { notesMessage, useNotes } from "./code/notes";
import { Done, PullRequest, prOfReview, type ThePR } from "./code/PullRequest";
import { ReviewDetail } from "./code/Review";
import { Ship } from "./code/Ship";
import { errOf, MidPath, tellAgent } from "./code/shared";

export function CodeTab({ agent, session }: { agent: AgentView; session: string }) {
  const tick = useStore((s) => s.gitTick);
  const [view, setView] = useState<GitRepoView | null>(null);
  const [err, setErr] = useState("");
  const [review, setReview] = useState<string | null>(null);
  const [showAll, setShowAll] = useState(false);
  const [n, setN] = useState(0);
  const reload = () => setN((x) => x + 1);
  useEffect(() => {
    let live = true;
    API.gitRepo(agent.id, showAll).then(
      (v) => live && (setView(v), setErr("")),
      (e) => live && setErr(errOf(e)),
    );
    return () => void (live = false);
  }, [agent.id, tick, n, showAll]);

  if (err) return <div className="cdempty"><TriangleExclamationPoint size={26} strokeWidth={1.5} /><b>{t("code.loadFailed")}</b><span className="small errt">{err}</span><button type="button" className="btn sm" onClick={reload}>{t("code.retry")}</button></div>;
  if (!view) return <div className="cd"><div className="cdhead"><Skeleton kind="lines" n={1} /></div><div className="cdpad"><Skeleton kind="rows" n={4} /></div></div>;
  if (!view.repo) return <NoRepo agent={agent} reason={view.reason} onDone={reload} />;
  return <Repo key={view.repo.id} agent={agent} session={session} view={view} n={n} reload={reload} review={review} setReview={setReview} showAll={showAll} setShowAll={setShowAll} />;
}

function Repo({ agent, session, view, n, reload, review, setReview, showAll, setShowAll }: {
  agent: AgentView; session: string; view: GitRepoView; n: number; reload: () => void;
  review: string | null; setReview: (id: string | null) => void; showAll: boolean; setShowAll: (b: boolean) => void;
}) {
  const repo = view.repo!;
  const st = view.status!;
  const branch = st.branch ?? "";
  const base = view.defaultBranch || "main";
  const onBase = !branch || branch === base;
  const github = !!view.origin?.github;
  const diffs = useDiffs(agent.id, undefined, n);
  const notes = useNotes(agent.id);
  const reviews = view.reviews ?? [];
  // this conversation's pull request: a review with one, of the folder's
  // branch or opened in this conversation (a sub-agent's, in its worktree);
  // an open one first, else the newest
  const mine = useMemo(() => {
    const ours = reviews.filter((r) => !!r.pr && !r.author.startsWith("person:github:") && (r.source === branch || (!!r.session && r.session === session)));
    return ours.find((r) => r.pr!.state === "open") ?? ours[0];
  }, [reviews, branch, session]);
  const draftReview = reviews.find((r) => r.source === branch && !r.pr && r.state !== "merged" && r.state !== "closed");
  // a pull request opened another way (gh, the web): found on GitHub by its branch
  const ghOK = github && view.origin?.gh === "ok";
  const found = useBranchPR(agent.id, branch, ghOK && !onBase && !mine, n);
  const pr: ThePR | undefined = mine ? prOfReview(mine) : found ? { number: found.number, url: found.url, state: found.state, draft: found.draft, title: found.title } : undefined;
  const commits = useBranchCommits(agent.id, base, branch, !onBase && !pr, n);
  const others = reviews.filter((r) => r.id !== mine?.id && r.id !== draftReview?.id && r.state !== "merged" && r.state !== "closed");
  const live = pr?.state === "open";
  const wt = mine?.worktree ? view.worktrees?.find((w) => w.id === mine.worktree && w.state === "active") : undefined;
  const nNotes = notes.list.length;

  const sendNotes = async () => {
    const text = notesMessage(notes.list, t("code.notesIntro", { branch: branch || "HEAD" }), (path, line, side) =>
      side === "old" ? t("code.atOldLine", { path, line }) : t("code.atLine", { path, line }));
    if (await tellAgent(agent, session, text)) notes.clear();
  };

  return (
    <div className="cd">
      <header className="cdhead">
        <span className="cdrepo" title={repo.main || repo.root}>
          <FolderGit2 size={14} />
          <b>{repo.name}</b>
        </span>
        <span className={"cdbranch" + (view.protected ? " prot" : "")} title={view.protected ? t("git.protectedBranch") : branch}>
          <GitBranch size={11} />
          <span>{branch || t("git.detached")}</span>
          {view.protected && <ShieldCheck size={11} />}
        </span>
        {(st.ahead > 0 || st.behind > 0) && (
          <span className="cdab small" title={st.upstream}>
            {st.ahead > 0 && <>↑{st.ahead}</>}
            {st.ahead > 0 && st.behind > 0 && " "}
            {st.behind > 0 && <>↓{st.behind}</>}
          </span>
        )}
        <span className="sp" />
        <button className="ix" type="button" aria-label={t("git.refresh")} title={t("git.refresh")} onClick={reload}>
          <RefreshCw size={13} />
        </button>
        <button className="ix" type="button" aria-label={t("code.gitSettings")} title={t("code.gitSettings")} onClick={() => openSettings("git")}>
          <Settings2 size={13} />
        </button>
      </header>
      <div className="cdbody">
        {review ? (
          <ReviewDetail key={review} agent={agent} id={review} onBack={() => (setReview(null), reload())} />
        ) : (
          <>
            {st.merging && <p className="cdbanner small"><TriangleExclamationPoint size={13} />{t("git.merging")}</p>}
            {github && !ghOK && <GHNeeded host={view.origin!.host} state={view.origin?.gh} />}
            {pr && live ? (
              <PullRequest agent={agent} session={session} view={view} pr={pr} worktree={mine && mine.source !== branch ? wt : undefined} diffs={diffs} n={n} notes={notes} reload={reload} />
            ) : (
              <>
                {pr && <Done agent={agent} pr={pr} worktree={wt} compact={!!diffs && diffs.work.length + diffs.staged.length > 0} reload={reload} />}
                {(!pr || (diffs && diffs.work.length + diffs.staged.length > 0)) && (
                  <>
                    <Ship agent={agent} view={view} diffs={diffs} commits={commits} reload={reload} />
                    <Changes agent={agent} diffs={diffs} reload={reload} notes={notes} title={t("code.changes")} />
                  </>
                )}
              </>
            )}
            <div className="cdmore">
              <TeamFold agent={agent} view={view} reviews={others} reload={reload} onReview={setReview} showAll={showAll} setShowAll={setShowAll} />
              {ghOK && <OtherPRsFold agent={agent} mine={pr?.number} onReview={setReview} />}
              <LogFold log={view.log ?? []} />
            </div>
          </>
        )}
      </div>
      {!review && nNotes > 0 && (
        <footer className="cdnotes">
          <span className="cdnotesn">{nNotes}</span>
          <span className="g small">{t("code.notesN", { n: nNotes })}</span>
          <button type="button" className="ix" aria-label={t("code.notesClear")} title={t("code.notesClear")} onClick={notes.clear}>
            <X size={13} />
          </button>
          <button type="button" className="btn sm pri" onClick={() => void sendNotes()}>
            <Send size={12} />
            {t("code.notesSend", { name: agent.name })}
          </button>
        </footer>
      )}
    </div>
  );
}

/** The commits the branch has that the default branch lacks. */
function useBranchCommits(agent: string, base: string, branch: string, on: boolean, n: number): GitCommit[] | null {
  const tick = useStore((s) => s.gitTick);
  const [cs, setCs] = useState<GitCommit[] | null>(null);
  useEffect(() => {
    if (!on) return setCs(null);
    let live = true;
    API.gitLog(agent, undefined, `${base}..HEAD`).then(
      (r) => live && setCs(r.commits ?? []),
      () => live && setCs(null),
    );
    return () => void (live = false);
  }, [agent, base, branch, on, tick, n]);
  return cs;
}

/** A pull request on GitHub for the branch that no local review knows of
 *  (opened with gh or on the web): open first, else the latest closed. */
function useBranchPR(agent: string, branch: string, on: boolean, n: number): GitPRSummary | null {
  const [p, setP] = useState<GitPRSummary | null>(null);
  useEffect(() => {
    if (!on) return setP(null);
    let live = true;
    (async () => {
      const open = await API.gitPRs(agent, "open");
      let x = open.prs.find((q) => q.head === branch && !q.fork);
      if (!x) x = (await API.gitPRs(agent, "closed")).prs.find((q) => q.head === branch && !q.fork);
      if (live) setP(x ?? null);
    })().catch(() => live && setP(null));
    return () => void (live = false);
  }, [agent, branch, on, n]);
  return p;
}

function NoRepo({ agent, reason, onDone }: { agent: AgentView; reason?: string; onDone: () => void }) {
  const [busy, setBusy] = useState(false);
  const noGit = !!reason && /unavailable|not installed|executable/i.test(reason);
  const folder = agent.sharedWorkspace || agent.workspace;
  return (
    <div className="cdempty">
      <span className="cdemptyic"><FolderGit2 size={22} strokeWidth={1.5} /></span>
      <b>{noGit ? t("code.noGit") : t("git.noRepo")}</b>
      <span className="small">{noGit ? t("code.noGitHint") : t("code.noRepoHint")}</span>
      {folder && <MidPath path={folder} className="cdemptyp mono" />}
      {!noGit && (
        <button type="button" className="btn pri" disabled={busy}
          onClick={async () => {
            setBusy(true);
            try {
              await API.gitInit(agent.id);
              toast(t("code.inited"));
              onDone();
            } catch (e) {
              toast(errOf(e));
            } finally {
              setBusy(false);
            }
          }}>
          <FolderGit2 size={14} />
          {t("code.init")}
        </button>
      )}
    </div>
  );
}

/** The panel follows pull requests through the local GitHub CLI: what to do
 *  when it is not installed, or not signed in to the host. */
function GHNeeded({ host, state }: { host: string; state?: string }) {
  const cmd = state === "missing" ? "brew install gh && gh auth login" : host === "github.com" ? "gh auth login" : `gh auth login --hostname ${host}`;
  const [done, setDone] = useState(false);
  return (
    <div className="cdgh">
      <Terminal size={14} />
      <div className="g">
        <b>{t(state === "missing" ? "code.gh.missing" : "code.gh.login", { host })}</b>
        <span className="small">{t("code.gh.why")}</span>
        <span className="cdghcmd">
          <code>{cmd}</code>
          <button
            type="button"
            className="ix"
            aria-label={t("code.gh.copy")}
            title={t("code.gh.copy")}
            onClick={() => {
              void navigator.clipboard?.writeText(cmd);
              setDone(true);
              setTimeout(() => setDone(false), 1200);
            }}
          >
            {done ? <Check size={12} /> : <Copy size={12} />}
          </button>
        </span>
      </div>
    </div>
  );
}
