// A file's diff (highlighted, hunk by hunk) with a bar above each hunk and,
// when lines take comments, a + beside each line that opens a composer
// under it. Comments already made show under their lines.

import { Fragment, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { MessageSquarePlus, Send } from "lucide-react";
import type { GitComment, GitFileDiff, GitFileStatus, GitHunk, GitSeverity, Hunk } from "../../api";
import { t, type Key } from "../../i18n";
import { ago } from "../../format";
import { useHighlighted } from "../Files";
import { Markdown } from "../Markdown";
import { Who } from "./shared";

const toHunk = (h: GitHunk): Hunk => ({ oldStart: h.oldStart, oldLines: h.oldLines, newStart: h.newStart, newLines: h.newLines, lines: h.lines.map((l) => ({ t: l.k, s: l.t, o: l.o, n: l.n })) });

export type Side = "new" | "old";

/** The code at a line of a diff (for a comment's context). */
export function lineText(file: GitFileDiff, line: number, side: Side): string {
  for (const h of file.hunks)
    for (const l of h.lines) {
      if (side === "old" ? l.k === "-" && l.o === line : l.k !== "-" && l.n === line) return l.t;
    }
  return "";
}

export function GitDiff({ file, hunkBar, comments, renderComment, composer }: {
  file: GitFileDiff;
  hunkBar?: (h: GitHunk) => ReactNode;
  comments?: GitComment[];
  renderComment?: (c: GitComment) => ReactNode;
  /** set: lines take comments; the composer for one, closed with close() */
  composer?: (line: number, side: Side, close: () => void) => ReactNode;
}) {
  const hunks = useMemo(() => file.hunks.map(toHunk), [file]);
  const hl = useHighlighted(hunks, file.path);
  const [open, setOpen] = useState<{ line: number; side: Side } | null>(null);
  let oi = 0;
  let ni = 0;
  const at = (line: number, side: Side) => (comments ?? []).filter((c) => c.path === file.path && c.line === line && (c.side ?? "new") === side);
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
                const side: Side = l.k === "-" ? "old" : "new";
                const line = side === "old" ? l.o ?? 0 : l.n ?? 0;
                const here = at(line, side);
                const writing = open && open.line === line && open.side === side;
                return (
                  <Fragment key={hi + ":" + li}>
                    <tr className={(l.k === "+" ? "ad" : l.k === "-" ? "rm" : "") + (here.length ? " has-cm" : "") + (writing ? " writing" : "")}>
                      <td className="ln">{l.o ?? ""}</td>
                      <td className="ln gxln">
                        {l.n ?? ""}
                        {composer && line > 0 && (
                          <button type="button" className="gxadd" aria-label={t("git.addComment")} title={t("git.addComment")} onClick={() => setOpen({ line, side })}>
                            <MessageSquarePlus size={11} />
                          </button>
                        )}
                      </td>
                      <td className="mk">{l.k === " " ? "" : l.k === "-" ? "−" : "+"}</td>
                      <td className="lc" dangerouslySetInnerHTML={{ __html: html || " " }} />
                    </tr>
                    {(here.length > 0 || writing) && (
                      <tr className="gxcmrow">
                        <td colSpan={4}>
                          {renderComment && here.map((c) => <Fragment key={c.id}>{renderComment(c)}</Fragment>)}
                          {writing && composer?.(line, side, () => setOpen(null))}
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

export function StatusTag({ s }: { s: GitFileStatus["status"] }) {
  const mark: Record<string, string> = { modified: "M", added: "A", deleted: "D", renamed: "R", untracked: "U", conflicted: "!", typechange: "T" };
  return (
    <span className={"fxst st-" + (s === "untracked" ? "added" : s)} title={t(`git.st.${s}` as Key)}>
      {mark[s]}
    </span>
  );
}

/** A comment for the agent: words only, sent with the line it is about. */
export function NoteComposer({ onCancel, onSave }: { onCancel: () => void; onSave: (body: string) => void }) {
  const [body, setBody] = useState("");
  const ref = useRef<HTMLTextAreaElement>(null);
  useEffect(() => ref.current?.focus(), []);
  const save = () => body.trim() && onSave(body.trim());
  return (
    <div className="gxcomposer">
      <textarea ref={ref} rows={2} value={body} placeholder={t("code.notePh")} onChange={(e) => setBody(e.target.value)}
        onKeyDown={(e) => {
          if ((e.metaKey || e.ctrlKey) && e.key === "Enter") (e.preventDefault(), save());
          if (e.key === "Escape") onCancel();
        }} />
      <div className="gxcrow">
        <span className="small muted">{t("code.noteHint")}</span>
        <span className="sp" />
        <button type="button" className="btn sm ghost" onClick={onCancel}>{t("common.cancel")}</button>
        <button type="button" className="btn sm pri" disabled={!body.trim()} onClick={save} title="⌘↵">
          <Send size={12} />
          {t("code.noteAdd")}
        </button>
      </div>
    </div>
  );
}

const SEVS: GitSeverity[] = ["blocker", "major", "minor", "nit", "note"];

/** A review's line comment: a severity and words, submitted with the verdict. */
export function SevComposer({ onCancel, onSave }: { onCancel: () => void; onSave: (sev: GitSeverity, body: string) => void }) {
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

/** A comment under its line (or in a review's conversation). */
export function InlineComment({ c, onResolve, pending, onDrop, sev = true, pendingLabel }: {
  c: GitComment; onResolve?: (resolved: boolean) => void; pending?: boolean; onDrop?: () => void; sev?: boolean; pendingLabel?: string;
}) {
  return (
    <div className={"gxcm" + (c.resolved ? " done" : "") + (pending ? " pending" : "")}>
      <div className="gxcmh">
        <Who actor={c.author} size={16} />
        {sev && <span className={"gxsevtag sev-" + c.severity}>{t(`git.sev.${c.severity}` as Key)}</span>}
        {pending && <span className="gxsevtag pend">{pendingLabel ?? t("git.pending")}</span>}
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
