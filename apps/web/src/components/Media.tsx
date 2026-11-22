// Images and videos an agent generated (features/media). The pictures are
// the answer: they are shown on their own (one large, several in a grid),
// a click opens them full size (← → between them, Esc closes), and what
// made them (the prompt, the model, the files) is behind the corner ⓘ.

import { useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { AlertCircle, ChevronLeft, ChevronRight, Download, FolderOpen, ImageOff, Info, X } from "lucide-react";
import { useRawURL } from "../api";
import { t } from "../i18n";
import type { MediaFile, MediaItem } from "../projection";
import { setState } from "../store";
import { fmtSize } from "./Attachments";

const openInFiles = (path: string) => setState({ fileOpen: path, panelOpen: true, panelTab: "files" });

/** "provider/model" → the model part a person recognises. */
const modelName = (m: string) => m.split("/").pop() ?? m;
const fileName = (p: string) => p.split("/").pop() ?? p;

function specLine(it: MediaItem): string {
  const s = it.spec ?? {};
  return [s.seconds ? t("media.seconds", { n: s.seconds }) : "", s.resolution ?? "", s.aspect ?? ""].filter(Boolean).join(" · ");
}

export function MediaCard({ it, inTurn }: { it: MediaItem; inTurn?: boolean }) {
  const video = it.kind === "video";
  const [view, setView] = useState<number | null>(null);
  const [info, setInfo] = useState(false);
  const n = it.files.length;
  // one line per prompt (several when the pictures came from several calls)
  const prompts = [...new Set(it.files.map((f) => f.prompt || it.prompt).filter(Boolean))];
  return (
    <div className={"mediaw" + (video ? " video" : "") + (inTurn ? " inturn" : "")}>
      {it.status === "pending" ? (
        <div className="mediapend" role="status">
          <span className="spin" />
          <span>
            <b>{t("media.making")}</b>
            <span className="small">{[specLine(it), t("media.makingNote")].filter(Boolean).join(" · ")}</span>
          </span>
        </div>
      ) : it.status === "failed" ? (
        <div className="mediafail">
          <AlertCircle size={14} />
          <span>{t("media.failed", { error: it.error ?? "" })}</span>
        </div>
      ) : video ? (
        it.files.map((f) => <VideoFile key={f.path} agent={it.agent} f={f} />)
      ) : (
        <div className="mediabox">
          <div className={"mediaimgs n" + Math.min(n, 4) + (n > 4 ? " many" : "")}>
            {it.files.map((f, i) => (
              <ImageFile key={f.path} agent={it.agent} f={f} onOpen={() => setView(i)} label={n > 1 ? t("media.imageN", { i: i + 1, n }) : t("media.image")} />
            ))}
          </div>
          {/* what made them: a corner button, not a row of its own */}
          {it.status === "done" && n > 0 && (
            <button type="button" className={"mediainfo" + (info ? " on" : "")} aria-expanded={info} onClick={() => setInfo(!info)} title={t("media.details")} aria-label={t("media.details")}>
              <Info size={14} />
            </button>
          )}
        </div>
      )}
      {info && (
        <div className="mediameta">
          {prompts.map((p, i) => (
            <p className="mprompt" key={i}>
              <span className="muted">{prompts.length > 1 ? t("media.promptN", { i: i + 1 }) : t("media.prompt")}</span> {p}
            </p>
          ))}
          <div className="mfiles">
            {it.model && <span className="mm">{modelName(it.model)}</span>}
            {it.files.map((f) => (
              <button key={f.path} type="button" className="linkbtn mono" title={t("media.openInFiles")} onClick={() => openInFiles(f.path)}>
                <FolderOpen size={12} />
                {fileName(f.path)}
                <span className="muted"> · {fmtSize(f.size)}</span>
              </button>
            ))}
          </div>
        </div>
      )}
      {view !== null && !video && <Viewer agent={it.agent} files={it.files} at={view} setAt={setView} />}
    </div>
  );
}

function ImageFile({ agent, f, onOpen, label }: { agent: string; f: MediaFile; onOpen: () => void; label: string }) {
  const src = useRawURL(agent, f.path);
  const [st, setSt] = useState<"" | "ok" | "gone">("");
  // deleted or moved since: say so instead of loading forever
  if (st === "gone")
    return (
      <span className="mimg gone" title={f.path}>
        <ImageOff size={18} />
        <span>{t("media.gone")}</span>
      </span>
    );
  return (
    <button type="button" className={"mimg" + (st === "ok" ? " ok" : "")} onClick={onOpen} aria-label={t("media.open", { what: label })}>
      {src ? <img src={src} alt={label} loading="lazy" decoding="async" onLoad={() => setSt("ok")} onError={() => setSt("gone")} /> : null}
    </button>
  );
}

/** Full size, one at a time: ← → (or the buttons) between them. */
function Viewer({ agent, files, at, setAt }: { agent: string; files: MediaFile[]; at: number; setAt: (i: number | null) => void }) {
  const f = files[at];
  const src = useRawURL(agent, f.path);
  const n = files.length;
  useEffect(() => {
    const on = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        setAt(null);
      } else if (e.key === "ArrowRight" && n > 1) setAt((at + 1) % n);
      else if (e.key === "ArrowLeft" && n > 1) setAt((at + n - 1) % n);
    };
    window.addEventListener("keydown", on, true);
    return () => window.removeEventListener("keydown", on, true);
  }, [at, n]);
  return createPortal(
    <div className="mviewer" role="dialog" aria-modal="true" aria-label={t("media.viewer")} onMouseDown={(e) => e.target === e.currentTarget && setAt(null)}>
      <div className="mvbar">
        {n > 1 && (
          <span className="mvn">
            {at + 1} / {n}
          </span>
        )}
        <span className="sp" />
        {src && (
          <a className="mvbtn" href={src} download={fileName(f.path)} title={t("media.download")} aria-label={t("media.download")}>
            <Download size={16} />
          </a>
        )}
        <button
          type="button"
          className="mvbtn"
          title={t("media.openInFiles")}
          aria-label={t("media.openInFiles")}
          onClick={() => {
            setAt(null);
            openInFiles(f.path);
          }}
        >
          <FolderOpen size={16} />
        </button>
        <button type="button" className="mvbtn" title={t("common.close")} aria-label={t("common.close")} onClick={() => setAt(null)}>
          <X size={18} />
        </button>
      </div>
      {n > 1 && (
        <button type="button" className="mvnav prev" aria-label={t("media.prev")} onClick={() => setAt((at + n - 1) % n)}>
          <ChevronLeft size={22} />
        </button>
      )}
      {src && <img key={f.path} className="mvimg" src={src} alt="" onMouseDown={(e) => e.stopPropagation()} />}
      {n > 1 && (
        <button type="button" className="mvnav next" aria-label={t("media.next")} onClick={() => setAt((at + 1) % n)}>
          <ChevronRight size={22} />
        </button>
      )}
      {n > 1 && (
        <div className="mvthumbs">
          {files.map((x, i) => (
            <Thumb key={x.path} agent={agent} f={x} on={i === at} onClick={() => setAt(i)} />
          ))}
        </div>
      )}
    </div>,
    document.body,
  );
}

function Thumb({ agent, f, on, onClick }: { agent: string; f: MediaFile; on: boolean; onClick: () => void }) {
  const src = useRawURL(agent, f.path);
  return (
    <button type="button" className={"mvthumb" + (on ? " on" : "")} onClick={onClick} aria-pressed={on}>
      {src && <img src={src} alt="" />}
    </button>
  );
}

function VideoFile({ agent, f }: { agent: string; f: MediaFile }) {
  const src = useRawURL(agent, f.path);
  return src ? <video className="mediavid" src={src} controls preload="metadata" playsInline aria-label={t("media.video")} /> : <span className="thumbph mediavid" />;
}
