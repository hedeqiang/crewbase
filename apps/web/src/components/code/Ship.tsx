// Before a pull request: where the work stands, and the way to ship it.
//
//   创建 PR   the agent drafts the title and description and opens it (the
//             push asks the person); 自己填写 opens it from here instead —
//             a branch of its own if the work is on main, a commit, the
//             local review the pull request hangs on (never shown), push
//   只提交    a commit, in place
//   推送      the branch to origin (asks first)
//
// Without a GitHub origin only 提交 and 推送 are offered.

import { useState } from "react";
import { ArrowRight, CircleCheck, GitBranch, GitCommitHorizontal, GitPullRequest, KeyRound, LoaderCircle, PenLine, Settings2, Upload } from "lucide-react";
import { API, type AgentView, type GitCommit, type GitRepoView, type GitReview } from "../../api";
import { t } from "../../i18n";
import { openSettings, toast } from "../../store";
import { confirmDialog } from "../Confirm";
import { changedPaths, type Diffs } from "./Changes";
import { errOf, tellAgent } from "./shared";

// a 创建 PR handed to the agent, per agent and branch, until the PR shows up
const asked = new Map<string, number>();
const ASK_FOR = 15 * 60_000;

function branchName(title: string, agent: string): string {
  const words = title.toLowerCase().replace(/[^a-z0-9]+/g, " ").trim().split(/\s+/).filter(Boolean).slice(0, 6);
  if (words.join("").length >= 3) return "crew/" + words.join("-").slice(0, 48);
  const d = new Date();
  const p = (n: number) => String(n).padStart(2, "0");
  return `crew/${agent}-${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}`;
}

export function Ship({ agent, session, view, diffs, commits, review, reload }: {
  agent: AgentView;
  session: string;
  view: GitRepoView;
  diffs: Diffs | null;
  /** the branch's commits not on the default branch (null: on it, or loading) */
  commits: GitCommit[] | null;
  /** a live local review of this branch without a pull request */
  review?: GitReview;
  reload: () => void;
}) {
  const st = view.status!;
  const branch = st.branch ?? "";
  const base = view.defaultBranch || "main";
  const origin = view.origin;
  const github = !!origin?.github;
  const onBase = !branch || branch === base || !!view.protected;
  const nFiles = diffs ? changedPaths(diffs).size : st.files.length;
  const staged = diffs?.staged.length ?? 0;
  const nCommits = commits?.length ?? 0;
  // the branch has something origin lacks (main without an upstream: nothing to say)
  const pushable = !!origin && !!branch && (st.upstream ? st.ahead > 0 : !onBase && nCommits > 0);
  const shippable = nFiles > 0 || nCommits > 0;
  const askKey = agent.id + ":" + branch;
  const askedAt = asked.get(askKey);
  const waiting = !!askedAt && Date.now() - askedAt < ASK_FOR;
  const [mode, setMode] = useState<"" | "commit" | "pr">("");
  const [busy, setBusy] = useState(false);
  const [, bump] = useState(0);

  // what the work is, in one sentence
  const title =
    nFiles > 0
      ? t("code.s.dirty", { n: nFiles })
      : nCommits > 0
        ? st.upstream && st.ahead === 0
          ? t("code.s.pushed", { n: nCommits })
          : t("code.s.committed", { n: nCommits })
        : st.upstream && st.ahead > 0
          ? t("code.s.unpushed", { n: st.ahead })
          : t("code.s.clean");
  const where = onBase ? (
    <>{t("code.s.on")} <code>{branch || "HEAD"}</code>{github && shippable ? " · " + t("code.s.newBranch") : ""}</>
  ) : (
    <><code>{branch}</code> → <code>{base}</code></>
  );

  const askAgent = async () => {
    const steps: string[] = [];
    if (onBase) steps.push(t("code.ask.branch", { branch: branch || "HEAD" }));
    if (nFiles > 0) steps.push(t("code.ask.commit", { n: nFiles }));
    steps.push(t("code.ask.open", { base }));
    const text = [t("code.ask.intro"), ...steps.map((s, i) => `${i + 1}. ${s}`), t("code.ask.outro")].join("\n");
    if (await tellAgent(agent, session, text)) {
      asked.set(askKey, Date.now());
      bump((x) => x + 1);
    }
  };

  const push = async () => {
    if (!(await confirmDialog({ text: t("git.pushQ", { branch }), ok: t("git.push") }))) return;
    setBusy(true);
    try {
      await API.gitPush({ agent: agent.id, branch });
      toast(t("git.pushed", { branch }));
    } catch (e) {
      toast(errOf(e));
    } finally {
      setBusy(false);
      reload();
    }
  };

  const tokenMissing = github && !origin!.token;
  return (
    <div className="cdcard">
      <div className="cdstat">
        <span className={"cdstatic" + (shippable ? " live" : "")}>{shippable ? <GitBranch size={15} /> : <CircleCheck size={15} />}</span>
        <div className="cdstatt">
          <b>{title}</b>
          <span className="small">{where}</span>
        </div>
      </div>

      {github && shippable && mode !== "pr" && (
        <div className="cdship">
          {waiting ? (
            <div className="cdwait" role="status">
              <LoaderCircle size={14} className="cdspin" />
              <span className="g">{t("code.asked", { name: agent.name })}</span>
              <button type="button" className="linkbtn small" onClick={() => (asked.delete(askKey), bump((x) => x + 1))}>{t("code.askAgain")}</button>
            </div>
          ) : (
            <button type="button" className="btn pri cdprimary" disabled={tokenMissing} onClick={() => void askAgent()}>
              <GitPullRequest size={14} />
              {t("code.createPR")}
            </button>
          )}
          {tokenMissing ? (
            <p className="cdnote small">
              <KeyRound size={12} />
              <span className="g">{t("code.noToken", { host: origin!.host })}</span>
              <button type="button" className="linkbtn small" onClick={() => openSettings("git")}>{t("code.toSettings")}</button>
            </p>
          ) : (
            <p className="cdnote small">
              <span className="g">{waiting ? "" : t("code.createHint")}</span>
              <button type="button" className="linkbtn small" onClick={() => setMode("pr")}>
                <PenLine size={11} />
                {t("code.selfFill")}
              </button>
            </p>
          )}
        </div>
      )}
      {mode === "pr" && <PRForm agent={agent} view={view} onBase={onBase} nFiles={nFiles} review={review} onDone={() => (setMode(""), reload())} />}

      {(nFiles > 0 || pushable) && mode !== "pr" && (
        <div className="cdsecondary">
          {nFiles > 0 && (
            <button type="button" className={"btn sm" + (!github ? " pri" : " ghost")} aria-expanded={mode === "commit"} onClick={() => setMode(mode === "commit" ? "" : "commit")}>
              <GitCommitHorizontal size={13} />
              {github ? t("code.commitOnly") : t("git.commit")}
            </button>
          )}
          {pushable && (
            <button type="button" className={"btn sm" + (!github && nFiles === 0 ? " pri" : " ghost")} disabled={busy} onClick={() => void push()} title={t("git.pushTitle")}>
              <Upload size={12} />
              {t("git.push")}
              {st.upstream && st.ahead > 0 && <span className="cdcount">↑{st.ahead}</span>}
            </button>
          )}
        </div>
      )}
      {mode === "commit" && <CommitForm agent={agent} branch={branch} protectedBranch={!!view.protected} staged={staged} onDone={() => (setMode(""), reload())} />}

      {!github && (
        <p className="cdnote small cdnogh">
          <GitPullRequest size={12} />
          <span className="g">{origin ? t("code.notGitHub", { host: origin.host || "origin" }) : t("code.noOrigin")}</span>
          <button type="button" className="ix" title={t("code.gitSettings")} aria-label={t("code.gitSettings")} onClick={() => openSettings("git")}>
            <Settings2 size={12} />
          </button>
        </p>
      )}
    </div>
  );
}

/** A commit, in place: the message, and (when some is staged) whether only that. */
export function CommitForm({ agent, branch, protectedBranch, staged, worktree, onDone, push }: {
  agent: AgentView; branch: string; protectedBranch: boolean; staged: number; worktree?: string; onDone: () => void;
  /** commit, then push the branch (推送更新) */
  push?: boolean;
}) {
  const [msg, setMsg] = useState("");
  const [onlyStaged, setOnlyStaged] = useState(false);
  const [busy, setBusy] = useState(false);
  const go = async () => {
    if (!msg.trim() || busy) return;
    if (protectedBranch && !(await confirmDialog({ text: t("git.protectedCommitQ", { branch }), ok: t("git.commit") }))) return;
    if (push && !(await confirmDialog({ text: t("git.pushQ", { branch }), ok: t("code.commitPush") }))) return;
    setBusy(true);
    try {
      const c = await API.gitCommit({ agent: agent.id, worktree, message: msg.trim(), all: !onlyStaged });
      if (push) {
        await API.gitPush({ agent: agent.id, worktree, branch });
        toast(t("code.committedPushed", { short: c.short }));
      } else toast(t("git.committed", { short: c.short }));
      setMsg("");
      onDone();
    } catch (e) {
      toast(errOf(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="cdform">
      <textarea value={msg} rows={2} autoFocus={!push} placeholder={t("git.msgPh")} onChange={(e) => setMsg(e.target.value)}
        onKeyDown={(e) => {
          if ((e.metaKey || e.ctrlKey) && e.key === "Enter") (e.preventDefault(), void go());
        }} />
      <div className="gxcrow">
        {staged > 0 && (
          <label className="gxcheck small">
            <input type="checkbox" checked={onlyStaged} onChange={(e) => setOnlyStaged(e.target.checked)} />
            {t("code.onlyStaged", { n: staged })}
          </label>
        )}
        <span className="sp" />
        <button type="button" className="btn sm pri" disabled={!msg.trim() || busy} onClick={() => void go()} title="⌘↵">
          {push ? <Upload size={12} /> : <GitCommitHorizontal size={13} />}
          {push ? t("code.commitPush") : t("git.commitTo", { branch: branch || "HEAD" })}
        </button>
      </div>
      {protectedBranch && !push && <p className="cdnote small warn">{t("git.protectedHint", { branch })}</p>}
    </div>
  );
}

/** Opening the pull request from here: title, description, draft; a new
 *  branch when the work is on main. The push asks first. */
function PRForm({ agent, view, onBase, nFiles, review, onDone }: {
  agent: AgentView; view: GitRepoView; onBase: boolean; nFiles: number; review?: GitReview; onDone: () => void;
}) {
  const base = view.defaultBranch || "main";
  const [title, setTitle] = useState(review?.title ?? view.log?.[0]?.subject ?? "");
  const [body, setBody] = useState(review?.body ?? "");
  const [draft, setDraft] = useState(false);
  const [branch, setBranch] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const newBranch = branch.trim() || branchName(title, agent.id);
  const source = onBase ? newBranch : view.status?.branch ?? "";
  const go = async () => {
    if (!title.trim() || busy) return;
    if (!(await confirmDialog({ text: t("code.prQ", { source, target: base }), ok: t("code.pushOpen") }))) return;
    setBusy(true);
    setErr("");
    try {
      if (onBase) await API.gitBranch({ agent: agent.id, name: newBranch });
      if (nFiles > 0) await API.gitCommit({ agent: agent.id, message: title.trim() + (body.trim() ? "\n\n" + body.trim() : ""), all: true });
      // the pull request hangs on a local review of the branch (opened
      // quietly; the person is its reviewer, nobody is asked)
      const rv = review && !onBase ? review : await API.gitOpenReview({ agent: agent.id, title: title.trim(), body: body.trim() || undefined, target: base });
      const out = await API.gitPRCreate(agent.id, rv.id, { title: title.trim(), draft });
      toast(t("code.prOpened", { n: out.pr?.number ?? "" }));
      onDone();
    } catch (e) {
      setErr(errOf(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="cdform">
      <label className="cdfl">
        <span className="small">{t("code.f.title")}</span>
        <input value={title} autoFocus onChange={(e) => setTitle(e.target.value)} placeholder={t("code.f.titlePh")} />
      </label>
      <label className="cdfl">
        <span className="small">{t("code.f.body")}</span>
        <textarea value={body} rows={3} onChange={(e) => setBody(e.target.value)} placeholder={t("code.f.bodyPh")} />
      </label>
      {onBase && (
        <label className="cdfl">
          <span className="small">{t("code.f.branch", { base })}</span>
          <input className="mono" value={branch} onChange={(e) => setBranch(e.target.value.replace(/\s+/g, "-"))} placeholder={branchName(title, agent.id)} />
        </label>
      )}
      <div className="cdflow small">
        <GitBranch size={12} />
        <span className="mono">{source || "…"}</span>
        <ArrowRight size={11} />
        <span className="mono">{base}</span>
        {nFiles > 0 && <span className="muted">· {t("code.f.commitsToo", { n: nFiles })}</span>}
      </div>
      {err && <p className="cderr small" role="alert">{err}</p>}
      <div className="gxcrow">
        <label className="gxcheck small">
          <input type="checkbox" checked={draft} onChange={(e) => setDraft(e.target.checked)} />
          {t("pr.draft")}
        </label>
        <span className="sp" />
        <button type="button" className="btn sm ghost" onClick={onDone}>{t("common.cancel")}</button>
        <button type="button" className="btn sm pri" disabled={!title.trim() || busy} onClick={() => void go()}>
          {busy ? <LoaderCircle size={12} className="cdspin" /> : <GitPullRequest size={12} />}
          {t("code.pushOpen")}
        </button>
      </div>
    </div>
  );
}
