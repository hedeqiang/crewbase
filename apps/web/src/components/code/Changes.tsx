// The uncommitted changes of a folder (the agent's, or a worktree's): one
// row a file with its +/- counts, opening in place to its diff. A file or
// a hunk can be discarded (or staged); with notes, a line takes a comment
// for the agent.

import { useEffect, useState } from "react";
import { ChevronDown, ChevronRight, MessageSquare, Minus, Plus, Undo2 } from "lucide-react";
import { API, type AgentView, type GitComment, type GitFileDiff, type GitHunk } from "../../api";
import { t } from "../../i18n";
import { toast, useStore } from "../../store";
import { confirmDialog } from "../Confirm";
import { FileBadge } from "../../lib/fileicons";
import { Skeleton } from "../Skeleton";
import { GitDiff, InlineComment, NoteComposer, StatusTag, lineText } from "./diff";
import { errOf, MidPath, useAct } from "./shared";
import type { Notes } from "./notes";

export interface Diffs {
  work: GitFileDiff[];
  staged: GitFileDiff[];
}

/** The folder's (or worktree's) unstaged and staged diffs, again on every
 *  repository change. */
export function useDiffs(agent: string, worktree: string | undefined, n: number, on = true): Diffs | null {
  const tick = useStore((s) => s.gitTick);
  const [d, setD] = useState<Diffs | null>(null);
  useEffect(() => {
    if (!on) return setD(null);
    let live = true;
    Promise.all([API.gitDiff(agent, { worktree, side: "work" }), API.gitDiff(agent, { worktree, side: "staged" })]).then(
      ([a, b]) => live && setD({ work: a.files ?? [], staged: b.files ?? [] }),
      (e) => live && toast(errOf(e)),
    );
    return () => void (live = false);
  }, [agent, worktree, tick, n, on]);
  return d;
}

export const totals = (fs: GitFileDiff[]) => fs.reduce((a, f) => ({ add: a.add + f.added, del: a.del + f.removed }), { add: 0, del: 0 });

/** The files of both diffs, once each (what a summary counts). */
export const changedPaths = (d: Diffs) => new Set([...d.work, ...d.staged].map((f) => f.path));

export function Changes({ agent, worktree, diffs, reload, notes, title }: {
  agent: AgentView;
  worktree?: string;
  diffs: Diffs | null;
  reload: () => void;
  /** set: lines take comments for the agent */
  notes?: Notes;
  title: string;
}) {
  const [open, setOpen] = useState<Set<string>>(new Set());
  const { busy, run } = useAct(reload);
  if (!diffs) return <div className="cdpad"><Skeleton kind="rows" n={3} /></div>;
  const { work, staged } = diffs;
  const all = [...work, ...staged];
  const sum = totals(all);
  const toggle = (k: string) => setOpen((s) => {
    const x = new Set(s);
    x.has(k) ? x.delete(k) : x.add(k);
    return x;
  });
  const files = (paths: string[], action: "stage" | "unstage" | "discard") => run(() => API.gitFiles({ agent: agent.id, worktree, paths, action }));
  const hunk = (path: string, h: GitHunk, action: "stage" | "unstage" | "discard") => run(() => API.gitHunk({ agent: agent.id, worktree, path, hunk: h.id, action }));
  const discard = async (paths: string[]) => {
    if (!(await confirmDialog({ text: t("git.discardQ", { n: paths.length, name: paths[0] }), ok: t("git.discard"), danger: true }))) return;
    void files(paths, "discard");
  };
  // the notes under their lines, as comments (pending, the person's)
  const noteComments = (path: string): GitComment[] =>
    (notes?.list ?? []).filter((x) => x.path === path).map((x) => ({ id: x.id, author: "person:owner", path: x.path, line: x.line, side: x.side, severity: "note", body: x.body, commit: "", round: 0, created: "" }));

  const row = (f: GitFileDiff, side: "work" | "staged") => {
    const k = side + ":" + f.path;
    const isOpen = open.has(k);
    const name = f.path.split("/").pop() ?? f.path;
    const dir = f.path.includes("/") ? f.path.slice(0, f.path.lastIndexOf("/")) : "";
    const cms = side === "work" ? noteComments(f.path) : [];
    return (
      <div key={k} className={"gxfile" + (isOpen ? " open" : "")}>
        <div className="gxfrow">
          <button type="button" className="gxfmain" onClick={() => toggle(k)} aria-expanded={isOpen}>
            {isOpen ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
            <FileBadge name={name} />
            <span className={"gxfname st-" + f.status}>{name}</span>
            {dir && <MidPath path={dir} className="gxfdir" />}
            <span className="sp" />
            {cms.length > 0 && (
              <span className="gxcmc on" title={t("code.notesOnFile", { n: cms.length })}>
                <MessageSquare size={11} />
                {cms.length}
              </span>
            )}
            <StatusTag s={f.status} />
            {!f.binary && (
              <span className="fxic">
                {f.added > 0 && <span className="add">+{f.added}</span>}
                {f.removed > 0 && <span className="del">−{f.removed}</span>}
              </span>
            )}
          </button>
          <span className="gxfacts">
            {side === "work" ? (
              <>
                <button type="button" className="ix dng" title={t("git.discard")} aria-label={t("git.discard")} disabled={busy} onClick={() => void discard([f.path])}>
                  <Undo2 size={12} />
                </button>
                <button type="button" className="ix" title={t("git.stage")} aria-label={t("git.stage")} disabled={busy} onClick={() => void files([f.path], "stage")}>
                  <Plus size={13} />
                </button>
              </>
            ) : (
              <button type="button" className="ix" title={t("git.unstage")} aria-label={t("git.unstage")} disabled={busy} onClick={() => void files([f.path], "unstage")}>
                <Minus size={13} />
              </button>
            )}
          </span>
        </div>
        {isOpen &&
          (f.binary ? (
            <div className="fxmsg small muted">{t("fx.binary")}</div>
          ) : f.hunks.length === 0 ? (
            <div className="fxmsg small muted">{t("fx.emptyFile")}</div>
          ) : (
            <GitDiff
              file={f}
              comments={cms}
              renderComment={(c) => <InlineComment c={c} sev={false} pending pendingLabel={t("code.toSend")} onDrop={() => notes?.drop(c.id)} />}
              composer={notes && side === "work" ? (line, s, close) => (
                <NoteComposer onCancel={close} onSave={(body) => (notes.add({ path: f.path, line, side: s, code: lineText(f, line, s), body }), close())} />
              ) : undefined}
              hunkBar={(h) =>
                side === "work" ? (
                  <span className="gxhbar">
                    <button type="button" className="gxha dng" disabled={busy}
                      onClick={async () => (await confirmDialog({ text: t("git.discardHunkQ"), ok: t("git.discard"), danger: true })) && hunk(f.path, h, "discard")}>
                      <Undo2 size={11} />
                      {t("git.discardHunk")}
                    </button>
                    <button type="button" className="gxha" disabled={busy} onClick={() => void hunk(f.path, h, "stage")}>
                      <Plus size={11} />
                      {t("git.stageHunk")}
                    </button>
                  </span>
                ) : (
                  <span className="gxhbar">
                    <button type="button" className="gxha" disabled={busy} onClick={() => void hunk(f.path, h, "unstage")}>
                      <Minus size={11} />
                      {t("git.unstageHunk")}
                    </button>
                  </span>
                )
              }
            />
          ))}
      </div>
    );
  };

  return (
    <section className="cdsec">
      <header className="cdsech">
        <span className="cdsect">{title}</span>
        {all.length > 0 && <span className="gxn">{new Set(all.map((f) => f.path)).size}</span>}
        <span className="sp" />
        {all.length > 0 && (
          <span className="fxic">
            {sum.add > 0 && <span className="add">+{sum.add}</span>}
            {sum.del > 0 && <span className="del">−{sum.del}</span>}
          </span>
        )}
        {work.length > 1 && (
          <button type="button" className="gxga" disabled={busy} onClick={() => void discard(work.map((f) => f.path))}>
            {t("git.discardAll")}
          </button>
        )}
      </header>
      {all.length === 0 ? (
        <p className="cdnone small">{t("git.clean")}</p>
      ) : (
        <>
          {work.map((f) => row(f, "work"))}
          {staged.length > 0 && (
            <>
              <div className="cdsub small">
                <span>{t("git.staged")}</span>
                <span className="sp" />
                <button type="button" className="gxga" disabled={busy} onClick={() => void files(staged.map((f) => f.path), "unstage")}>{t("git.unstageAll")}</button>
              </div>
              {staged.map((f) => row(f, "staged"))}
            </>
          )}
          {notes && work.length > 0 && notes.list.length === 0 && <p className="cdhint small">{t("code.lineHint")}</p>}
        </>
      )}
    </section>
  );
}
