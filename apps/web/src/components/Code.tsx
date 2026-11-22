// 代码: the agent's repository (features/git). Four places:
//
//   提交   what changed in the folder (or a worktree): stage, unstage or
//          discard whole files or single hunks, then commit
//   工作树 the worktrees sub-agents (and people) work in: who, what for,
//          how far along, its review; make one, remove one
//   评审   code reviews: the change with line comments, verdicts and
//          rounds, the merge (previewed first)
//   锁定   frozen files and lines
//
// Everything refreshes when the daemon says the repository changed
// (git.changed over SSE).

import { Fragment, createContext, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import {
  ArrowLeft, ArrowRight, Check, ChevronDown, ChevronRight, CircleDot, FolderGit2, GitBranch, GitCommitHorizontal, GitMerge, GitPullRequest,
  Lock, LockOpen, MessageSquare, Minus, Plus, RefreshCw, Settings2, ShieldCheck, Trash2, Undo2, Upload, User, X,
} from "lucide-react";
import {
  API, type AgentView, type GitComment, type GitCommentInput, type GitFileDiff, type GitFileStatus, type GitHunk, type GitLock,
  type GitRepoView, type GitReview, type GitReviewState, type GitSettings, type GitSeverity, type GitWorktree, type Hunk,
} from "../api";
import { t, type Key } from "../i18n";
import { ago } from "../format";
import { setState, toast, useStore } from "../store";
import { Avatar } from "./Avatar";
import { confirmDialog } from "./Confirm";
import { FileBadge } from "../lib/fileicons";
import { useHighlighted } from "./Files";
import { Popover, useMenu } from "./Popover";
import { Skeleton } from "./Skeleton";
import { Markdown } from "./Markdown";

type Section = "changes" | "worktrees" | "reviews" | "locks" | "settings";

const errOf = (e: any) => e?.message ?? String(e);

/** Runs an action, says what went wrong, and refreshes. */
function useAct(reload: () => void) {
  const [busy, setBusy] = useState(false);
  const run = async (f: () => Promise<unknown>, ok?: string) => {
    setBusy(true);
    try {
      await f();
      if (ok) toast(ok);
    } catch (e: any) {
      toast(errOf(e));
    } finally {
      setBusy(false);
      reload();
    }
  };
  return { busy, run };
}

// ---------------------------------------------------------------- actors

/** agent:kai → Kai; agent:kai-sub-2 → Kai 的子 Agent; person:owner → 你 */
function useActor() {
  const agents = useStore((s) => s.agents);
  return useMemo(() => {
    const name = (actor: string): string => {
      if (actor.startsWith("person:")) {
        const id = actor.slice(7);
        return id === "owner" ? t("git.you") : id;
      }
      const id = actor.replace(/^agent:/, "");
      if (agents[id]) return agents[id].name;
      const m = id.match(/^(.*)-sub-\d+$/);
      if (m) return t("git.subOf", { name: agents[m[1]]?.name ?? m[1] });
      return id;
    };
    const agentOf = (actor: string): AgentView | undefined => {
      const id = actor.replace(/^agent:/, "");
      return agents[id] ?? agents[id.replace(/-sub-\d+$/, "")];
    };
    return { name, agentOf };
  }, [agents]);
}

function Who({ actor, size = 18, label = true }: { actor: string; size?: number; label?: boolean }) {
  const { name, agentOf } = useActor();
  const a = actor.startsWith("agent:") ? agentOf(actor) : undefined;
  const sub = /-sub-\d+$/.test(actor);
  return (
    <span className="gxwho" title={name(actor)}>
      {a ? (
        <span className={"gxav" + (sub ? " sub" : "")}>
          <Avatar agent={a} size={size} />
        </span>
      ) : (
        <span className="gxav person" style={{ width: size, height: size }}>
          <User size={size - 6} />
        </span>
      )}
      {label && <span className="gxwhon">{name(actor)}</span>}
    </span>
  );
}

// ---------------------------------------------------------------- tab

export function CodeTab({ agent }: { agent: AgentView }) {
  const tick = useStore((s) => s.gitTick);
  const [view, setView] = useState<GitRepoView | null>(null);
  const [err, setErr] = useState("");
  const [section, setSection] = useState<Section>("changes");
  const [scope, setScope] = useState(""); // "" = the agent's folder; else a worktree id
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
  // a worktree that went away: back to the folder
  useEffect(() => {
    if (scope && view?.worktrees && !view.worktrees.some((w) => w.id === scope && w.state === "active")) setScope("");
  }, [view, scope]);

  if (err) return <div className="gxempty small errt">{err}</div>;
  if (!view) return <div className="gxpad"><Skeleton kind="code" /></div>;
  if (!view.repo)
    return (
      <div className="gxempty">
        <FolderGit2 size={28} strokeWidth={1.5} />
        <b>{t("git.noRepo")}</b>
        <span className="small muted">{t("git.noRepoHint")}</span>
      </div>
    );
  const repo = view.repo;
  const st = view.status!;
  const active = (view.worktrees ?? []).filter((w) => w.state === "active");
  const openReviews = (view.reviews ?? []).filter((r) => r.state !== "merged" && r.state !== "closed");
  const waiting = openReviews.filter((r) => r.reviewers.includes("person:owner") && (r.state === "open")).length;
  const nChanged = scope ? (active.find((w) => w.id === scope)?.status?.files.length ?? 0) : st.files.length;
  const tabs: [Section, string, ReactNode][] = [
    ["changes", t("git.sec.changes"), nChanged > 0 ? <span className="gxn">{nChanged}</span> : null],
    ["worktrees", t("git.sec.worktrees"), active.length > 0 ? <span className="gxn">{active.length}</span> : null],
    ["reviews", t("git.sec.reviews"), openReviews.length > 0 ? <span className={"gxn" + (waiting ? " hot" : "")}>{openReviews.length}</span> : null],
    ["locks", t("git.sec.locks"), (view.locks?.length ?? 0) > 0 ? <span className="gxn">{view.locks!.length}</span> : null],
  ];
  return (
    <div className="gx">
      <header className="gxhead">
        <span className="gxrepo" title={repo.main || repo.root}>
          <FolderGit2 size={15} />
          <b>{repo.name}</b>
        </span>
        <span className={"gxbranch" + (view.protected ? " prot" : "")} title={view.protected ? t("git.protectedBranch") : undefined}>
          <GitBranch size={12} />
          {st.branch || t("git.detached")}
          {view.protected && <ShieldCheck size={11} />}
        </span>
        {(st.ahead > 0 || st.behind > 0) && (
          <span className="gxab small" title={st.upstream}>
            {st.ahead > 0 && <>↑{st.ahead}</>} {st.behind > 0 && <>↓{st.behind}</>}
          </span>
        )}
        <span className="sp" />
        <button className="ix" type="button" aria-label={t("git.refresh")} title={t("git.refresh")} onClick={reload}>
          <RefreshCw size={13} />
        </button>
        <button className={"ix" + (section === "settings" ? " on" : "")} type="button" aria-label={t("git.settings")} title={t("git.settings")} onClick={() => (setSection("settings"), setReview(null))}>
          <Settings2 size={13} />
        </button>
      </header>
      {!review && (
        <div className="gxtabs" role="tablist">
          {tabs.map(([k, l, badge]) => (
            <button key={k} type="button" role="tab" aria-selected={section === k} className={"gxtab" + (section === k ? " on" : "")} onClick={() => setSection(k)}>
              {l}
              {badge}
            </button>
          ))}
        </div>
      )}
      <div className="gxbody">
        {review ? (
          <ReviewDetail key={review} agent={agent} id={review} onBack={() => (setReview(null), reload())} />
        ) : section === "changes" ? (
          <Changes agent={agent} view={view} scope={scope} setScope={setScope} reload={reload} />
        ) : section === "worktrees" ? (
          <Worktrees agent={agent} view={view} reload={reload} showAll={showAll} setShowAll={setShowAll}
            onOpen={(id) => (setScope(id), setSection("changes"))} onReview={(id) => setReview(id)} />
        ) : section === "reviews" ? (
          <Reviews reviews={view.reviews ?? []} onOpen={setReview} />
        ) : section === "locks" ? (
          <Locks agent={agent} locks={view.locks ?? []} reload={reload} />
        ) : (
          <GitSettingsView onDone={() => setSection("changes")} />
        )}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------- 提交

function Changes({ agent, view, scope, setScope, reload }: { agent: AgentView; view: GitRepoView; scope: string; setScope: (s: string) => void; reload: () => void }) {
  const tick = useStore((s) => s.gitTick);
  const wts = (view.worktrees ?? []).filter((w) => w.state === "active");
  const wt = wts.find((w) => w.id === scope);
  const st = wt ? wt.status : view.status;
  const branch = wt ? wt.branch : view.status?.branch;
  const prot = !wt && !!view.protected;
  const [work, setWork] = useState<GitFileDiff[] | null>(null);
  const [staged, setStaged] = useState<GitFileDiff[] | null>(null);
  const [open, setOpen] = useState<Set<string>>(new Set());
  const [msg, setMsg] = useState("");
  const [all, setAll] = useState(true);
  const [n, setN] = useState(0);
  const again = () => (setN((x) => x + 1), reload());
  const { busy, run } = useAct(again);
  useEffect(() => {
    let live = true;
    const w = scope || undefined;
    Promise.all([API.gitDiff(agent.id, { worktree: w, side: "work" }), API.gitDiff(agent.id, { worktree: w, side: "staged" })]).then(
      ([a, b]) => live && (setWork(a.files ?? []), setStaged(b.files ?? [])),
      (e) => live && toast(errOf(e)),
    );
    return () => void (live = false);
  }, [agent.id, scope, tick, n]);
  useEffect(() => setAll((staged?.length ?? 0) === 0), [staged?.length]);

  const toggle = (k: string) => setOpen((s) => {
    const x = new Set(s);
    x.has(k) ? x.delete(k) : x.add(k);
    return x;
  });
  const files = (paths: string[], action: "stage" | "unstage" | "discard") => run(() => API.gitFiles({ agent: agent.id, worktree: scope || undefined, paths, action }));
  const hunk = (path: string, h: GitHunk, action: "stage" | "unstage" | "discard") => run(() => API.gitHunk({ agent: agent.id, worktree: scope || undefined, path, hunk: h.id, action }));
  const discard = async (paths: string[]) => {
    if (!(await confirmDialog({ text: t("git.discardQ", { n: paths.length, name: paths[0] }), ok: t("git.discard"), danger: true }))) return;
    void files(paths, "discard");
  };
  const commit = () =>
    run(async () => {
      if (prot && !(await confirmDialog({ text: t("git.protectedCommitQ", { branch: branch ?? "" }), ok: t("git.commit") }))) return;
      const c = await API.gitCommit({ agent: agent.id, worktree: scope || undefined, message: msg, all });
      setMsg("");
      toast(t("git.committed", { short: c.short }));
    });
  const nStaged = staged?.length ?? 0;
  const nWork = work?.length ?? 0;
  const canCommit = msg.trim() !== "" && (all ? nStaged + nWork > 0 : nStaged > 0);

  return (
    <div className="gxchanges">
      <div className="gxscope">
        <ScopePicker view={view} scope={scope} setScope={setScope} />
        {st?.merging && <span className="gxwarn small">{t("git.merging")}</span>}
      </div>
      {!work || !staged ? (
        <div className="gxpad"><Skeleton kind="code" /></div>
      ) : nWork + nStaged === 0 ? (
        <div className="gxempty small">
          <Check size={22} strokeWidth={1.5} />
          <span>{t("git.clean")}</span>
        </div>
      ) : (
        <div className="gxlists">
          {nStaged > 0 && (
            <FileGroup title={t("git.staged")} count={nStaged} files={staged} side="staged" open={open} toggle={toggle}
              groupAct={<button type="button" className="gxga" disabled={busy} onClick={() => files(staged.map((f) => f.path), "unstage")}>{t("git.unstageAll")}</button>}
              fileActs={(f) => (
                <button type="button" className="ix" title={t("git.unstage")} aria-label={t("git.unstage")} disabled={busy} onClick={() => files([f.path], "unstage")}>
                  <Minus size={13} />
                </button>
              )}
              hunkActs={(f, h) => <button type="button" className="gxha" disabled={busy} onClick={() => hunk(f.path, h, "unstage")}><Minus size={11} />{t("git.unstageHunk")}</button>}
            />
          )}
          {nWork > 0 && (
            <FileGroup title={t("git.unstaged")} count={nWork} files={work} side="work" open={open} toggle={toggle}
              groupAct={
                <>
                  <button type="button" className="gxga" disabled={busy} onClick={() => discard(work.map((f) => f.path))}>{t("git.discardAll")}</button>
                  <button type="button" className="gxga" disabled={busy} onClick={() => files(work.map((f) => f.path), "stage")}>{t("git.stageAll")}</button>
                </>
              }
              fileActs={(f) => (
                <>
                  <button type="button" className="ix dng" title={t("git.discard")} aria-label={t("git.discard")} disabled={busy} onClick={() => discard([f.path])}>
                    <Undo2 size={12} />
                  </button>
                  <button type="button" className="ix" title={t("git.stage")} aria-label={t("git.stage")} disabled={busy} onClick={() => files([f.path], "stage")}>
                    <Plus size={13} />
                  </button>
                </>
              )}
              hunkActs={(f, h) => (
                <>
                  <button type="button" className="gxha dng" disabled={busy} onClick={async () => (await confirmDialog({ text: t("git.discardHunkQ"), ok: t("git.discard"), danger: true })) && hunk(f.path, h, "discard")}>
                    <Undo2 size={11} />
                    {t("git.discardHunk")}
                  </button>
                  <button type="button" className="gxha" disabled={busy} onClick={() => hunk(f.path, h, "stage")}>
                    <Plus size={11} />
                    {t("git.stageHunk")}
                  </button>
                </>
              )}
            />
          )}
        </div>
      )}
      <div className="gxcommit">
        <textarea
          value={msg}
          onChange={(e) => setMsg(e.target.value)}
          placeholder={t("git.msgPh")}
          rows={2}
          onKeyDown={(e) => {
            if ((e.metaKey || e.ctrlKey) && e.key === "Enter" && canCommit && !busy) (e.preventDefault(), void commit());
          }}
        />
        <div className="gxcrow">
          <label className="gxcheck small">
            <input type="checkbox" checked={all} onChange={(e) => setAll(e.target.checked)} />
            {t("git.commitAll")}
          </label>
          <span className="sp" />
          {wt && <PushBtn agent={agent} worktree={wt.id} branch={wt.branch} />}
          <button type="button" className="btn sm pri" disabled={!canCommit || busy} onClick={() => void commit()} title="⌘↵">
            <GitCommitHorizontal size={13} />
            {t("git.commitTo", { branch: branch ?? "HEAD" })}
          </button>
        </div>
        {prot && <div className="gxprot small"><ShieldCheck size={12} />{t("git.protectedHint", { branch: branch ?? "" })}</div>}
      </div>
      {!wt && (view.log?.length ?? 0) > 0 && (
        <details className="gxlog">
          <summary className="small">{t("git.recent")}</summary>
          {view.log!.map((c) => (
            <div key={c.hash} className="gxlogi">
              <code>{c.short}</code>
              <span className="g">{c.subject}</span>
              <span className="small muted">{c.author} · {ago(c.when)}</span>
            </div>
          ))}
        </details>
      )}
    </div>
  );
}

function ScopePicker({ view, scope, setScope }: { view: GitRepoView; scope: string; setScope: (s: string) => void }) {
  const m = useMenu();
  const wts = (view.worktrees ?? []).filter((w) => w.state === "active");
  const cur = wts.find((w) => w.id === scope);
  return (
    <>
      <button type="button" className="gxscopeb" ref={m.ref} onClick={m.toggle} aria-expanded={m.open}>
        {cur ? <GitBranch size={12} /> : <FolderGit2 size={12} />}
        <span>{cur ? cur.branch : t("git.mainFolder")}</span>
        {wts.length > 0 && <ChevronDown size={12} />}
      </button>
      {wts.length > 0 && (
        <Popover anchor={m.anchor} open={m.open} onClose={m.close} width={300}>
          <button className={"mi" + (!scope ? " on" : "")} type="button" onClick={() => (setScope(""), m.close())}>
            <FolderGit2 size={14} />
            <span className="g">
              {t("git.mainFolder")}
              <span>{view.status?.branch}</span>
            </span>
          </button>
          {wts.map((w) => (
            <button key={w.id} className={"mi" + (scope === w.id ? " on" : "")} type="button" onClick={() => (setScope(w.id), m.close())}>
              <GitBranch size={14} />
              <span className="g">
                <span className="mono">{w.branch}</span>
                <span>{w.task || w.id}</span>
              </span>
            </button>
          ))}
        </Popover>
      )}
    </>
  );
}

function FileGroup({ title, count, files, side, open, toggle, groupAct, fileActs, hunkActs }: {
  title: string; count: number; files: GitFileDiff[]; side: string; open: Set<string>; toggle: (k: string) => void;
  groupAct: ReactNode; fileActs: (f: GitFileDiff) => ReactNode; hunkActs: (f: GitFileDiff, h: GitHunk) => ReactNode;
}) {
  return (
    <section className="gxgroup">
      <div className="gxgh">
        <span className="small">{title}</span>
        <span className="gxn">{count}</span>
        <span className="sp" />
        {groupAct}
      </div>
      {files.map((f) => {
        const k = side + ":" + f.path;
        const isOpen = open.has(k);
        const name = f.path.split("/").pop() ?? f.path;
        const dir = f.path.includes("/") ? f.path.slice(0, f.path.lastIndexOf("/")) : "";
        return (
          <div key={k} className={"gxfile" + (isOpen ? " open" : "")}>
            <div className="gxfrow">
              <button type="button" className="gxfmain" onClick={() => toggle(k)} aria-expanded={isOpen}>
                {isOpen ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
                <FileBadge name={name} />
                <span className={"gxfname st-" + f.status}>{name}</span>
                {dir && <span className="gxfdir">{dir}</span>}
                <span className="sp" />
                <StatusTag s={f.status} />
                {!f.binary && (
                  <span className="fxic">
                    {f.added > 0 && <span className="add">+{f.added}</span>}
                    {f.removed > 0 && <span className="del">−{f.removed}</span>}
                  </span>
                )}
              </button>
              <span className="gxfacts">{fileActs(f)}</span>
            </div>
            {isOpen &&
              (f.binary ? (
                <div className="fxmsg small muted">{t("fx.binary")}</div>
              ) : f.hunks.length === 0 ? (
                <div className="fxmsg small muted">{t("fx.emptyFile")}</div>
              ) : (
                <GitDiff file={f} hunkBar={(h) => <div className="gxhbar">{hunkActs(f, h)}</div>} />
              ))}
          </div>
        );
      })}
    </section>
  );
}

function StatusTag({ s }: { s: GitFileStatus["status"] }) {
  const mark: Record<string, string> = { modified: "M", added: "A", deleted: "D", renamed: "R", untracked: "U", conflicted: "!", typechange: "T" };
  return (
    <span className={"fxst st-" + (s === "untracked" ? "added" : s)} title={t(`git.st.${s}` as Key)}>
      {mark[s]}
    </span>
  );
}

// ---------------------------------------------------------------- diff

const toHunk = (h: GitHunk): Hunk => ({ oldStart: h.oldStart, oldLines: h.oldLines, newStart: h.newStart, newLines: h.newLines, lines: h.lines.map((l) => ({ t: l.k, s: l.t, o: l.o, n: l.n })) });

/** A file's diff, with a bar above each hunk (stage / discard …) and, in
 *  a review, comments under their lines and a + to add one. */
function GitDiff({ file, hunkBar, comments, onAddComment }: {
  file: GitFileDiff;
  hunkBar?: (h: GitHunk) => ReactNode;
  comments?: GitComment[];
  /** set: lines take comments */
  onAddComment?: (line: number, side: "new" | "old", sev: GitSeverity, body: string) => void;
}) {
  const canComment = !!onAddComment;
  const hunks = useMemo(() => file.hunks.map(toHunk), [file]);
  const hl = useHighlighted(hunks, file.path);
  const [composer, setComposer] = useState<{ line: number; side: "new" | "old" } | null>(null);
  let oi = 0;
  let ni = 0;
  const at = (line: number, side: "new" | "old") => (comments ?? []).filter((c) => c.path === file.path && c.line === line && (c.side ?? "new") === side);
  return (
    <div className="fxdiff hljs gxdiff">
      <table>
        <tbody>
          {file.hunks.map((h, hi) => (
            <Fragment key={h.id}>
              <tr className="hh">
                <td colSpan={4}>
                  <span className="gxhh">
                    <span className="mono">{h.header.replace(/^(@@[^@]*@@).*/, "$1")}</span>
                    <span className="gxhctx">{h.header.replace(/^@@[^@]*@@\s?/, "")}</span>
                    <span className="sp" />
                    {hunkBar?.(h)}
                  </span>
                </td>
              </tr>
              {h.lines.map((l, li) => {
                const html = l.k === "+" ? hl.n[ni++] : l.k === "-" ? hl.o[oi++] : (ni++, hl.o[oi++]);
                const side: "new" | "old" = l.k === "-" ? "old" : "new";
                const line = side === "old" ? l.o ?? 0 : l.n ?? 0;
                const here = at(line, side);
                const writing = composer && composer.line === line && composer.side === side;
                return (
                  <Fragment key={hi + ":" + li}>
                    <tr className={(l.k === "+" ? "ad" : l.k === "-" ? "rm" : "") + (here.length ? " has-cm" : "")}>
                      <td className="ln">{l.o ?? ""}</td>
                      <td className="ln gxln">
                        {l.n ?? ""}
                        {canComment && line > 0 && (
                          <button type="button" className="gxadd" aria-label={t("git.addComment")} title={t("git.addComment")} onClick={() => setComposer({ line, side })}>
                            <Plus size={11} />
                          </button>
                        )}
                      </td>
                      <td className="mk">{l.k === " " ? "" : l.k === "-" ? "−" : "+"}</td>
                      <td className="lc" dangerouslySetInnerHTML={{ __html: html || " " }} />
                    </tr>
                    {(here.length > 0 || writing) && (
                      <tr className="gxcmrow">
                        <td colSpan={4}>
                          {here.map((c) => <InlineInReview key={c.id} c={c} />)}
                          {writing && onAddComment && (
                            <CommentComposer
                              onCancel={() => setComposer(null)}
                              onSave={(sev, body) => {
                                onAddComment(line, side, sev, body);
                                setComposer(null);
                              }}
                            />
                          )}
                        </td>
                      </tr>
                    )}
                  </Fragment>
                );
              })}
            </Fragment>
          ))}
        </tbody>
      </table>
    </div>
  );
}

const SEVS: GitSeverity[] = ["blocker", "major", "minor", "nit", "note"];

function CommentComposer({ onCancel, onSave }: { onCancel: () => void; onSave: (sev: GitSeverity, body: string) => void }) {
  const [sev, setSev] = useState<GitSeverity>("major");
  const [body, setBody] = useState("");
  const ref = useRef<HTMLTextAreaElement>(null);
  useEffect(() => ref.current?.focus(), []);
  return (
    <div className="gxcomposer">
      <div className="gxsevs" role="radiogroup" aria-label={t("git.severity")}>
        {SEVS.map((s) => (
          <button key={s} type="button" aria-pressed={sev === s} className={"gxsev sev-" + s} onClick={() => setSev(s)}>
            {t(`git.sev.${s}` as Key)}
          </button>
        ))}
      </div>
      <textarea ref={ref} rows={2} value={body} placeholder={t("git.commentPh")} onChange={(e) => setBody(e.target.value)}
        onKeyDown={(e) => {
          if ((e.metaKey || e.ctrlKey) && e.key === "Enter" && body.trim()) (e.preventDefault(), onSave(sev, body.trim()));
          if (e.key === "Escape") onCancel();
        }} />
      <div className="gxcrow">
        <span className="small muted">{t("git.pendingHint")}</span>
        <span className="sp" />
        <button type="button" className="btn sm ghost" onClick={onCancel}>{t("common.cancel")}</button>
        <button type="button" className="btn sm pri" disabled={!body.trim()} onClick={() => onSave(sev, body.trim())}>{t("git.addToReview")}</button>
      </div>
    </div>
  );
}

function InlineComment({ c, onResolve, pending, onDrop }: { c: GitComment; onResolve?: (resolved: boolean) => void; pending?: boolean; onDrop?: () => void }) {
  return (
    <div className={"gxcm" + (c.resolved ? " done" : "") + (pending ? " pending" : "")}>
      <div className="gxcmh">
        <Who actor={c.author} size={16} />
        <span className={"gxsevtag sev-" + c.severity}>{t(`git.sev.${c.severity}` as Key)}</span>
        {pending && <span className="gxsevtag pend">{t("git.pending")}</span>}
        {c.resolved && <span className="small muted">{t("git.resolved")}</span>}
        <span className="sp" />
        {!pending && <span className="small muted">{t("git.roundN", { n: c.round })} · {ago(c.created)}</span>}
        {onResolve && (
          <button type="button" className="linkbtn small" onClick={() => onResolve(!c.resolved)}>
            {c.resolved ? t("git.reopen") : t("git.resolve")}
          </button>
        )}
        {onDrop && (
          <button type="button" className="linkbtn small muted" onClick={onDrop}>
            {t("common.delete")}
          </button>
        )}
      </div>
      <div className="gxcmb md">
        <Markdown text={c.body} />
      </div>
    </div>
  );
}

// ---------------------------------------------------------------- 工作树

function Worktrees({ agent, view, reload, showAll, setShowAll, onOpen, onReview }: {
  agent: AgentView; view: GitRepoView; reload: () => void; showAll: boolean; setShowAll: (b: boolean) => void; onOpen: (id: string) => void; onReview: (id: string) => void;
}) {
  const { busy, run } = useAct(reload);
  const list = view.worktrees ?? [];
  const m = useMenu();
  const [task, setTask] = useState("");
  const [base, setBase] = useState("");
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
    <div className="gxwts">
      <div className="gxbar">
        <span className="small muted">{t("git.wtIntro")}</span>
        <span className="sp" />
        <button type="button" className="btn sm" ref={m.ref} onClick={m.toggle}>
          <Plus size={12} />
          {t("git.newWorktree")}
        </button>
        <Popover anchor={m.anchor} open={m.open} onClose={m.close} align="end" width={300} className="gxform">
          <label className="small">{t("git.wtTask")}</label>
          <input value={task} onChange={(e) => setTask(e.target.value)} placeholder={t("git.wtTaskPh")} />
          <label className="small">{t("git.wtBase")}</label>
          <input value={base} onChange={(e) => setBase(e.target.value)} placeholder={view.status?.branch ?? "main"} />
          <button type="button" className="btn sm pri" disabled={busy} onClick={() => (m.close(), void run(() => API.gitSpawn({ agent: agent.id, task, base: base || undefined }), t("git.wtMade")))}>
            {t("git.create")}
          </button>
        </Popover>
      </div>
      {list.length === 0 ? (
        <div className="gxempty small">
          <GitBranch size={22} strokeWidth={1.5} />
          <span>{t("git.noWorktrees")}</span>
        </div>
      ) : (
        list.map((w) => (
          <article key={w.id} className={"gxwt" + (w.state !== "active" ? " gone" : "")}>
            <div className="gxwth">
              <GitBranch size={13} />
              <b className="mono" title={w.path}>{w.branch}</b>
              <span className="small muted">{t("git.fromBase", { base: w.base })}</span>
              <span className="sp" />
              <WtState w={w} />
            </div>
            <div className="gxwtm">
              <Who actor={"agent:" + (w.child && w.state === "active" ? w.child : w.holder)} />
              {w.origin === "subagent" && <span className="pill sm">{t("git.bySub")}</span>}
              <span className="small muted">{ago(w.created)}</span>
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
                <button type="button" className="btn sm ghost" onClick={() => onOpen(w.id)}>{t("git.viewChanges")}</button>
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
          </article>
        ))
      )}
      <label className="gxcheck small gxall">
        <input type="checkbox" checked={showAll} onChange={(e) => setShowAll(e.target.checked)} />
        {t("git.showRemoved")}
      </label>
    </div>
  );
}

function WtState({ w }: { w: GitWorktree }) {
  if (w.state !== "active") return <span className={"gxst wt-" + w.state}>{t(`git.wt.${w.state}` as Key)}</span>;
  if (w.missing) return <span className="gxst wt-removed">{t("git.wt.missing")}</span>;
  const dirty = (w.status?.files.length ?? 0) > 0;
  return <span className={"gxst " + (dirty ? "wt-dirty" : w.ahead > 0 ? "wt-ready" : "wt-idle")}>{dirty ? t("git.wt.dirty") : w.ahead > 0 ? t("git.wt.ready") : t("git.wt.idle")}</span>;
}

// ---------------------------------------------------------------- 评审

function ReviewState({ s }: { s: GitReviewState }) {
  const icon = s === "merged" ? <GitMerge size={11} /> : s === "approved" ? <Check size={11} /> : s === "changes_requested" ? <X size={11} /> : <GitPullRequest size={11} />;
  return (
    <span className={"gxst rv-" + s}>
      {icon}
      {t(`git.rv.${s}` as Key)}
    </span>
  );
}

function Reviews({ reviews, onOpen }: { reviews: GitReview[]; onOpen: (id: string) => void }) {
  const [all, setAll] = useState(false);
  const list = reviews.filter((r) => all || (r.state !== "merged" && r.state !== "closed"));
  const done = reviews.length - reviews.filter((r) => r.state !== "merged" && r.state !== "closed").length;
  return (
    <div className="gxrvs">
      <div className="gxbar">
        <span className="seg gxmini">
          <button type="button" aria-pressed={!all} onClick={() => setAll(false)}>{t("git.rvActive")}</button>
          <button type="button" aria-pressed={all} onClick={() => setAll(true)}>{t("git.rvAll")}{done > 0 && ` · ${done}`}</button>
        </span>
      </div>
      {list.length === 0 ? (
        <div className="gxempty small">
          <GitPullRequest size={22} strokeWidth={1.5} />
          <span>{t("git.noReviews")}</span>
          <span className="muted">{t("git.noReviewsHint")}</span>
        </div>
      ) : (
        list.map((r) => {
          const open = r.comments.filter((c) => !c.resolved && (c.severity === "blocker" || c.severity === "major")).length;
          return (
            <button key={r.id} type="button" className="gxrv" onClick={() => onOpen(r.id)}>
              <div className="gxrvh">
                <ReviewState s={r.state} />
                <b className="g">{r.title}</b>
              </div>
              <div className="gxrvm small">
                <span className="mono gxflow">{r.source} <ArrowRight size={11} /> {r.target}</span>
                <span className="sp" />
                {r.state !== "merged" && r.state !== "closed" && <span className="muted">{t("git.roundOf", { n: r.round, max: r.maxRounds })}</span>}
                {r.comments.length > 0 && (
                  <span className={"gxcmc" + (open ? " hot" : "")}>
                    <MessageSquare size={11} />
                    {r.comments.length}
                  </span>
                )}
              </div>
              <div className="gxrvp small">
                <Who actor={r.author} />
                <ArrowRight size={11} className="muted" />
                <span className="gxstack">
                  {r.reviewers.map((x) => {
                    const v = lastVerdict(r, x);
                    return (
                      <span key={x} className={"gxrvr" + (v ? " v-" + v : "")} title={v ? t(`git.v.${v}` as Key) : t("git.v.waiting")}>
                        <Who actor={x} label={r.reviewers.length < 3} />
                      </span>
                    );
                  })}
                </span>
                <span className="sp" />
                <span className="muted">{ago(r.updated)}</span>
              </div>
            </button>
          );
        })
      )}
    </div>
  );
}

function lastVerdict(r: GitReview, who: string): "approve" | "request_changes" | undefined {
  for (let i = r.verdicts.length - 1; i >= 0; i--) {
    const v = r.verdicts[i];
    if (v.reviewer === who && v.decision !== "comment") {
      if (v.decision === "approve" && v.commit !== r.head) return undefined; // stale
      return v.decision;
    }
  }
  return undefined;
}

function ReviewDetail({ agent, id, onBack }: { agent: AgentView; id: string; onBack: () => void }) {
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
  if (err) return <div className="gxempty small errt">{err}</div>;
  if (!data) return <div className="gxpad"><Skeleton kind="code" /></div>;
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
  const approvers = r.reviewers.filter((x) => lastVerdict(r, x) === "approve");
  const blockers = r.comments.filter((c) => c.severity === "blocker" && !c.resolved).length;
  const add = (file: string) => (line: number, side: "new" | "old", sev: GitSeverity, text: string) =>
    setPending((x) => [...x, { path: file, line, side, severity: sev, body: text }]);

  return (
    <div className="gxrd">
      <div className="gxrdtop">
        <button type="button" className="btn sm ghost" onClick={onBack}>
          <ArrowLeft size={12} />
          {t("git.sec.reviews")}
        </button>
        <span className="sp" />
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
        {live && <span className="small muted">{t("git.roundOf", { n: r.round, max: r.maxRounds })}</span>}
        <span className="small muted">· {r.head.slice(0, 8)}</span>
      </div>
      <div className="gxrdp">
        <div>
          <span className="small muted">{t("git.author")}</span>
          <Who actor={r.author} />
        </div>
        <div>
          <span className="small muted">{t("git.reviewers")}</span>
          <span className="gxstack">
            {r.reviewers.map((x) => {
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
                <select value={strategy} onChange={(e) => setStrategy(e.target.value)} aria-label={t("git.strategy")}>
                  <option value="merge-no-ff">{t("git.st.noff")}</option>
                  <option value="squash">{t("git.st.squash")}</option>
                  <option value="ff-only" disabled={!p?.fastForward}>{t("git.st.ff")}</option>
                </select>
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
                <ReviewCtx.Provider value={{ pendingIds: new Set(pendingAsComments.map((c) => c.id)), onResolve: (cid, v) => void run(() => API.gitResolveComment(agent.id, r.id, cid, v)), onDrop: (cid) => setPending((x) => x.filter((_, i) => "p" + i !== cid)) }}>
                  <GitDiff file={f} comments={cms} onAddComment={live && r.state !== "draft" && !data.selfReview ? add(f.path) : undefined} />
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

// ---------------------------------------------------------------- 锁定

function Locks({ agent, locks, reload }: { agent: AgentView; locks: GitLock[]; reload: () => void }) {
  const { busy, run } = useAct(reload);
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
    }, t("git.locked"));
  return (
    <div className="gxlocks">
      <p className="small muted gxintro">{t("git.lockIntro")}</p>
      {locks.length === 0 ? (
        <div className="gxempty small">
          <LockOpen size={22} strokeWidth={1.5} />
          <span>{t("git.noLocks")}</span>
        </div>
      ) : (
        locks.map((l) => (
          <div key={l.id} className="gxlock">
            <Lock size={13} />
            <div className="g">
              <span className="mono">{l.path}{l.start ? `:${l.start}–${l.end}` : ""}</span>
              <span className="small muted">
                {l.start ? t("git.lockLines", { a: l.start, b: l.end ?? l.start }) : t("git.lockFile")}
                {l.reason && <> · {l.reason}</>}
              </span>
            </div>
            <Who actor={l.holder} />
            <button type="button" className="linkbtn small" disabled={busy} onClick={() => run(() => API.gitUnlock(agent.id, l.id), t("git.unlocked"))}>
              {t("git.unlock")}
            </button>
          </div>
        ))
      )}
      <div className="gxlockf">
        <input className="mono" value={path} onChange={(e) => setPath(e.target.value)} placeholder={t("git.lockPathPh")} />
        <div className="gxlockl">
          <input inputMode="numeric" value={from} onChange={(e) => setFrom(e.target.value.replace(/\D/g, ""))} placeholder={t("git.lockFrom")} />
          <span className="muted">–</span>
          <input inputMode="numeric" value={to} onChange={(e) => setTo(e.target.value.replace(/\D/g, ""))} placeholder={t("git.lockTo")} />
        </div>
        <input value={reason} onChange={(e) => setReason(e.target.value)} placeholder={t("git.lockReasonPh")} />
        <button type="button" className="btn sm pri" disabled={busy || !path.trim()} onClick={() => void add()}>
          <Lock size={12} />
          {t("git.lock")}
        </button>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------- push

function PushBtn({ agent, worktree, branch }: { agent: AgentView; worktree?: string; branch: string }) {
  const [busy, setBusy] = useState(false);
  return (
    <button type="button" className="btn sm ghost" disabled={busy} title={t("git.pushTitle")}
      onClick={async () => {
        if (!(await confirmDialog({ text: t("git.pushQ", { branch }), ok: t("git.push") }))) return;
        setBusy(true);
        try {
          await API.gitPush({ agent: agent.id, worktree, branch });
          toast(t("git.pushed", { branch }));
        } catch (e: any) {
          toast(errOf(e));
        } finally {
          setBusy(false);
        }
      }}>
      <Upload size={12} />
      {t("git.push")}
    </button>
  );
}

// ---------------------------------------------------------------- settings

function GitSettingsView({ onDone }: { onDone: () => void }) {
  const [s, setS] = useState<GitSettings | null>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => void API.gitSettings().then(setS, (e) => toast(errOf(e))), []);
  if (!s) return <div className="gxpad"><Skeleton kind="code" /></div>;
  const save = async () => {
    setBusy(true);
    try {
      setS(await API.setGitSettings(s));
      toast(t("git.saved"));
      onDone();
    } catch (e: any) {
      toast(errOf(e));
    } finally {
      setBusy(false);
    }
  };
  const iso: GitSettings["isolation"][] = ["worktree-on-write", "isolated-worktree", "shared"];
  return (
    <div className="gxset">
      <h4>{t("git.set.isolation")}</h4>
      <div className="gxopts">
        {iso.map((k) => (
          <label key={k} className={"gxopt" + (s.isolation === k ? " on" : "")}>
            <input type="radio" name="iso" checked={s.isolation === k} onChange={() => setS({ ...s, isolation: k })} />
            <span className="g">
              <b>{t(`git.iso.${k}` as Key)}</b>
              <span className="small muted">{t(`git.iso.${k}.hint` as Key)}</span>
            </span>
          </label>
        ))}
      </div>
      <h4>{t("git.set.review")}</h4>
      <div className="gxfield">
        <span>{t("git.set.rounds")}</span>
        <input type="number" min={0} max={10} value={s.maxAutoRounds} onChange={(e) => setS({ ...s, maxAutoRounds: Number(e.target.value) })} />
      </div>
      <div className="gxfield">
        <span>{t("git.set.approvals")}</span>
        <input type="number" min={1} max={5} value={s.approvals} onChange={(e) => setS({ ...s, approvals: Number(e.target.value) })} />
      </div>
      <label className="gxcheck">
        <input type="checkbox" checked={s.autoReview} onChange={(e) => setS({ ...s, autoReview: e.target.checked })} />
        {t("git.set.autoReview")}
      </label>
      <h4>{t("git.set.protected")}</h4>
      <input className="mono" value={s.protected.join(", ")} onChange={(e) => setS({ ...s, protected: e.target.value.split(",").map((x) => x.trim()) })} placeholder="main, release/*" />
      <span className="small muted">{t("git.set.protectedHint")}</span>
      <h4>{t("git.set.remotes")}</h4>
      <span className="small muted">{t("git.set.remotesHint")}</span>
      {s.remotes.map((r, i) => (
        <div key={i} className="gxremote">
          <input value={r.host} placeholder="github.com" onChange={(e) => setS({ ...s, remotes: s.remotes.map((x, j) => (j === i ? { ...x, host: e.target.value } : x)) })} />
          <input className="mono" value={r.credential} placeholder="vault:github-token" onChange={(e) => setS({ ...s, remotes: s.remotes.map((x, j) => (j === i ? { ...x, credential: e.target.value } : x)) })} />
          <button type="button" className="ix" aria-label={t("common.delete")} onClick={() => setS({ ...s, remotes: s.remotes.filter((_, j) => j !== i) })}>
            <X size={12} />
          </button>
        </div>
      ))}
      <button type="button" className="linkbtn small" onClick={() => setS({ ...s, remotes: [...s.remotes, { host: "", credential: "vault:" }] })}>
        + {t("git.set.addRemote")}
      </button>
      <div className="gxcrow gxsetf">
        <span className="sp" />
        <button type="button" className="btn sm ghost" onClick={onDone}>{t("common.cancel")}</button>
        <button type="button" className="btn sm pri" disabled={busy} onClick={() => void save()}>{t("common.save")}</button>
      </div>
    </div>
  );
}

/** Opens the 代码 tab (a review, from elsewhere). */
export function openCode() {
  setState({ panelOpen: true, panelTab: "code" });
}
