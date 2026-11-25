// A local code review (features/git): sub-agents' work in their worktrees
// is reviewed here before it merges — the person approves or asks for
// changes with line comments, then merges (previewed first). The
// conversation's own pull request does not come here: its review is
// hidden behind the PR.

import { createContext, useContext, useEffect, useState } from "react";
import { ArrowLeft, ArrowRight, Check, ChevronDown, ChevronRight, CircleDot, ExternalLink, GitMerge, GitPullRequest, MessageSquare, ShieldCheck, Upload, X } from "lucide-react";
import { API, type AgentView, type GitComment, type GitCommentInput, type GitReview, type GitReviewState, type GitSeverity } from "../../api";
import { t, type Key } from "../../i18n";
import { ago } from "../../format";
import { toast, useStore } from "../../store";
import { confirmDialog } from "../Confirm";
import { FileBadge } from "../../lib/fileicons";
import { Markdown } from "../Markdown";
import { Skeleton } from "../Skeleton";
import { GitDiff, InlineComment, SevComposer } from "./diff";
import { errOf, useAct, Who } from "./shared";

export function ReviewState({ s }: { s: GitReviewState }) {
  const icon = s === "merged" ? <GitMerge size={11} /> : s === "approved" ? <Check size={11} /> : s === "changes_requested" ? <X size={11} /> : <GitPullRequest size={11} />;
  return (
    <span className={"gxst rv-" + s}>
      {icon}
      {t(`git.rv.${s}` as Key)}
    </span>
  );
}

export function lastVerdict(r: GitReview, who: string): "approve" | "request_changes" | undefined {
  for (let i = r.verdicts.length - 1; i >= 0; i--) {
    const v = r.verdicts[i];
    if (v.reviewer === who && v.decision !== "comment") {
      if (v.decision === "approve" && v.commit !== r.head) return undefined; // stale
      return v.decision;
    }
  }
  return undefined;
}

/** A review in a list: its state, branches, who wrote and who reviews it. */
export function ReviewRow({ r, onOpen }: { r: GitReview; onOpen: () => void }) {
  const open = r.comments.filter((c) => !c.resolved && (c.severity === "blocker" || c.severity === "major")).length;
  return (
    <button type="button" className="gxrv" onClick={onOpen}>
      <div className="gxrvh">
        <ReviewState s={r.state} />
        <b className="g">{r.title}</b>
      </div>
      <div className="gxrvm small">
        <span className="mono gxflow">{r.source} <ArrowRight size={11} /> {r.target}</span>
        <span className="sp" />
        {r.comments.length > 0 && (
          <span className={"gxcmc" + (open ? " hot" : "")}>
            <MessageSquare size={11} />
            {r.comments.length}
          </span>
        )}
        <Who actor={r.author} label={false} size={16} />
        <span className="muted">{ago(r.updated)}</span>
      </div>
    </button>
  );
}

export function ReviewDetail({ agent, id, onBack }: { agent: AgentView; id: string; onBack: () => void }) {
  const tick = useStore((s) => s.gitTick);
  const [data, setData] = useState<Awaited<ReturnType<typeof API.gitReview>> | null>(null);
  const [err, setErr] = useState("");
  const [n, setN] = useState(0);
  const [pending, setPending] = useState<GitCommentInput[]>([]);
  const [body, setBody] = useState("");
  const [strategy, setStrategy] = useState("merge-no-ff");
  const [openFiles, setOpenFiles] = useState<Set<string> | null>(null);
  const reload = () => setN((x) => x + 1);
  const { busy, run } = useAct(reload);
  useEffect(() => {
    let live = true;
    API.gitReview(agent.id, id).then(
      (d) => live && (setData(d), setErr("")),
      (e) => live && setErr(errOf(e)),
    );
    return () => void (live = false);
  }, [agent.id, id, tick, n]);
  const back = (
    <div className="gxrdtop">
      <button type="button" className="btn sm ghost" onClick={onBack}>
        <ArrowLeft size={12} />
        {t("code.back")}
      </button>
    </div>
  );
  if (err) return <div className="gxrd">{back}<p className="cderr small">{err}</p></div>;
  if (!data) return <div className="gxrd">{back}<Skeleton kind="code" /></div>;
  const r = data.review;
  const p = data.preview;
  const live = r.state !== "merged" && r.state !== "closed";
  const canJudge = live && r.state !== "draft" && !data.selfReview;
  const files = data.files;
  const shown = openFiles ?? new Set(files.slice(0, 3).map((f) => f.path));
  const toggle = (path: string) => setOpenFiles(() => {
    const x = new Set(shown);
    x.has(path) ? x.delete(path) : x.add(path);
    return x;
  });
  const general = r.comments.filter((c) => !c.path);
  const pendingAsComments: GitComment[] = pending.map((c, i) => ({ id: "p" + i, author: data.me, path: c.path, line: c.line, side: c.side, severity: c.severity ?? "note", body: c.body, commit: r.head, round: r.round, created: new Date().toISOString() }));
  const submit = (decision: "approve" | "request_changes" | "comment") =>
    run(async () => {
      await API.gitSubmit(agent.id, r.id, { decision, body: body.trim() || undefined, comments: pending });
      setPending([]);
      setBody("");
    }, t(`git.submitted.${decision}` as Key));
  const merge = () =>
    run(async () => {
      if (!(await confirmDialog({ text: t("git.mergeQ", { source: r.source, target: r.target }), ok: t("git.merge") }))) return;
      const out = await API.gitMerge(agent.id, r.id, strategy);
      toast(t("git.mergedAs", { short: out.commit.short }));
    });
  // everyone whose latest verdict approves the current head (a person too)
  const approvers = [...new Set(r.verdicts.map((v) => v.reviewer))].filter((x) => lastVerdict(r, x) === "approve");
  const blockers = r.comments.filter((c) => c.severity === "blocker" && !c.resolved).length;
  const add = (file: string) => (line: number, side: "new" | "old", sev: GitSeverity, text: string) =>
    setPending((x) => [...x, { path: file, line, side, severity: sev, body: text }]);
  const ctx = { pendingIds: new Set(pendingAsComments.map((c) => c.id)), onResolve: (cid: string, v: boolean) => void run(() => API.gitResolveComment(agent.id, r.id, cid, v)), onDrop: (cid: string) => setPending((x) => x.filter((_, i) => "p" + i !== cid)) };

  return (
    <div className="gxrd">
      <div className="gxrdtop">
        <button type="button" className="btn sm ghost" onClick={onBack}>
          <ArrowLeft size={12} />
          {t("code.back")}
        </button>
        <span className="sp" />
        {r.pr && (
          <a className="btn sm ghost" href={r.pr.url} target="_blank" rel="noreferrer">
            <ExternalLink size={12} />#{r.pr.number}
          </a>
        )}
        {r.pr && r.pr.state === "open" && live && (
          <button type="button" className="btn sm ghost" disabled={busy}
            onClick={async () => {
              const pr = r.pr!;
              const unposted = r.comments.filter((c) => !c.resolved && !(pr.posted ?? []).includes(c.id)).length;
              if (!(await confirmDialog({ text: t("pr.publishQ", { n: pr.number, c: unposted }), ok: t("pr.publish") }))) return;
              void run(async () => {
                const x = await API.gitPRReview(agent.id, r.id, pr.number);
                toast(t(x.event === "COMMENT" && r.state !== "open" ? "pr.publishedAsComment" : "pr.published"));
              });
            }}>
            <Upload size={12} />
            {t("pr.publish")}
          </button>
        )}
        {live && (
          <button type="button" className="linkbtn small muted" disabled={busy} onClick={async () => (await confirmDialog({ text: t("git.closeQ"), ok: t("git.closeReview"), danger: true })) && run(() => API.gitReviewAct(agent.id, r.id, "close"))}>
            {t("git.closeReview")}
          </button>
        )}
      </div>
      <h3 className="gxrdt">{r.title}</h3>
      <div className="gxrdm">
        <ReviewState s={r.state} />
        <span className="mono small gxflow">{r.source} <ArrowRight size={11} /> {r.target}</span>
        {live && <span className="small muted">{r.round > r.maxRounds ? t("git.roundOver", { n: r.round }) : t("git.roundOf", { n: r.round, max: r.maxRounds })}</span>}
      </div>
      <div className="gxrdp">
        <div>
          <span className="small muted">{t("git.author")}</span>
          <Who actor={r.author} />
        </div>
        <div>
          <span className="small muted">{t("git.reviewers")}</span>
          <span className="gxstack">
            {[...r.reviewers, ...approvers.filter((x) => !r.reviewers.includes(x))].map((x) => {
              const v = lastVerdict(r, x);
              return (
                <span key={x} className={"gxrvr" + (v ? " v-" + v : "")}>
                  <Who actor={x} />
                  <span className="small muted">{v ? t(`git.v.${v}` as Key) : t("git.v.waiting")}</span>
                </span>
              );
            })}
          </span>
        </div>
      </div>
      {r.body && <ReviewBody text={r.body} />}

      {/* where it stands: what is missing before it merges */}
      {live ? (
        <div className={"gxgate" + (r.state === "approved" && p?.clean ? " ok" : !p?.clean ? " bad" : "")}>
          <ul>
            <li className={approvers.length > 0 ? "ok" : ""}>
              {approvers.length > 0 ? <Check size={13} /> : <CircleDot size={13} />}
              {approvers.length > 0 ? t("git.gate.approved", { who: approvers.length }) : r.state === "draft" ? t("git.gate.draft") : t("git.gate.needApproval")}
            </li>
            <li className={blockers === 0 ? "ok" : "bad"}>
              {blockers === 0 ? <Check size={13} /> : <X size={13} />}
              {blockers === 0 ? t("git.gate.noBlockers") : t("git.gate.blockers", { n: blockers })}
            </li>
            {p && (
              <li className={p.clean ? "ok" : "bad"}>
                {p.clean ? <Check size={13} /> : <X size={13} />}
                {p.upToDate ? t("git.gate.upToDate") : p.clean ? (p.fastForward ? t("git.gate.ff", { target: r.target }) : t("git.gate.clean", { target: r.target })) : t("git.gate.conflicts", { files: (p.conflicts ?? []).join(", ") })}
              </li>
            )}
          </ul>
          <div className="gxgatea">
            {r.state === "draft" ? (
              <button type="button" className="btn sm pri" disabled={busy} onClick={() => run(() => API.gitReviewAct(agent.id, r.id, "publish"))}>
                {t("git.publish")}
              </button>
            ) : (
              <>
                <span className="seg sm" role="radiogroup" aria-label={t("git.strategy")}>
                  {([["merge-no-ff", "git.st.noff"], ["squash", "git.st.squash"], ["ff-only", "git.st.ff"]] as const).map(([k, l]) => (
                    <button key={k} type="button" aria-pressed={strategy === k} disabled={k === "ff-only" && !p?.fastForward} onClick={() => setStrategy(k)}>
                      {t(l)}
                    </button>
                  ))}
                </span>
                <span className="sp" />
                <button type="button" className="btn sm pri" disabled={busy || r.state !== "approved" || !p?.clean} onClick={() => void merge()}
                  title={r.state !== "approved" ? t("git.mergeNeedsApproval") : undefined}>
                  <GitMerge size={13} />
                  {t("git.mergeInto", { target: r.target })}
                </button>
              </>
            )}
          </div>
        </div>
      ) : (
        r.merge && (
          <div className="gxgate ok">
            <ul>
              <li className="ok">
                <GitMerge size={13} />
                {t("git.mergedBy", { who: "", short: r.merge.commit.slice(0, 8), at: ago(r.merge.at) })}
              </li>
            </ul>
          </div>
        )
      )}

      {/* verdict */}
      {live && r.state !== "draft" && (
        <div className="gxverdict">
          {data.selfReview ? (
            <div className="gxself small">
              <ShieldCheck size={13} />
              {t("git.selfReview")}
            </div>
          ) : (
            <>
              <textarea rows={2} value={body} onChange={(e) => setBody(e.target.value)} placeholder={t("git.verdictPh")} />
              <div className="gxcrow">
                {pending.length > 0 ? <span className="small gxpend">{t("git.pendingN", { n: pending.length })}</span> : <span className="small muted">{t("git.lineHint")}</span>}
                <span className="sp" />
                <button type="button" className="btn sm ghost" disabled={busy || (!body.trim() && pending.length === 0)} onClick={() => void submit("comment")}>
                  <MessageSquare size={12} />
                  {t("git.v.comment")}
                </button>
                <button type="button" className="btn sm dng" disabled={busy || !canJudge} onClick={() => void submit("request_changes")}>
                  <X size={12} />
                  {t("git.requestChanges")}
                </button>
                <button type="button" className="btn sm okb" disabled={busy || !canJudge || pending.some((c) => c.severity === "blocker")} onClick={() => void submit("approve")}>
                  <Check size={12} />
                  {t("git.approve")}
                </button>
              </div>
            </>
          )}
        </div>
      )}

      {/* the conversation: verdicts and general comments, oldest first */}
      {(r.verdicts.length > 0 || general.length > 0) && (
        <section className="gxtl">
          {[...r.verdicts.map((v) => ({ at: v.at, v })), ...general.map((c) => ({ at: c.created, c }))]
            .sort((a, b) => a.at.localeCompare(b.at))
            .map((x, i) =>
              "v" in x && x.v ? (
                <div key={i} className={"gxtli v-" + x.v.decision}>
                  <Who actor={x.v.reviewer} />
                  <span className="small">
                    <b>{t(`git.v.${x.v.decision}` as Key)}</b> · {t("git.roundN", { n: x.v.round })} · {ago(x.v.at)}
                    {x.v.decision === "approve" && x.v.commit !== r.head && <span className="gxstale"> · {t("git.stale")}</span>}
                  </span>
                  {x.v.body && <ReviewBody text={x.v.body} />}
                </div>
              ) : (
                "c" in x && x.c && <InlineComment key={i} c={x.c} onResolve={(v) => void run(() => API.gitResolveComment(agent.id, r.id, x.c!.id, v))} />
              ),
            )}
        </section>
      )}

      {/* the change */}
      <section className="gxrdfiles">
        <div className="gxgh">
          <span className="small">{t("git.filesChanged")}</span>
          <span className="gxn">{files.length}</span>
          <span className="sp" />
          <span className="fxic small">
            <span className="add">+{files.reduce((a, f) => a + f.added, 0)}</span>
            <span className="del">−{files.reduce((a, f) => a + f.removed, 0)}</span>
          </span>
        </div>
        {files.map((f) => {
          const name = f.path.split("/").pop() ?? f.path;
          const isOpen = shown.has(f.path);
          const cms = [...r.comments.filter((c) => c.path === f.path), ...pendingAsComments.filter((c) => c.path === f.path)];
          const canComment = live && r.state !== "draft" && !data.selfReview;
          return (
            <div key={f.path} className={"gxfile" + (isOpen ? " open" : "")}>
              <div className="gxfrow">
                <button type="button" className="gxfmain" onClick={() => toggle(f.path)} aria-expanded={isOpen}>
                  {isOpen ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
                  <FileBadge name={name} />
                  <span className={"gxfname st-" + f.status}>{f.path}</span>
                  <span className="sp" />
                  {cms.length > 0 && (
                    <span className="gxcmc">
                      <MessageSquare size={11} />
                      {cms.length}
                    </span>
                  )}
                  <span className="fxic">
                    {f.added > 0 && <span className="add">+{f.added}</span>}
                    {f.removed > 0 && <span className="del">−{f.removed}</span>}
                  </span>
                </button>
              </div>
              {isOpen && (f.binary ? <div className="fxmsg small muted">{t("fx.binary")}</div> : (
                <ReviewCtx.Provider value={ctx}>
                  <GitDiff file={f} comments={cms} renderComment={(c) => <InlineInReview c={c} />}
                    composer={canComment ? (line, side, close) => <SevComposer onCancel={close} onSave={(sev, text) => (add(f.path)(line, side, sev, text), close())} /> : undefined} />
                </ReviewCtx.Provider>
              ))}
            </div>
          );
        })}
      </section>
    </div>
  );
}

/** The author's description: Markdown, folded when long. */
function ReviewBody({ text }: { text: string }) {
  const [open, setOpen] = useState(false);
  const long = text.length > 420 || text.split("\n").length > 8;
  return (
    <div className={"gxrdb" + (long && !open ? " fold" : "")}>
      <div className="md">
        <Markdown text={text} />
      </div>
      {long && (
        <button type="button" className="linkbtn small gxmore" onClick={() => setOpen(!open)}>
          {open ? t("git.less") : t("git.more")}
        </button>
      )}
    </div>
  );
}

// a comment under its line knows, in a review, whether it is pending and how to resolve it
const ReviewCtx = createContext<{ pendingIds: Set<string>; onResolve: (cid: string, resolved: boolean) => void; onDrop: (cid: string) => void } | null>(null);

function InlineInReview({ c }: { c: GitComment }) {
  const ctx = useContext(ReviewCtx);
  const pending = !!ctx?.pendingIds.has(c.id);
  return <InlineComment c={c} pending={pending} onDrop={pending ? () => ctx!.onDrop(c.id) : undefined} onResolve={ctx && !pending ? (v) => ctx.onResolve(c.id, v) : undefined} />;
}
