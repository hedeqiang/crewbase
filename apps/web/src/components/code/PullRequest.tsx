// The conversation's pull request, once it is open: what it is and whether
// it can merge (checks, conflicts, reviews) at the top, 合并 as the one
// obvious action; then the checks (a failed one goes to the agent with its
// log), the review comments (each, or all, to the agent), and what the
// agent changed since (推送更新). Merged or closed: a done state.

import { useEffect, useState, type ReactNode } from "react";
import {
  ArrowRight, Check, ExternalLink, GitMerge, GitPullRequest, GitPullRequestClosed, GitPullRequestDraft, LoaderCircle, MessageSquareReply, RefreshCw, Trash2, Upload, Wrench, X,
} from "lucide-react";
import { API, type AgentView, type GitCheck, type GitPRView, type GitRepoView, type GitReview, type GitWorktree } from "../../api";
import { t, type Key } from "../../i18n";
import { ago } from "../../format";
import { toast, useStore } from "../../store";
import { confirmDialog } from "../Confirm";
import { Markdown } from "../Markdown";
import { Skeleton } from "../Skeleton";
import { Changes, changedPaths, useDiffs, type Diffs } from "./Changes";
import { CommitForm } from "./Ship";
import { errOf, tellAgent, useActor, useHanded } from "./shared";
import type { Notes } from "./notes";

/** The pull request the panel shows: a local review's (it can merge from
 *  here), or one found on GitHub for the branch (read here, merged there). */
export interface ThePR {
  number: number;
  url: string;
  state: string;
  draft?: boolean;
  title: string;
  review?: GitReview;
}

export function prOfReview(r: GitReview): ThePR {
  return { number: r.pr!.number, url: r.pr!.url, state: r.pr!.state, draft: r.pr!.draft, title: r.title, review: r };
}

const failed = (c: GitCheck) => ["failure", "timed_out", "cancelled", "action_required", "startup_failure", "error"].includes(c.conclusion);
const checkState = (c: GitCheck) => (c.status !== "completed" ? "pending" : failed(c) ? "failure" : c.conclusion === "success" ? "success" : "neutral");

/** GitHub's view of the pull request: read on open and on each repository
 *  change (a review's link is synced first), every 20s while checks run. */
function usePRView(agent: string, pr: ThePR) {
  const tick = useStore((s) => s.gitTick);
  const [v, setV] = useState<GitPRView | null>(null);
  const [err, setErr] = useState("");
  const [n, setN] = useState(0);
  const [loading, setLoading] = useState(false);
  const rid = pr.review?.id;
  useEffect(() => {
    let live = true;
    setLoading(true);
    (async () => {
      if (rid && n > 0) await API.gitPRSync(agent, rid, pr.number).catch(() => undefined);
      const x = await API.gitPR(agent, pr.number);
      if (live) (setV(x), setErr(""));
    })()
      .catch((e) => live && setErr(errOf(e)))
      .finally(() => live && setLoading(false));
    return () => void (live = false);
  }, [agent, pr.number, rid, tick, n]);
  useEffect(() => {
    if (v?.checks.state !== "pending") return;
    const id = setInterval(() => document.visibilityState === "visible" && setN((x) => x + 1), 20_000);
    return () => clearInterval(id);
  }, [v?.checks.state]);
  return { v, err, loading, refresh: () => setN((x) => x + 1) };
}

export function PRIcon({ state, draft, size = 14 }: { state: string; draft?: boolean; size?: number }) {
  if (state === "merged") return <GitMerge size={size} className="prst merged" />;
  if (state === "closed") return <GitPullRequestClosed size={size} className="prst closed" />;
  if (draft) return <GitPullRequestDraft size={size} className="prst draft" />;
  return <GitPullRequest size={size} className="prst open" />;
}

export function StateTag({ state, draft }: { state: string; draft?: boolean }) {
  const k = state === "open" && draft ? "draft" : state;
  return <span className={"cdtag st-" + k}>{t(`code.st.${k}` as Key)}</span>;
}

// ---------------------------------------------------------------- open

/** worktree: the PR's branch is checked out there (a sub-agent's work),
 *  so what changed since, and the push, are that worktree's. */
export function PullRequest({ agent, session, view, pr, worktree, diffs: folderDiffs, n, notes, reload }: {
  agent: AgentView; session: string; view: GitRepoView; pr: ThePR; worktree?: GitWorktree; diffs: Diffs | null; n: number; notes: Notes; reload: () => void;
}) {
  const { v, err, loading, refresh } = usePRView(agent.id, pr);
  const wtDiffs = useDiffs(agent.id, worktree?.id ?? "-", n, !!worktree);
  const diffs = worktree ? wtDiffs : folderDiffs;
  const st = worktree ? { branch: worktree.branch, ahead: worktree.status?.ahead ?? 0 } : view.status!;
  const branch = st.branch ?? "";
  const base = v?.base ?? pr.review?.target ?? view.defaultBranch ?? "main";
  const nFiles = diffs ? changedPaths(diffs).size : 0;
  return (
    <>
      <div className="cdcard cdpr">
        <div className="cdprh">
          <PRIcon state={pr.state} draft={v?.draft ?? pr.draft} size={16} />
          <a className="cdprn" href={pr.url} target="_blank" rel="noreferrer" title={t("pr.openOnGitHub")}>
            #{pr.number}
            <ExternalLink size={11} />
          </a>
          <StateTag state={pr.state} draft={v?.draft ?? pr.draft} />
          <span className="sp" />
          <button type="button" className="ix" aria-label={t("pr.refresh")} title={t("pr.refresh")} disabled={loading} onClick={refresh}>
            <RefreshCw size={13} className={loading ? "cdspin" : ""} />
          </button>
        </div>
        <h3 className="cdprt">{v?.title ?? pr.title}</h3>
        <div className="cdflow small">
          <span className="mono">{v?.head ?? branch}</span>
          <ArrowRight size={11} />
          <span className="mono">{base}</span>
          {v && (
            <>
              <span className="muted">·</span>
              <span className="fxic">
                {v.additions > 0 && <span className="add">+{v.additions}</span>}
                {v.deletions > 0 && <span className="del">−{v.deletions}</span>}
              </span>
              <span className="muted">{t("code.nFiles", { n: v.files.length })}</span>
            </>
          )}
        </div>
        {err ? (
          <p className="cderr small" role="alert">{err}</p>
        ) : !v ? (
          <div className="cdgate"><Skeleton kind="lines" n={3} /></div>
        ) : (
          <Gate agent={agent} v={v} pr={pr} base={base} onMerged={() => (refresh(), reload())} />
        )}
      </div>

      {v && <Checks agent={agent} session={session} v={v} branch={branch} />}
      {v && <Feedback agent={agent} session={session} v={v} review={pr.review} branch={branch} />}

      {(nFiles > 0 || st.ahead > 0) && (
        <>
          <Changes agent={agent} worktree={worktree?.id} diffs={diffs} reload={reload} notes={worktree ? undefined : notes} title={t("code.since")} />
          <div className="cdupd">
            {nFiles > 0 ? (
              <CommitForm agent={agent} worktree={worktree?.id} branch={branch} protectedBranch={!worktree && !!view.protected} staged={diffs?.staged.length ?? 0} push onDone={reload} />
            ) : (
              <PushUpdate agent={agent} worktree={worktree?.id} branch={branch} ahead={st.ahead} onDone={reload} />
            )}
          </div>
        </>
      )}
    </>
  );
}

function PushUpdate({ agent, worktree, branch, ahead, onDone }: { agent: AgentView; worktree?: string; branch: string; ahead: number; onDone: () => void }) {
  const [busy, setBusy] = useState(false);
  return (
    <div className="cdpushu">
      <span className="small g">{t("code.unpushed", { n: ahead })}</span>
      <button type="button" className="btn sm pri" disabled={busy}
        onClick={async () => {
          if (!(await confirmDialog({ text: t("git.pushQ", { branch }), ok: t("code.pushUpdate") }))) return;
          setBusy(true);
          try {
            await API.gitPush({ agent: agent.id, worktree, branch });
            toast(t("git.pushed", { branch }));
            onDone();
          } catch (e) {
            toast(errOf(e));
          } finally {
            setBusy(false);
          }
        }}>
        <Upload size={12} />
        {t("code.pushUpdate")}
      </button>
    </div>
  );
}

// ---------------------------------------------------------------- can it merge

type Line = { tone: "ok" | "bad" | "wait" | ""; icon: ReactNode; text: string };

function Gate({ agent, v, pr, base, onMerged }: { agent: AgentView; v: GitPRView; pr: ThePR; base: string; onMerged: () => void }) {
  const [method, setMethod] = useState<"squash" | "merge" | "rebase">("squash");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const { name } = useActor();
  const runs = v.checks.runs;
  const nFail = runs.filter((c) => checkState(c) === "failure").length;
  const nWait = runs.filter((c) => checkState(c) === "pending").length;
  // the latest verdict of each GitHub reviewer, and the local review's
  const latest = new Map<string, string>();
  for (const r of [...v.reviews].sort((a, b) => a.submitted_at.localeCompare(b.submitted_at))) {
    if (r.state === "APPROVED" || r.state === "CHANGES_REQUESTED") latest.set(r.user.login, r.state);
    if (r.state === "DISMISSED") latest.delete(r.user.login);
  }
  const asking = [...latest].filter(([, s]) => s === "CHANGES_REQUESTED").map(([u]) => u);
  const approving = [...latest].filter(([, s]) => s === "APPROVED").map(([u]) => u);
  const rv = pr.review;
  if (rv?.state === "changes_requested") {
    for (const x of rv.verdicts) if (x.decision === "request_changes" && x.round === rv.round && !asking.includes(name(x.reviewer))) asking.push(name(x.reviewer));
  }
  const lines: Line[] = [
    runs.length === 0
      ? { tone: "", icon: <Check size={13} />, text: t("code.g.noChecks") }
      : nFail > 0
        ? { tone: "bad", icon: <X size={13} />, text: t("code.g.fail", { n: nFail, all: runs.length }) }
        : nWait > 0
          ? { tone: "wait", icon: <LoaderCircle size={13} className="cdspin" />, text: t("code.g.wait", { n: nWait, all: runs.length }) }
          : { tone: "ok", icon: <Check size={13} />, text: t("code.g.pass", { n: runs.length }) },
    v.mergeable === null
      ? { tone: "wait", icon: <LoaderCircle size={13} className="cdspin" />, text: t("code.g.unknown") }
      : v.mergeable
        ? { tone: "ok", icon: <Check size={13} />, text: t("code.g.clean", { base }) }
        : { tone: "bad", icon: <X size={13} />, text: t("code.g.conflict", { base }) },
    asking.length > 0
      ? { tone: "bad", icon: <X size={13} />, text: t("code.g.changes", { who: asking.join("、") }) }
      : approving.length > 0 || rv?.state === "approved"
        ? { tone: "ok", icon: <Check size={13} />, text: t("code.g.approved", { who: approving.join("、") || t("git.you") }) }
        : { tone: "", icon: <MessageSquareReply size={13} />, text: t("code.g.noReview") },
  ];
  const draft = v.draft;
  const why = !rv
    ? t("code.m.unlinked")
    : draft
      ? t("code.m.draft")
      : nFail > 0
        ? t("code.m.failing")
        : v.mergeable === false
          ? t("code.m.conflict", { base })
          : rv.state === "changes_requested"
            ? t("code.m.changes")
            : "";
  const merge = async () => {
    const label = t(`code.method.${method}` as Key);
    if (!(await confirmDialog({ text: t("code.mergeQ", { n: v.number, base, how: label }), ok: t("code.merge") }))) return;
    setBusy(true);
    setErr("");
    try {
      await API.gitPRMerge(agent.id, rv!.id, v.number, method, true);
      toast(t("pr.mergedOnGitHub"));
      onMerged();
    } catch (e) {
      setErr(errOf(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <ul className="cdgate">
        {lines.map((l, i) => (
          <li key={i} className={l.tone}>
            {l.icon}
            <span>{l.text}</span>
          </li>
        ))}
      </ul>
      {pr.state === "open" && (
        <div className="cdmerge">
          {rv ? (
            <>
              <span className="seg sm cdmethod" role="radiogroup" aria-label={t("git.strategy")}>
                {(["squash", "merge", "rebase"] as const).map((m) => (
                  <button key={m} type="button" role="radio" aria-checked={method === m} aria-pressed={method === m} onClick={() => setMethod(m)}>
                    {t(`code.method.${m}` as Key)}
                  </button>
                ))}
              </span>
              <span className="sp" />
              <button type="button" className="btn sm pri" disabled={busy || !!why} onClick={() => void merge()}>
                {busy ? <LoaderCircle size={12} className="cdspin" /> : <GitMerge size={13} />}
                {t("code.merge")}
              </button>
            </>
          ) : (
            <>
              <span className="sp" />
              <a className="btn sm" href={v.url} target="_blank" rel="noreferrer">
                <ExternalLink size={12} />
                {t("code.mergeThere")}
              </a>
            </>
          )}
        </div>
      )}
      {pr.state === "open" && (why || err) && <p className={"cdwhy small" + (err ? " cderr" : "")} role={err ? "alert" : undefined}>{err || why}</p>}
    </>
  );
}

// ---------------------------------------------------------------- checks

function Checks({ agent, session, v, branch }: { agent: AgentView; session: string; v: GitPRView; branch: string }) {
  const runs = [...v.checks.runs].sort((a, b) => rank(checkState(a)) - rank(checkState(b)));
  const handed = useHanded();
  const [open, setOpen] = useState<Set<string>>(new Set());
  if (runs.length === 0) return null;
  const fix = async (c: GitCheck) => {
    const parts = [t("code.fixCheck", { n: v.number, name: c.name, conclusion: c.conclusion || "failure", branch })];
    if (c.url) parts.push(t("code.fixLink", { url: c.url }));
    if (c.summary) parts.push(t("code.fixSummary") + "\n" + c.summary.trim());
    if (c.annotations?.length) parts.push(t("code.fixNotes") + "\n" + c.annotations.map((a) => "- " + a).join("\n"));
    if (await tellAgent(agent, session, parts.join("\n\n"))) handed.mark("ck:" + v.headSha + ":" + c.name);
  };
  return (
    <section className="cdsec">
      <header className="cdsech">
        <span className="cdsect">{t("pr.checksTitle")}</span>
        <span className="gxn">{runs.length}</span>
      </header>
      <div className="cdchecks">
        {runs.map((c, i) => {
          const s = checkState(c);
          const k = c.name + i;
          const detail = !!(c.summary || c.annotations?.length);
          const isOpen = open.has(k) || (s === "failure" && !open.has("x" + k));
          const done = handed.has("ck:" + v.headSha + ":" + c.name);
          return (
            <div key={k} className={"cdcheck " + s}>
              <div className="cdcheckh">
                <button type="button" className="cdcheckm" disabled={!detail} aria-expanded={detail ? isOpen : undefined}
                  onClick={() => setOpen((o) => {
                    const x = new Set(o);
                    if (s === "failure") x.has("x" + k) ? x.delete("x" + k) : x.add("x" + k);
                    else x.has(k) ? x.delete(k) : x.add(k);
                    return x;
                  })}>
                  <CheckDot s={s} />
                  <span className="g">{c.name}</span>
                  {s !== "failure" && <span className="small muted">{s === "pending" ? t("pr.ck.pending") : s === "success" ? t("pr.ck.success") : c.conclusion}</span>}
                </button>
                {s === "failure" && (done ? (
                  <span className="cddone-chip small"><Check size={11} />{t("code.handed")}</span>
                ) : (
                  <button type="button" className="btn sm cdfix" onClick={() => void fix(c)}>
                    <Wrench size={12} />
                    {t("code.fixIt")}
                  </button>
                ))}
                {c.url && (
                  <a className="ix" href={c.url} target="_blank" rel="noreferrer" aria-label={t("pr.openOnGitHub")} title={t("pr.openOnGitHub")}>
                    <ExternalLink size={12} />
                  </a>
                )}
              </div>
              {detail && isOpen && (
                <div className="cdcheckb">
                  {c.summary && <pre className="whpre">{c.summary}</pre>}
                  {c.annotations?.map((a, j) => <div key={j} className="prann mono small">{a}</div>)}
                </div>
              )}
            </div>
          );
        })}
      </div>
    </section>
  );
}

const rank = (s: string) => (s === "failure" ? 0 : s === "pending" ? 1 : s === "neutral" ? 3 : 2);

function CheckDot({ s }: { s: string }) {
  return (
    <span className={"prck " + s} aria-hidden>
      {s === "success" ? <Check size={11} /> : s === "failure" ? <X size={11} /> : s === "pending" ? <LoaderCircle size={11} className="cdspin" /> : <span className="cddot" />}
    </span>
  );
}

// ---------------------------------------------------------------- review comments

interface Item {
  key: string;
  who: string;
  kind?: string; // a verdict (GitHub review state, or a local severity)
  path?: string;
  line?: number;
  body: string;
  url?: string;
  at?: string;
}

function Feedback({ agent, session, v, review, branch }: { agent: AgentView; session: string; v: GitPRView; review?: GitReview; branch: string }) {
  const { name } = useActor();
  const handed = useHanded();
  const items: Item[] = [
    ...v.reviews
      .filter((r) => r.body.trim() && !r.body.startsWith("**Crewbase review**"))
      .map((r) => ({ key: "ghr:" + r.id, who: r.user.login, kind: r.state, body: r.body, url: r.html_url, at: r.submitted_at })),
    ...v.comments.map((c) => ({ key: "ghc:" + c.id, who: c.user.login, path: c.path, line: c.line, body: c.body, url: c.html_url, at: c.created_at })),
    ...(review?.comments ?? [])
      .filter((c) => !c.resolved && !c.author.startsWith("person:github:"))
      .map((c) => ({ key: "lc:" + c.id, who: name(c.author), kind: "sev:" + c.severity, path: c.path, line: c.line, body: c.body, at: c.created })),
  ];
  if (items.length === 0) return null;
  // nothing to do: an approval; a request a later review by the same person replaced
  const lastBy = new Map<string, string>();
  for (const r of [...v.reviews].sort((a, b) => a.submitted_at.localeCompare(b.submitted_at))) if (r.state !== "COMMENTED") lastBy.set(r.user.login, "ghr:" + r.id);
  const settled = (x: Item) => x.kind === "APPROVED" || x.kind === "DISMISSED" || (x.kind === "CHANGES_REQUESTED" && lastBy.get(x.who) !== x.key);
  const open = items.filter((x) => !handed.has(x.key) && !settled(x));
  const say = (x: Item) => {
    const where = x.path ? (x.line ? t("code.atLine", { path: x.path, line: x.line }) : x.path) : t("code.onPR");
    return `${t("code.whoSaid", { who: x.who, where })}\n   ${x.body.trim().replace(/\n/g, "\n   ")}${x.url ? "\n   " + x.url : ""}`;
  };
  const hand = async (xs: Item[]) => {
    const text = `${t("code.handleIntro", { n: v.number, branch })}\n\n${xs.map((x, i) => `${i + 1}. ${say(x)}`).join("\n\n")}`;
    if (await tellAgent(agent, session, text)) handed.mark(...xs.map((x) => x.key));
  };
  return (
    <section className="cdsec">
      <header className="cdsech">
        <span className="cdsect">{t("code.feedback")}</span>
        <span className="gxn">{items.length}</span>
        <span className="sp" />
        {open.length > 1 && (
          <button type="button" className="btn sm ghost" onClick={() => void hand(open)}>
            <MessageSquareReply size={12} />
            {t("code.handleAll", { n: open.length })}
          </button>
        )}
      </header>
      <div className="cdlist">
        {items.map((x) => (
          <article key={x.key} className={"cdfb" + (handed.has(x.key) || settled(x) ? " done" : "") + ((x.kind === "CHANGES_REQUESTED" || x.kind === "sev:blocker") && !settled(x) ? " ask" : "")}>
            <div className="cdfbh">
              <b>{x.who}</b>
              {x.kind && <Verdict kind={x.kind} />}
              {x.path && <span className="mono small cdfbp" title={x.path}>{x.path.split("/").pop()}{x.line ? `:${x.line}` : ""}</span>}
              <span className="sp" />
              {x.at && <span className="small muted">{ago(x.at)}</span>}
            </div>
            <div className="cdfbb md">
              <Markdown text={x.body} />
            </div>
            <div className="cdfba">
              {x.url && (
                <a className="cdfbl small" href={x.url} target="_blank" rel="noreferrer">
                  <ExternalLink size={11} />
                  {t("code.viewOnGitHub")}
                </a>
              )}
              <span className="sp" />
              {settled(x) ? null : handed.has(x.key) ? (
                <span className="cddone-chip small"><Check size={11} />{t("code.handed")}</span>
              ) : (
                <button type="button" className="btn sm ghost cdhand" onClick={() => void hand([x])}>
                  <MessageSquareReply size={12} />
                  {t("code.handleIt")}
                </button>
              )}
            </div>
          </article>
        ))}
      </div>
    </section>
  );
}

function Verdict({ kind }: { kind: string }) {
  if (kind.startsWith("sev:")) {
    const s = kind.slice(4);
    return <span className={"gxsevtag sev-" + s}>{t(`git.sev.${s}` as Key)}</span>;
  }
  const tone = kind === "APPROVED" ? "ok" : kind === "CHANGES_REQUESTED" ? "red" : "";
  return <span className={"pill sm " + tone}>{t(`pr.rs.${kind}` as Key)}</span>;
}

// ---------------------------------------------------------------- done

export function Done({ agent, pr, worktree, compact, reload }: { agent: AgentView; pr: ThePR; worktree?: GitWorktree; compact?: boolean; reload: () => void }) {
  const merged = pr.state === "merged" || pr.review?.state === "merged";
  const [busy, setBusy] = useState(false);
  const at = pr.review?.merge?.at ?? pr.review?.pr?.updated;
  const base = pr.review?.target ?? "";
  const clean = async () => {
    if (!worktree) return;
    // work left in it is archived (a patch and a bundle) before it goes
    const dirty = (worktree.status?.files.length ?? 0) > 0;
    if (!(await confirmDialog({ text: dirty ? t("git.removeDirtyQ", { branch: worktree.branch }) : t("code.cleanQ", { branch: worktree.branch }), ok: t("git.remove"), danger: dirty }))) return;
    setBusy(true);
    try {
      await API.gitRemoveWorktree(agent.id, worktree.id, dirty);
      toast(t("git.removed"));
    } catch (e) {
      toast(errOf(e));
    } finally {
      setBusy(false);
      reload();
    }
  };
  return (
    <div className={"cdcard cddone" + (merged ? " merged" : " closed") + (compact ? " compact" : "")}>
      <span className="cddoneic">{merged ? <GitMerge size={compact ? 14 : 18} /> : <GitPullRequestClosed size={compact ? 14 : 18} />}</span>
      <div className="cddonet">
        <b>{merged ? (base ? t("code.mergedInto", { base }) : t("code.merged")) : t("code.closed")}</b>
        <span className="small">
          <a href={pr.url} target="_blank" rel="noreferrer">#{pr.number}</a> {pr.title}
          {at && <> · {ago(at)}</>}
        </span>
      </div>
      {!compact && (
        <div className="cddonea">
          <a className="btn sm ghost" href={pr.url} target="_blank" rel="noreferrer">
            <ExternalLink size={12} />
            {t("pr.openOnGitHub")}
          </a>
          {worktree && (
            <button type="button" className="btn sm" disabled={busy} onClick={() => void clean()}>
              <Trash2 size={12} />
              {t("code.cleanWt")}
            </button>
          )}
        </div>
      )}
      {!compact && worktree && <p className="small cddonen">{t("code.cleanHint", { branch: worktree.branch })}</p>}
    </div>
  );
}
