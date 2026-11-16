// Images and videos an agent generated (features/media): shown inline in
// the conversation, opened full size or in the Files tab.

import { AlertCircle, Clapperboard, FolderOpen, ImageIcon } from "lucide-react";
import { useRawURL } from "../api";
import { t } from "../i18n";
import type { FeedItem, MediaFile } from "../projection";
import { setState } from "../store";
import { fmtSize } from "./Attachments";

type Item = Extract<FeedItem, { k: "media" }>;

const openInFiles = (path: string) => setState({ fileOpen: path, panelOpen: true, panelTab: "files" });

/** "provider/model" → the model part a person recognises. */
const modelName = (m: string) => m.split("/").pop() ?? m;

function specLine(it: Item): string {
  const s = it.spec ?? {};
  return [s.seconds ? t("media.seconds", { n: s.seconds }) : "", s.resolution ?? "", s.aspect ?? ""].filter(Boolean).join(" · ");
}

export function MediaCard({ it }: { it: Item }) {
  const video = it.kind === "video";
  return (
    <div className={"mediaw" + (video ? " video" : "")}>
      <div className="mediah">
        {video ? <Clapperboard size={14} /> : <ImageIcon size={14} />}
        <span className="mp" title={it.prompt}>
          {it.prompt || t(video ? "media.video" : "media.image")}
        </span>
        {it.model && <span className="mm">{modelName(it.model)}</span>}
      </div>
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
        <div className={"mediaimgs n" + Math.min(it.files.length, 4)}>
          {it.files.map((f) => (
            <ImageFile key={f.path} agent={it.agent} f={f} />
          ))}
        </div>
      )}
      {it.status === "done" && it.files.length > 0 && (
        <div className="mediaf">
          {it.files.map((f) => (
            <button key={f.path} type="button" className="linkbtn mono" title={t("media.openInFiles")} onClick={() => openInFiles(f.path)}>
              <FolderOpen size={12} />
              {f.path.split("/").pop()}
              <span className="muted"> · {fmtSize(f.size)}</span>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

function ImageFile({ agent, f }: { agent: string; f: MediaFile }) {
  const src = useRawURL(agent, f.path);
  const name = f.path.split("/").pop() ?? f.path;
  return (
    <a href={src} target="_blank" rel="noreferrer" title={t("att.open", { name })}>
      {src ? <img src={src} alt={name} loading="lazy" /> : <span className="thumbph" />}
    </a>
  );
}

function VideoFile({ agent, f }: { agent: string; f: MediaFile }) {
  const src = useRawURL(agent, f.path);
  return src ? (
    <video className="mediavid" src={src} controls preload="metadata" playsInline aria-label={f.path.split("/").pop()} />
  ) : (
    <span className="thumbph mediavid" />
  );
}
