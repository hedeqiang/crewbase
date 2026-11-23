// 代码 › 拉取请求: the repository's GitHub pull requests (features/git/pr.go),
// and a review's pull request (open it, publish the review, merge it).

import { useEffect, useState } from "react";
import {
  ArrowLeft, ArrowRight, Check, CircleDot, ExternalLink, GitMerge, GitPullRequest, GitPullRequestDraft, Loader, RefreshCw, ShieldAlert, Upload, X,
} from "lucide-react";
import { API, type AgentView, type GitCheck, type GitPRSummary, type GitPRView, type GitReview } from "../api";
import { t, type Key } from "../i18n";
import { ago } from "../format";
import { toast, useStore } from "../store";
import { confirmDialog } from "./Confirm";
import { Markdown } from "./Markdown";
import { Skeleton } from "./Skeleton";

const errOf = (e: any) => e?.message ?? String(e);

function PRIcon({ p }: { p: { state: string; draft?: boolean } }) {
  if (p.state === "merged") return <GitMerge size={14} className="prst merged" />;
  if (p.state === "closed") return <GitPullRequest size={14} className="prst closed" />;
  if (p.draft) return <GitPullRequestDraft size={14} className="prst draft" />;
  return <GitPullRequest size={14} className="prst open" />;
}

function ChecksDot({ state }: { state?: string }) {
  if (!state || state === "none") return null;
  const icon = state === "success" ? <Check size={11} /> : state === "failure" ? <X size={11} /> : <Loader size={11} />;
  return (
    <span className={"prck " + state} title={t(`pr.ck.${state}` as Key)}>
      {icon}
    </span>
  );
}

// ---------------------------------------------------------------- list

export function PRList({ agent, onOpenReview, onOpenWorktree }: { agent: AgentView; onOpenReview: (id: string) => void; onOpenWorktree: (id: string) => void }) {
  const tick = useStore((s) => s.gitTick);
  const [state, setState] = useState<"open" | "closed">("open");
  const [list, setList] = useState<GitPRSummary[] | null>(null);
  const [err, setErr] = useState("");
  const [open, setOpen] = useState<number | null>(null);
  const [n, setN] = useState(0);
  useEffect(() => {
    let live = true;
    setErr("");
    API.gitPRs(agent.id, state).then(
      (r) => live && setList(r.prs),
      (e) => live && (setErr(errOf(e)), setList([])),
    );
    return () => void (live = false);
  }, [agent.id, state, tick, n]);
  if (open !== null)
    return <PRDetail agent={agent} n={open} onBack={() => (setOpen(null), setN((x) => x + 1))} onOpenReview={onOpenReview} onOpenWorktree={onOpenWorktree} />;
  return (
    <div className="prs">
      <div className="gxbar">
        <span className="seg gxmini">
          <button type="button" aria-pressed={state === "open"} onClick={() => setState("open")}>{t("pr.open")}</button>
          <button type="button" aria-pressed={state === "closed"} onClick={() => setState("closed")}>{t("pr.closed")}</button>
        </span>
        <span className="sp" />
        <button className="ix" type="button" aria-label={t("git.refresh")} title={t("git.refresh")} onClick={() => setN((x) => x + 1)}>
          <RefreshCw size={13} />
        </button>
      </div>
      {err ? (
        <div className="gxempty small">
          <ShieldAlert size={22} strokeWidth={1.5} />
          <span className="errt">{err}</span>
          <span className="muted">{t("pr.setupHint")}</span>
        </div>
      ) : !list ? (
        <div className="gxpad"><Skeleton kind="rows" /></div>
      ) : list.length === 0 ? (
        <div className="gxempty small">
          <GitPullRequest size={22} strokeWidth={1.5} />
          <span>{t("pr.none")}</span>
        </div>
      ) : (
        list.map((p) => (
          <button key={p.number} type="button" className="gxrv prrow" onClick={() => setOpen(p.number)}>
            <div className="gxrvh">
              <PRIcon p={p} />
              <b className="g">{p.title}</b>
              <span className="small muted">#{p.number}</span>
            </div>
            <div className="gxrvm small">
              <span className="mono gxflow">{p.head} <ArrowRight size={11} /> {p.base}</span>
              <span className="sp" />
              {p.fork && <span className="prtag fork">{t("pr.fork")}</span>}
              {p.review && <span className="prtag rv">{t("pr.linked")}</span>}
              {p.worktree && <span className="prtag wt">{t("pr.checkedOut")}</span>}
            </div>
            <div className="gxrvp small">
              <span className="muted">{p.author}</span>
              <span className="sp" />
              <span className="muted">{ago(p.updated)}</span>
            </div>
          </button>
        ))
      )}
    </div>
  );
}

// ---------------------------------------------------------------- one PR

function CheckRow({ c }: { c: GitCheck }) {
  const failed = ["failure", "timed_out", "cancelled", "action_required", "error"].includes(c.conclusion);
  const [open, setOpen] = useState(failed);
  const st = c.status !== "completed" ? "pending" : failed ? "failure" : c.conclusion === "success" ? "success" : "neutral";
  return (
    <div className={"prcheck " + st}>
      <button type="button" className="prcheckh" onClick={() => setOpen(!open)} aria-expanded={open}>
        <ChecksDot state={st === "neutral" ? undefined : st} />
        <span className="g">{c.name}</span>
        <span className="small muted">{c.status !== "completed" ? t("pr.ck.pending") : c.conclusion}</span>
        {c.url && (
          <a href={c.url} target="_blank" rel="noreferrer" className="ix" onClick={(e) => e.stopPropagation()} aria-label={t("pr.openOnGitHub")}>
            <ExternalLink size={12} />
          </a>
        )}
      </button>
      {open && (c.summary || c.annotations?.length) ? (
        <div className="prcheckb">
          {c.summary && <pre className="whpre">{c.summary}</pre>}
          {c.annotations?.map((a, i) => (
            <div key={i} className="prann mono small">{a}</div>
          ))}
        </div>
      ) : null}
    </div>
  );
}

function PRDetail({ agent, n, onBack, onOpenReview, onOpenWorktree }: { agent: AgentView; n: number; onBack: () => void; onOpenReview: (id: string) => void; onOpenWorktree: (id: string) => void }) {
  const tick = useStore((s) => s.gitTick);
  const [v, setV] = useState<GitPRView | null>(null);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [k, setK] = useState(0);
  useEffect(() => {
    let live = true;
    API.gitPR(agent.id, n).then(
      (x) => live && setV(x),
      (e) => live && setErr(errOf(e)),
    );
    return () => void (live = false);
  }, [agent.id, n, tick, k]);
  if (err) return <div className="gxempty small errt">{err}</div>;
  if (!v) return <div className="gxpad"><Skeleton kind="code" /></div>;
  const checkout = async () => {
    if (v.fork && !(await confirmDialog({ text: t("pr.forkCheckoutQ", { n: v.number }), ok: t("pr.checkout") }))) return;
    setBusy(true);
    try {
      const w = await API.gitPRCheckout(agent.id, v.number);
      toast(t("pr.checkedOutTo", { id: w.id }));
      setK((x) => x + 1);
    } catch (e) {
      toast(errOf(e));
    } finally {
      setBusy(false);
    }
  };
  const merge = v.merged ? "merged" : v.mergeable === null ? "unknown" : v.mergeable ? "clean" : "conflict";
  return (
    <div className="gxrd prd">
      <div className="gxrdtop">
        <button type="button" className="btn sm ghost" onClick={onBack}>
          <ArrowLeft size={12} />
          {t("git.sec.prs")}
        </button>
        <span className="sp" />
        <a className="btn sm ghost" href={v.url} target="_blank" rel="noreferrer">
          <ExternalLink size={12} />
          {t("pr.openOnGitHub")}
        </a>
      </div>
      <h3 className="gxrdt">
        <PRIcon p={v} /> {v.title} <span className="muted">#{v.number}</span>
      </h3>
      <div className="gxrdm">
        <span className="mono small gxflow">{v.head} <ArrowRight size={11} /> {v.base}</span>
        <span className="small muted">{v.author}</span>
        <span className="fxic small">
          <span className="add">+{v.additions}</span>
          <span className="del">−{v.deletions}</span>
        </span>
        {v.fork && <span className="prtag fork">{t("pr.fork")}</span>}
      </div>
      {v.fork && (
        <div className="gxself small prwarn">
          <ShieldAlert size={13} />
          {t("pr.forkNote")}
        </div>
      )}

      <div className={"gxgate" + (v.checks.state === "failure" || merge === "conflict" ? " bad" : merge === "clean" && v.checks.state !== "pending" ? " ok" : "")}>
        <ul>
          <li className={merge === "clean" || merge === "merged" ? "ok" : merge === "conflict" ? "bad" : ""}>
            {merge === "clean" || merge === "merged" ? <Check size={13} /> : merge === "conflict" ? <X size={13} /> : <CircleDot size={13} />}
            {t(`pr.merge.${merge}` as Key, { base: v.base })}
          </li>
          <li className={v.checks.state === "success" ? "ok" : v.checks.state === "failure" ? "bad" : ""}>
            {v.checks.state === "success" ? <Check size={13} /> : v.checks.state === "failure" ? <X size={13} /> : <CircleDot size={13} />}
            {t(`pr.checks.${v.checks.state}` as Key)}
          </li>
        </ul>
        <div className="gxgatea">
          {v.review && (
            <button type="button" className="btn sm ghost" onClick={() => onOpenReview(v.review!)}>
              <GitPullRequest size={12} />
              {t("pr.openReview")}
            </button>
          )}
          {v.worktree ? (
            <button type="button" className="btn sm" onClick={() => onOpenWorktree(v.worktree!)}>
              {t("pr.viewWorktree")}
            </button>
          ) : (
            v.state === "open" && !v.review && (
              <button type="button" className="btn sm pri" disabled={busy} onClick={() => void checkout()}>
                {t("pr.checkout")}
              </button>
            )
          )}
        </div>
      </div>

      {v.checks.runs.length > 0 && (
        <section className="prsec">
          <div className="gxgh">
            <span className="small">{t("pr.checksTitle")}</span>
            <span className="gxn">{v.checks.runs.length}</span>
          </div>
          {v.checks.runs.map((c, i) => <CheckRow key={i} c={c} />)}
        </section>
      )}

      {v.body && (
        <section className="prsec">
          <div className="gxgh">
            <span className="small">{t("pr.description")}</span>
          </div>
          <div className="gxrdb prbody">
            <div className="md">
              <Markdown text={v.body} />
            </div>
          </div>
        </section>
      )}

      <section className="prsec">
        <div className="gxgh">
          <span className="small">{t("git.filesChanged")}</span>
          <span className="gxn">{v.files.length}</span>
        </div>
        {v.files.map((f) => (
          <div key={f.filename} className="prfile small">
            <span className={"fxst st-" + (f.status === "removed" ? "deleted" : f.status === "added" ? "added" : "modified")}>{f.status === "removed" ? "D" : f.status === "added" ? "A" : "M"}</span>
            <span className="mono g">{f.filename}</span>
            <span className="fxic">
              {f.additions > 0 && <span className="add">+{f.additions}</span>}
              {f.deletions > 0 && <span className="del">−{f.deletions}</span>}
            </span>
          </div>
        ))}
      </section>

      {(v.reviews.length > 0 || v.comments.length > 0) && (
        <section className="prsec">
          <div className="gxgh">
            <span className="small">{t("pr.ghReviews")}</span>
          </div>
          {v.reviews
            .filter((r) => r.body || r.state !== "COMMENTED")
            .map((r) => (
              <div key={r.id} className={"gxtli v-" + (r.state === "APPROVED" ? "approve" : r.state === "CHANGES_REQUESTED" ? "request_changes" : "comment")}>
                <b className="small">{r.user.login}</b>
                <span className="small muted">{t(`pr.rs.${r.state}` as Key)} · {ago(r.submitted_at)}</span>
                {r.body && (
                  <div className="gxrdb">
                    <div className="md">
                      <Markdown text={r.body} />
                    </div>
                  </div>
                )}
              </div>
            ))}
          {v.comments.slice(0, 30).map((c) => (
            <div key={c.id} className="gxcm">
              <div className="gxcmh">
                <b className="small">{c.user.login}</b>
                <span className="mono small muted">{c.path}:{c.line}</span>
              </div>
              <div className="gxcmb md">
                <Markdown text={c.body} />
              </div>
            </div>
          ))}
        </section>
      )}
    </div>
  );
}

// ---------------------------------------------------------------- a review's PR

/** In a review: open its pull request, or show it with publish / merge. */
export function ReviewPR({ agent, r, onChanged }: { agent: AgentView; r: GitReview; onChanged: () => void }) {
  const [busy, setBusy] = useState(false);
  const [draft, setDraft] = useState(false);
  const [method, setMethod] = useState("squash");
  const run = async (f: () => Promise<unknown>, ok?: string) => {
    setBusy(true);
    try {
      await f();
      if (ok) toast(ok);
    } catch (e) {
      toast(errOf(e));
    } finally {
      setBusy(false);
      onChanged();
    }
  };
  const live = r.state !== "merged" && r.state !== "closed";
  const pr = r.pr;
  if (!pr) {
    if (!live) return null;
    return (
      <div className="prbox">
        <GitPullRequest size={14} className="muted" />
        <span className="small g">{t("pr.noneYet", { target: r.target })}</span>
        <label className="gxcheck small">
          <input type="checkbox" checked={draft} onChange={(e) => setDraft(e.target.checked)} />
          {t("pr.draft")}
        </label>
        <button type="button" className="btn sm" disabled={busy}
          onClick={async () => (await confirmDialog({ text: t("pr.createQ", { source: r.source, target: r.target }), ok: t("pr.create") })) && run(() => API.gitPRCreate(agent.id, r.id, { draft }), t("pr.created"))}>
          <Upload size={12} />
          {t("pr.create")}
        </button>
      </div>
    );
  }
  const unposted = r.comments.filter((c) => !c.resolved && !(pr.posted ?? []).includes(c.id)).length;
  return (
    <div className="prbox linked">
      <div className="prboxh">
        <PRIcon p={{ state: pr.state, draft: pr.draft }} />
        <a href={pr.url} target="_blank" rel="noreferrer" className="small">
          {pr.repo}#{pr.number}
        </a>
        <span className={"prtag st-" + pr.state}>{t(`pr.state.${pr.state}` as Key)}</span>
        <ChecksDot state={pr.checks} />
        {pr.ciRounds ? <span className="small muted">{t("pr.ciRounds", { n: pr.ciRounds })}</span> : null}
        <span className="sp" />
        <button className="ix" type="button" aria-label={t("git.refresh")} title={t("pr.refresh")} disabled={busy} onClick={() => void run(() => API.gitPRSync(agent.id, r.id, pr.number))}>
          <RefreshCw size={12} />
        </button>
      </div>
      {live && pr.state === "open" && (
        <div className="prboxa">
          <button type="button" className="btn sm ghost" disabled={busy}
            onClick={async () => (await confirmDialog({ text: t("pr.publishQ", { n: pr.number, c: unposted }), ok: t("pr.publish") })) && run(async () => {
              const x = await API.gitPRReview(agent.id, r.id, pr.number);
              toast(t(x.event === "COMMENT" && r.state !== "open" ? "pr.publishedAsComment" : "pr.published"));
            })}>
            <Upload size={12} />
            {t("pr.publish")}
            {unposted > 0 && <span className="gxn">{unposted}</span>}
          </button>
          <span className="sp" />
          <select value={method} onChange={(e) => setMethod(e.target.value)} aria-label={t("git.strategy")}>
            <option value="squash">{t("git.st.squash")}</option>
            <option value="merge">{t("git.st.noff")}</option>
            <option value="rebase">rebase</option>
          </select>
          <button type="button" className="btn sm pri" disabled={busy || r.state !== "approved"} title={r.state !== "approved" ? t("git.mergeNeedsApproval") : undefined}
            onClick={async () => (await confirmDialog({ text: t("pr.mergeQ", { n: pr.number, target: r.target }), ok: t("git.merge") })) && run(() => API.gitPRMerge(agent.id, r.id, pr.number, method), t("pr.mergedOnGitHub"))}>
            <GitMerge size={12} />
            {t("pr.mergeOnGitHub")}
          </button>
        </div>
      )}
    </div>
  );
}
