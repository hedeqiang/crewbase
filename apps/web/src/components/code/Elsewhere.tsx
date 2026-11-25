// What is not this conversation's work, folded to one quiet line each and
// opening in place: sub-agents' worktrees (with their reviews) and frozen
// files, the repository's other pull requests, and the recent commits.

import { useEffect, useState, type ReactNode } from "react";
import { ArrowRight, ChevronDown, ChevronRight, CircleDot, GitBranch, GitCommitHorizontal, GitPullRequest, History, Lock, Plus, ShieldAlert, Trash2, Users } from "lucide-react";
import { API, type AgentView, type GitCommit, type GitLock, type GitPRSummary, type GitRepoView, type GitReview, type GitWorktree } from "../../api";
import { t, type Key } from "../../i18n";
import { ago } from "../../format";
import { toast, useStore } from "../../store";
import { confirmDialog } from "../Confirm";
import { Skeleton } from "../Skeleton";
import { Changes, useDiffs } from "./Changes";
import { PRIcon } from "./PullRequest";
import { ReviewRow, ReviewState } from "./Review";
import { errOf, useAct, Who } from "./shared";

/** A line that opens what it names in place, below it. */
export function Fold({ icon, title, hint, hot, children, open, onToggle }: {
  icon: ReactNode; title: ReactNode; hint?: ReactNode; hot?: boolean; children: ReactNode; open: boolean; onToggle: () => void;
}) {
  return (
    <section className={"cdfold" + (open ? " open" : "")}>
      <button type="button" className="cdfoldh" aria-expanded={open} onClick={onToggle}>
        <span className="cdfoldi">{icon}</span>
        <span className="cdfoldt">{title}</span>
        {hint && <span className={"cdfoldn small" + (hot ? " hot" : "")}>{hint}</span>}
        <span className="sp" />
        {open ? <ChevronDown size={13} /> : <ChevronRight size={13} />}
      </button>
      {open && <div className="cdfoldb">{children}</div>}
    </section>
  );
}

// ---------------------------------------------------------------- worktrees and locks

export function TeamFold({ agent, view, reviews, reload, onReview, showAll, setShowAll }: {
  agent: AgentView; view: GitRepoView; reviews: GitReview[]; reload: () => void; onReview: (id: string) => void; showAll: boolean; setShowAll: (b: boolean) => void;
}) {
  const [open, setOpen] = useState(false);
  const all = view.worktrees ?? [];
  const active = all.filter((w) => w.state === "active");
  const locks = view.locks ?? [];
  const waiting = reviews.filter((r) => r.state === "open" && r.reviewers.includes("person:owner")).length;
  const holders = new Set(active.map((w) => w.child || w.holder));
  const parts: string[] = [];
  if (active.length) parts.push(t("code.team.wts", { n: holders.size, m: active.length }));
  if (locks.length) parts.push(t("code.team.locks", { n: locks.length }));
  if (!parts.length && reviews.length) parts.push(t("code.team.reviews", { n: reviews.length }));
  const title = parts.length ? parts.join(" · ") : t("code.team.none");
  return (
    <Fold icon={<Users size={13} />} title={title} hint={waiting ? t("code.team.waiting", { n: waiting }) : undefined} hot={waiting > 0} open={open} onToggle={() => setOpen(!open)}>
      <Worktrees agent={agent} view={view} reload={reload} onReview={onReview} />
      {reviews.filter((r) => !r.worktree || !all.some((w) => w.id === r.worktree && w.state === "active")).length > 0 && (
        <div className="cdgroup">
          <div className="cdgrouph small">{t("code.otherReviews")}</div>
          {reviews.filter((r) => !r.worktree || !all.some((w) => w.id === r.worktree && w.state === "active")).map((r) => <ReviewRow key={r.id} r={r} onOpen={() => onReview(r.id)} />)}
        </div>
      )}
      <Locks agent={agent} locks={locks} reload={reload} />
      <label className="gxcheck small cdshowall">
        <input type="checkbox" checked={showAll} onChange={(e) => setShowAll(e.target.checked)} />
        {t("git.showRemoved")}
      </label>
    </Fold>
  );
}

function Worktrees({ agent, view, reload, onReview }: { agent: AgentView; view: GitRepoView; reload: () => void; onReview: (id: string) => void }) {
  const { busy, run } = useAct(reload);
  const list = view.worktrees ?? [];
  const [form, setForm] = useState(false);
  const [task, setTask] = useState("");
  const [base, setBase] = useState("");
  const [peek, setPeek] = useState<string | null>(null);
  const remove = async (w: GitWorktree) => {
    const dirty = (w.status?.files.length ?? 0) > 0 || (w.ahead > 0 && !w.review);
    if (!(await confirmDialog({ text: dirty ? t("git.removeDirtyQ", { branch: w.branch }) : t("git.removeQ", { branch: w.branch }), ok: t("git.remove"), danger: dirty }))) return;
    void run(() => API.gitRemoveWorktree(agent.id, w.id, dirty), t("git.removed"));
  };
  const openReview = (w: GitWorktree) =>
    run(async () => {
      const r = await API.gitOpenReview({ agent: agent.id, worktree: w.id });
      onReview(r.id);
    });
  return (
    <div className="cdgroup">
      <div className="cdgrouph small">
        <span className="g">{t("git.wtIntro")}</span>
        <button type="button" className="gxga" aria-expanded={form} onClick={() => setForm(!form)}>
          <Plus size={11} />
          {t("git.newWorktree")}
        </button>
      </div>
      {form && (
        <div className="cdform">
          <label className="cdfl">
            <span className="small">{t("git.wtTask")}</span>
            <input value={task} autoFocus onChange={(e) => setTask(e.target.value)} placeholder={t("git.wtTaskPh")} />
          </label>
          <label className="cdfl">
            <span className="small">{t("git.wtBase")}</span>
            <input className="mono" value={base} onChange={(e) => setBase(e.target.value)} placeholder={view.status?.branch ?? "main"} />
          </label>
          <div className="gxcrow">
            <span className="sp" />
            <button type="button" className="btn sm ghost" onClick={() => setForm(false)}>{t("common.cancel")}</button>
            <button type="button" className="btn sm pri" disabled={busy}
              onClick={() => void run(async () => (await API.gitSpawn({ agent: agent.id, task, base: base || undefined }), setForm(false), setTask(""), setBase("")), t("git.wtMade"))}>
              {t("git.create")}
            </button>
          </div>
        </div>
      )}
      {list.length === 0 ? (
        <p className="cdnone small">{t("git.noWorktrees")}</p>
      ) : (
        list.map((w) => (
          <article key={w.id} className={"gxwt" + (w.state !== "active" ? " gone" : "")}>
            <div className="gxwth">
              <GitBranch size={13} />
              <b className="mono" title={w.path}>{w.branch}</b>
              {w.pr ? <span className="prtag">#{w.pr}</span> : null}
              {w.untrusted && <span className="prtag fork" title={t("pr.forkNote")}>{t("pr.untrusted")}</span>}
              <span className="sp" />
              <WtState w={w} />
            </div>
            <div className="gxwtm">
              <Who actor={"agent:" + (w.child && w.state === "active" ? w.child : w.holder)} />
              <span className="small muted">{t("git.fromBase", { base: w.base })} · {ago(w.created)}</span>
            </div>
            {w.task && <p className="gxwtt">{w.task.replace(/\[Workspace\][^\n]*\n*/g, "").replace(/[*#`>|]+/g, "").replace(/\s+/g, " ").trim()}</p>}
            {w.state === "active" && (
              <div className="gxwts2 small">
                <span><GitCommitHorizontal size={12} />{t("git.nCommits", { n: w.ahead })}</span>
                <span className={(w.status?.files.length ?? 0) > 0 ? "warn" : ""}><CircleDot size={11} />{t("git.nChanged", { n: w.status?.files.length ?? 0 })}</span>
                {w.behind > 0 && <span>{t("git.behindBase", { n: w.behind, base: w.base })}</span>}
              </div>
            )}
            {w.archive && <div className="small muted gxarch">{t("git.archived")}: <code>{(w.archive.patch || w.archive.bundle || "").split("/").slice(-2).join("/")}</code></div>}
            {w.state === "active" && (
              <div className="gxwta">
                {(w.status?.files.length ?? 0) > 0 && (
                  <button type="button" className="btn sm ghost" aria-expanded={peek === w.id} onClick={() => setPeek(peek === w.id ? null : w.id)}>
                    {peek === w.id ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
                    {t("git.viewChanges")}
                  </button>
                )}
                {w.review ? (
                  <button type="button" className="btn sm ghost" onClick={() => onReview(w.review!)}>
                    <GitPullRequest size={12} />
                    {t("git.openReview")}
                  </button>
                ) : (
                  w.ahead > 0 && (
                    <button type="button" className="btn sm" disabled={busy} onClick={() => void openReview(w)}>
                      <GitPullRequest size={12} />
                      {t("git.startReview")}
                    </button>
                  )
                )}
                <span className="sp" />
                <button type="button" className="ix dng" aria-label={t("git.remove")} title={t("git.remove")} disabled={busy} onClick={() => void remove(w)}>
                  <Trash2 size={13} />
                </button>
              </div>
            )}
            {peek === w.id && <WtChanges agent={agent} w={w} reload={reload} />}
          </article>
        ))
      )}
    </div>
  );
}

function WtChanges({ agent, w, reload }: { agent: AgentView; w: GitWorktree; reload: () => void }) {
  const [n, setN] = useState(0);
  const diffs = useDiffs(agent.id, w.id, n);
  return (
    <div className="cdwtdiff">
      <Changes agent={agent} worktree={w.id} diffs={diffs} reload={() => (setN((x) => x + 1), reload())} title={t("git.nChanged", { n: w.status?.files.length ?? 0 })} />
    </div>
  );
}

function WtState({ w }: { w: GitWorktree }) {
  if (w.state !== "active") return <span className={"gxst wt-" + w.state}>{t(`git.wt.${w.state}` as Key)}</span>;
  if (w.missing) return <span className="gxst wt-removed">{t("git.wt.missing")}</span>;
  const dirty = (w.status?.files.length ?? 0) > 0;
  if (!dirty && w.reviewState) return <ReviewState s={w.reviewState} />;
  return <span className={"gxst " + (dirty ? "wt-dirty" : w.ahead > 0 ? "wt-ready" : "wt-idle")}>{dirty ? t("git.wt.dirty") : w.ahead > 0 ? t("git.wt.ready") : t("git.wt.idle")}</span>;
}

function Locks({ agent, locks, reload }: { agent: AgentView; locks: GitLock[]; reload: () => void }) {
  const { busy, run } = useAct(reload);
  const [form, setForm] = useState(false);
  const [path, setPath] = useState("");
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [reason, setReason] = useState("");
  const add = () =>
    run(async () => {
      await API.gitLock({ agent: agent.id, path, start: from ? Number(from) : undefined, end: to ? Number(to) : from ? Number(from) : undefined, reason });
      setPath("");
      setFrom("");
      setTo("");
      setReason("");
      setForm(false);
    }, t("git.locked"));
  return (
    <div className="cdgroup">
      <div className="cdgrouph small">
        <span className="g">{t("code.locksTitle")}</span>
        <button type="button" className="gxga" aria-expanded={form} onClick={() => setForm(!form)}>
          <Lock size={11} />
          {t("git.lock")}
        </button>
      </div>
      {form && (
        <div className="cdform">
          <p className="small muted cdformi">{t("git.lockIntro")}</p>
          <input className="mono" value={path} autoFocus onChange={(e) => setPath(e.target.value)} placeholder={t("git.lockPathPh")} />
          <div className="gxlockl">
            <input inputMode="numeric" value={from} onChange={(e) => setFrom(e.target.value.replace(/\D/g, ""))} placeholder={t("git.lockFrom")} />
            <span className="muted">–</span>
            <input inputMode="numeric" value={to} onChange={(e) => setTo(e.target.value.replace(/\D/g, ""))} placeholder={t("git.lockTo")} />
          </div>
          <input value={reason} onChange={(e) => setReason(e.target.value)} placeholder={t("git.lockReasonPh")} />
          <div className="gxcrow">
            <span className="sp" />
            <button type="button" className="btn sm ghost" onClick={() => setForm(false)}>{t("common.cancel")}</button>
            <button type="button" className="btn sm pri" disabled={busy || !path.trim()} onClick={() => void add()}>
              <Lock size={12} />
              {t("git.lock")}
            </button>
          </div>
        </div>
      )}
      {locks.length === 0 ? (
        !form && <p className="cdnone small">{t("git.noLocks")}</p>
      ) : (
        locks.map((l) => (
          <div key={l.id} className="gxlock">
            <Lock size={13} />
            <div className="g">
              <span className="mono" title={l.path}>{l.path}{l.start ? `:${l.start}–${l.end}` : ""}</span>
              <span className="small muted">
                {l.start ? t("git.lockLines", { a: l.start, b: l.end ?? l.start }) : t("git.lockFile")}
                {l.reason && <> · {l.reason}</>}
              </span>
            </div>
            <Who actor={l.holder} label={false} />
            <button type="button" className="linkbtn small" disabled={busy} onClick={() => run(() => API.gitUnlock(agent.id, l.id), t("git.unlocked"))}>
              {t("git.unlock")}
            </button>
          </div>
        ))
      )}
    </div>
  );
}

// ---------------------------------------------------------------- other pull requests

export function OtherPRsFold({ agent, mine, onReview }: { agent: AgentView; mine?: number; onReview: (id: string) => void }) {
  const [open, setOpen] = useState(false);
  return (
    <Fold icon={<GitPullRequest size={13} />} title={t("code.otherPRs")} open={open} onToggle={() => setOpen(!open)}>
      <PRList agent={agent} mine={mine} onReview={onReview} />
    </Fold>
  );
}

function PRList({ agent, mine, onReview }: { agent: AgentView; mine?: number; onReview: (id: string) => void }) {
  const tick = useStore((s) => s.gitTick);
  const [state, setState] = useState<"open" | "closed">("open");
  const [list, setList] = useState<GitPRSummary[] | null>(null);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(0);
  useEffect(() => {
    let live = true;
    setErr("");
    setList(null);
    API.gitPRs(agent.id, state).then(
      (r) => live && setList(r.prs.filter((p) => p.number !== mine)),
      (e) => live && (setErr(errOf(e)), setList([])),
    );
    return () => void (live = false);
  }, [agent.id, state, tick, mine]);
  const checkout = async (p: GitPRSummary) => {
    if (p.fork && !(await confirmDialog({ text: t("pr.forkCheckoutQ", { n: p.number }), ok: t("pr.checkout") }))) return;
    setBusy(p.number);
    try {
      const w = await API.gitPRCheckout(agent.id, p.number);
      toast(t("pr.checkedOutTo", { id: w.branch }));
    } catch (e) {
      toast(errOf(e));
    } finally {
      setBusy(0);
    }
  };
  return (
    <div className="cdgroup">
      <div className="cdgrouph">
        <span className="seg sm">
          <button type="button" aria-pressed={state === "open"} onClick={() => setState("open")}>{t("pr.open")}</button>
          <button type="button" aria-pressed={state === "closed"} onClick={() => setState("closed")}>{t("pr.closed")}</button>
        </span>
      </div>
      {err ? (
        <p className="cderr small"><ShieldAlert size={12} /> {err}</p>
      ) : !list ? (
        <Skeleton kind="rows" n={2} />
      ) : list.length === 0 ? (
        <p className="cdnone small">{t("pr.none")}</p>
      ) : (
        list.map((p) => (
          <div key={p.number} className="cdprrow">
            <div className="cdprrowh">
              <PRIcon state={p.state} draft={p.draft} />
              <a href={p.url} target="_blank" rel="noreferrer" className="g" title={p.title}>{p.title}</a>
              <span className="small muted">#{p.number}</span>
            </div>
            <div className="cdprrowm small">
              <span className="mono gxflow">{p.head} <ArrowRight size={11} /> {p.base}</span>
              <span className="muted">{p.author} · {ago(p.updated)}</span>
              {p.fork && <span className="prtag fork">{t("pr.fork")}</span>}
              <span className="sp" />
              {p.review ? (
                <button type="button" className="linkbtn small" onClick={() => onReview(p.review!)}>{t("pr.openReview")}</button>
              ) : p.worktree ? (
                <span className="prtag wt">{t("pr.checkedOut")}</span>
              ) : (
                p.state === "open" && (
                  <button type="button" className="linkbtn small" disabled={busy === p.number} onClick={() => void checkout(p)}>{t("pr.checkout")}</button>
                )
              )}
            </div>
          </div>
        ))
      )}
    </div>
  );
}

// ---------------------------------------------------------------- recent commits

export function LogFold({ log }: { log: GitCommit[] }) {
  const [open, setOpen] = useState(false);
  if (log.length === 0) return null;
  return (
    <Fold icon={<History size={13} />} title={t("git.recent")} hint={log[0].subject} open={open} onToggle={() => setOpen(!open)}>
      <div className="cdgroup">
        {log.map((c) => (
          <div key={c.hash} className="gxlogi">
            <code>{c.short}</code>
            <span className="g">{c.subject}</span>
            <span className="small muted">{ago(c.when)}</span>
          </div>
        ))}
      </div>
    </Fold>
  );
}
