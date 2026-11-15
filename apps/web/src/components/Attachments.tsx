// Conversation attachments: the composer's pending files (picked, pasted
// or dropped; uploaded into the agent workspace right away) and the files
// shown in a sent message.

import { AlertCircle, File, FileImage, FileSpreadsheet, FileText, Presentation, X } from "lucide-react";
import { uploadFile, useRawURL, type Attachment } from "../api";
import { t } from "../i18n";
import { getState, setState, toast, type PendingFile } from "../store";

export const MAX_FILES = 10;
const MAX_BYTES = 30 << 20;

export function fmtSize(n: number): string {
  if (n >= 1 << 20) return (n / (1 << 20)).toFixed(1) + " MB";
  if (n >= 1 << 10) return Math.round(n / (1 << 10)) + " KB";
  return n + " B";
}

const IMAGE = /^image\/(png|jpeg|gif|webp)$/;

function FileIcon({ mime, name, size = 16 }: { mime: string; name: string; size?: number }) {
  const ext = name.toLowerCase().split(".").pop() ?? "";
  if (mime.startsWith("image/")) return <FileImage size={size} />;
  if (/^(xlsx?|csv|tsv)$/.test(ext)) return <FileSpreadsheet size={size} />;
  if (/^pptx?$/.test(ext)) return <Presentation size={size} />;
  if (mime === "application/pdf" || /^(docx?|md|txt)$/.test(ext) || mime.startsWith("text/")) return <FileText size={size} />;
  return <File size={size} />;
}

let seq = 0;

function update(session: string, id: number, patch: Partial<PendingFile>) {
  setState((s) => ({ attach: { ...s.attach, [session]: (s.attach[session] ?? []).map((f) => (f.id === id ? { ...f, ...patch } : f)) } }));
}

/** Adds files to a session's next message and uploads them now. */
export function addFiles(session: string, agent: string, files: File[]) {
  const have = getState().attach[session] ?? [];
  if (have.length + files.length > MAX_FILES) {
    toast(t("att.tooMany", { n: MAX_FILES }));
    files = files.slice(0, Math.max(0, MAX_FILES - have.length));
  }
  for (const f of files) {
    if (f.size > MAX_BYTES) {
      toast(t("att.tooBig", { name: f.name }));
      continue;
    }
    const id = ++seq;
    // pasted screenshots arrive as "image.png"; give them a dated name
    const name = f.name && f.name !== "image.png" ? f.name : `${t("att.screenshot")} ${new Date().toLocaleString("sv").replace(/[: ]/g, (c) => (c === ":" ? "." : " "))}.png`;
    const p: PendingFile = { id, name, size: f.size, mime: f.type, state: "up", preview: IMAGE.test(f.type) ? URL.createObjectURL(f) : undefined };
    setState((s) => ({ attach: { ...s.attach, [session]: [...(s.attach[session] ?? []), p] } }));
    uploadFile(agent, f, name).then(
      (att) => update(session, id, { state: "ok", att, mime: att.mime }),
      (e) => update(session, id, { state: "err", error: e?.message ?? String(e) }),
    );
  }
}

export function removeFile(session: string, id: number) {
  setState((s) => {
    const list = s.attach[session] ?? [];
    const f = list.find((x) => x.id === id);
    if (f?.preview) URL.revokeObjectURL(f.preview);
    return { attach: { ...s.attach, [session]: list.filter((x) => x.id !== id) } };
  });
}

export function clearFiles(session: string) {
  setState((s) => {
    for (const f of s.attach[session] ?? []) if (f.preview) URL.revokeObjectURL(f.preview);
    const { [session]: _, ...attach } = s.attach;
    return { attach };
  });
}

/** Files pasted or dropped: real files only (not text or links). */
export function filesFrom(dt: DataTransfer | null): File[] {
  if (!dt) return [];
  const out: File[] = [];
  for (const it of Array.from(dt.items ?? [])) {
    if (it.kind === "file") {
      const f = it.getAsFile();
      if (f) out.push(f);
    }
  }
  return out.length ? out : Array.from(dt.files ?? []);
}

/** Chips above the composer's text box. */
export function PendingChips({ session, files }: { session: string; files: PendingFile[] }) {
  if (!files.length) return null;
  return (
    <div className="attrow" aria-label={t("chat.attach")}>
      {files.map((f) => (
        <div key={f.id} className={"attchip" + (f.state === "err" ? " err" : "")} title={f.state === "err" ? t("att.failed", { error: f.error ?? "" }) : f.att ? t("att.saved", { path: f.att.path }) : f.name}>
          {f.preview ? <img src={f.preview} alt="" /> : <span className="ai">{f.state === "err" ? <AlertCircle size={16} /> : <FileIcon mime={f.mime} name={f.name} />}</span>}
          <span className="an">
            <b>{f.name}</b>
            <span>{f.state === "up" ? t("att.uploading") : f.state === "err" ? t("att.failed", { error: f.error ?? "" }) : fmtSize(f.size)}</span>
          </span>
          {f.state === "up" && <span className="spin sm" />}
          <button type="button" className="ax" aria-label={t("att.remove", { name: f.name })} onClick={() => removeFile(session, f.id)}>
            <X size={12} />
          </button>
        </div>
      ))}
    </div>
  );
}

/** Attachments in a sent message: image thumbnails and file chips. */
export function MsgAttachments({ agent, items }: { agent?: string; items: Attachment[] }) {
  if (!items.length || !agent) return null;
  const images = items.filter((a) => IMAGE.test(a.mime));
  const files = items.filter((a) => !IMAGE.test(a.mime));
  return (
    <div className="msgatt">
      {images.length > 0 && (
        <div className="attimgs">
          {images.map((a) => (
            <Thumb key={a.path} agent={agent} a={a} />
          ))}
        </div>
      )}
      {files.map((a) => (
        <button
          key={a.path}
          type="button"
          className="attchip sent"
          title={t("att.saved", { path: a.path })}
          onClick={() => setState({ fileOpen: a.path, panelOpen: true, panelTab: "files" })}
        >
          <span className="ai">
            <FileIcon mime={a.mime} name={a.name} />
          </span>
          <span className="an">
            <b>{a.name}</b>
            <span>{fmtSize(a.size)}</span>
          </span>
        </button>
      ))}
    </div>
  );
}

function Thumb({ agent, a }: { agent: string; a: Attachment }) {
  const src = useRawURL(agent, a.path);
  return (
    <a href={src} target="_blank" rel="noreferrer" title={t("att.open", { name: a.name })}>
      {src ? <img src={src} alt={a.name} loading="lazy" /> : <span className="thumbph" />}
    </a>
  );
}
