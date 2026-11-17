import { useEffect, useState } from "react";
import { AlertCircle, ArrowDown, ArrowDownToLine, Check, RotateCw, X } from "lucide-react";
import type { DesktopUpdateState } from "../api";
import { openExternal } from "../api";
import { getLang, t } from "../i18n";
import { downloadUpdate, openUpdateNotes, percent, restartToUpdate, useDesktopUpdate, useUpdateNotesOpen } from "../updates";
import { Markdown } from "./Markdown";

const RELEASES = "https://github.com/hedeqiang/crewbase/releases";

/** A ring that fills with the download; spins while the size is unknown. */
export function UpdateRing({ progress, spin, size = 22 }: { progress?: number; spin?: boolean; size?: number }) {
  const r = 9;
  const c = 2 * Math.PI * r;
  const p = Math.max(0, Math.min(1, progress ?? 0));
  return (
    <svg className={"updring" + (spin ? " spin" : "")} width={size} height={size} viewBox="0 0 22 22" aria-hidden="true">
      <circle className="trk" cx="11" cy="11" r={r} />
      <circle className="arc" cx="11" cy="11" r={r} strokeDasharray={c} strokeDashoffset={spin ? c * 0.72 : c * (1 - p)} />
    </svg>
  );
}

/** What of the state the sidebar shows; null hides the pill. A check the
 *  person did not ask for that fails, or finds nothing, stays quiet. */
function pillKind(st: DesktopUpdateState | null): string | null {
  if (!st) return null;
  switch (st.state) {
    case "available":
    case "downloading":
    case "ready":
      return st.state;
    case "checking":
      return st.user ? "checking" : null;
    case "error":
      return st.errorDuring === "download" || st.user ? "error" : null;
    case "idle":
      return st.user && st.upToDate ? "upToDate" : null;
  }
  return null;
}

/** The update, in place in the sidebar above the profile: downloading with
 *  a ring, then "重启以完成更新". */
export function UpdatePill() {
  const st = useDesktopUpdate();
  const kind = pillKind(st);
  // a dismissed message stays hidden until the state changes
  const key = st ? [kind, st.version, st.error, st.lastCheck].join("|") : "";
  const [dismissed, setDismissed] = useState("");
  useEffect(() => {
    if (kind !== "upToDate") return;
    const id = setTimeout(() => setDismissed(key), 4000);
    return () => clearTimeout(id);
  }, [kind, key]);
  if (!st || !kind || dismissed === key) return null;
  const v = st.version ?? "";
  const pct = percent(st);
  const notesLink = st.version ? (
    <button className="updlink" type="button" onClick={() => openUpdateNotes()}>
      {t("upd.notes")}
    </button>
  ) : null;
  const close = (
    <button className="ib updx" type="button" aria-label={t("common.close")} title={t("common.close")} onClick={() => setDismissed(key)}>
      <X size={13} />
    </button>
  );
  let body;
  switch (kind) {
    case "checking":
      body = (
        <>
          <span className="updic">
            <UpdateRing spin />
          </span>
          <span className="g">
            <b>{t("upd.stChecking")}</b>
          </span>
        </>
      );
      break;
    case "upToDate":
      body = (
        <>
          <span className="updic ok">
            <Check size={13} />
          </span>
          <span className="g">
            <b>{t("upd.upToDate")}</b>
            <span>{t("upd.version", { v: st.current })}</span>
          </span>
          {close}
        </>
      );
      break;
    case "available":
      body = (
        <>
          <span className="updic">
            <ArrowDownToLine size={13} />
          </span>
          <span className="g">
            <b>{t("upd.stAvailable", { v })}</b>
            {notesLink}
          </span>
          <button className="btn sm" type="button" onClick={downloadUpdate}>
            {t("upd.download")}
          </button>
        </>
      );
      break;
    case "downloading":
      body = (
        <>
          <span className="updic ring" role="progressbar" aria-valuemin={0} aria-valuemax={100} aria-valuenow={pct} aria-label={t("upd.pillDownloading")}>
            <UpdateRing progress={st.progress} spin={!st.total} />
            <ArrowDown size={10} className="bob" />
          </span>
          <span className="g">
            <b>{t("upd.pillDownloading")}</b>
            <span className="num">{st.progress >= 1 ? t("upd.stInstalling", { v }) : `${v} · ${pct}%`}</span>
          </span>
        </>
      );
      break;
    case "ready":
      body = (
        <>
          <span className="updic go">
            <RotateCw size={13} />
          </span>
          <span className="g">
            <button className="updmain" type="button" onClick={() => void restartToUpdate()} title={t("upd.installQuiet")}>
              {t("upd.restart")}
            </button>
            <span>
              {v}
              {notesLink && " · "}
              {notesLink}
            </span>
          </span>
        </>
      );
      break;
    case "error":
      body = (
        <>
          <span className="updic warn">
            <AlertCircle size={13} />
          </span>
          <span className="g">
            <b>{st.errorDuring === "download" ? t("upd.pillDownloadFailed") : t("upd.pillCheckFailed")}</b>
            <span title={st.error}>{st.error}</span>
          </span>
          <button className="btn sm" type="button" onClick={downloadUpdate}>
            {t("common.retry")}
          </button>
          {close}
        </>
      );
      break;
  }
  return (
    <div className="updpill" data-kind={kind} aria-live="polite">
      {body}
    </div>
  );
}

/** The release notes of the update found, on request ("查看更新内容"). */
export function UpdateNotes() {
  const open = useUpdateNotesOpen();
  const st = useDesktopUpdate();
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && openUpdateNotes(false);
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open]);
  if (!open || !st?.version) return null;
  const close = () => openUpdateNotes(false);
  const v = st.version;
  return (
    <>
      <div className="scrim" onClick={close} />
      <div className="modal updnotesdlg" role="dialog" aria-modal="true" aria-label={t("upd.notesTitle", { v })}>
        <div className="mhd">
          <h3>{t("upd.notesTitle", { v })}</h3>
          <button className="ib" type="button" aria-label={t("common.close")} onClick={close}>
            <X size={16} />
          </button>
        </div>
        <div className="mbd">
          {/* CHANGELOG.md is written in Chinese: say so in English */}
          {getLang() === "en" && st.notes && /[一-鿿]/.test(st.notes) && <p className="small updnoteslang">{t("upd.notesZh")}</p>}
          <div className="updnotesbody">{st.notes?.trim() ? <Markdown text={st.notes} /> : <p className="small">{t("upd.notesEmpty")}</p>}</div>
          <div className="mft">
            <button className="btn ghost" type="button" style={{ marginRight: "auto" }} onClick={() => openExternal(`${RELEASES}/tag/v${v}`)}>
              {t("upd.onGithub")}
            </button>
            {st.state === "ready" ? (
              <>
                <button className="btn ghost" type="button" onClick={close}>
                  {t("upd.later")}
                </button>
                <button
                  className="btn pri"
                  type="button"
                  onClick={() => {
                    close();
                    void restartToUpdate();
                  }}
                >
                  {t("upd.restart")}
                </button>
              </>
            ) : st.state === "available" ? (
              <button
                className="btn pri"
                type="button"
                onClick={() => {
                  close();
                  downloadUpdate();
                }}
              >
                {t("upd.download")}
              </button>
            ) : (
              <button className="btn" type="button" onClick={close}>
                {t("common.close")}
              </button>
            )}
          </div>
        </div>
      </div>
    </>
  );
}

