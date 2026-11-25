// Before a pull request: where the work stands, with 提交 and 推送 (the
// push asks first). A pull request is opened in the conversation, by the
// agent; once there is one the panel follows it (PullRequest.tsx).

import { useState } from "react";
import { CircleCheck, GitBranch, GitCommitHorizontal, Upload } from "lucide-react";
import { API, type AgentView, type GitCommit, type GitRepoView } from "../../api";
import { t } from "../../i18n";
import { toast } from "../../store";
import { confirmDialog } from "../Confirm";
import { changedPaths, type Diffs } from "./Changes";
import { errOf } from "./shared";

export function Ship({ agent, view, diffs, commits, reload }: {
  agent: AgentView;
  view: GitRepoView;
  diffs: Diffs | null;
  /** the branch's commits not on the default branch (null: on it, or loading) */
  commits: GitCommit[] | null;
  reload: () => void;
}) {
  const st = view.status!;
  const branch = st.branch ?? "";
  const base = view.defaultBranch || "main";
  const origin = view.origin;
  const onBase = !branch || branch === base || !!view.protected;
  const nFiles = diffs ? changedPaths(diffs).size : st.files.length;
  const staged = diffs?.staged.length ?? 0;
  const nCommits = commits?.length ?? 0;
  // the branch has something origin lacks (main without an upstream: nothing to say)
  const pushable = !!origin && !!branch && (st.upstream ? st.ahead > 0 : !onBase && nCommits > 0);
  const shippable = nFiles > 0 || nCommits > 0;
  const [mode, setMode] = useState<"" | "commit">("");
  const [busy, setBusy] = useState(false);

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
    <>{t("code.s.on")} <code>{branch || "HEAD"}</code></>
  ) : (
    <><code>{branch}</code> → <code>{base}</code></>
  );

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

  return (
    <div className="cdcard">
      <div className="cdstat">
        <span className={"cdstatic" + (shippable ? " live" : "")}>{shippable ? <GitBranch size={15} /> : <CircleCheck size={15} />}</span>
        <div className="cdstatt">
          <b>{title}</b>
          <span className="small">{where}</span>
        </div>
      </div>

      {(nFiles > 0 || pushable) && (
        <div className="cdsecondary">
          {nFiles > 0 && (
            <button type="button" className="btn sm pri" aria-expanded={mode === "commit"} onClick={() => setMode(mode === "commit" ? "" : "commit")}>
              <GitCommitHorizontal size={13} />
              {t("git.commit")}
            </button>
          )}
          {pushable && (
            <button type="button" className={"btn sm" + (nFiles === 0 ? " pri" : " ghost")} disabled={busy} onClick={() => void push()} title={t("git.pushTitle")}>
              <Upload size={12} />
              {t("git.push")}
              {st.upstream && st.ahead > 0 && <span className="cdcount">↑{st.ahead}</span>}
            </button>
          )}
        </div>
      )}
      {mode === "commit" && <CommitForm agent={agent} branch={branch} protectedBranch={!!view.protected} staged={staged} onDone={() => (setMode(""), reload())} />}

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
