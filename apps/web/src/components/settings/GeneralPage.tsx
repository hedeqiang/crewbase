// 设置 › 通用: appearance, language, notifications on this device, system
// permissions, keeping running in the background, updates, and the
// defaults new agents get (run mode, models, workspace).

import { useEffect, useState, type ReactNode } from "react";
import { Head, Row } from "./Head";
import { API, desktop, type DesktopPermission, type DesktopUpdateState, type UpdateInfo, type Settings as SettingsData } from "../../api";
import { getLang, t, type Key, type Lang } from "../../i18n";
import { ago, modeLabel } from "../../format";
import { setBubbles, setLang, setLinkCards, setState, toast, useStore, type Bubbles } from "../../store";
import { shortPath } from "../Conversation";
import { askNotify, notifyPermission, notifySupported } from "../../companion";
import { Markdown } from "../Markdown";
import { UpdateRing } from "../Update";
import { checkForUpdates, downloadUpdate, inPlaceUpdates, openUpdateNotes, percent, restartToUpdate, useDesktopUpdate } from "../../updates";
import { ThemeSeg } from "../SessionList";

/** 关闭窗口后继续运行: the desktop app's setting (the tray menu has it too);
 *  shown only where it applies. */
function KeepRunning() {
  const d = desktop();
  const [on, setOn] = useState<boolean | null>(null);
  useEffect(() => {
    d?.keepRunning?.().then(setOn, () => setOn(null));
  }, []);
  if (!d?.keepRunning || on === null) return null;
  const flip = () =>
    d.setKeepRunning?.(!on).then(setOn, (e: any) => toast(e?.message ?? String(e)));
  return (
    <Row title={t("set.keepRunning")} desc={t("set.keepRunningDesc")}>
      <button className="tg" type="button" role="switch" aria-checked={on} aria-label={t("set.keepRunning")} onClick={() => void flip()} />
    </Row>
  );
}

/** 显示链接预览: cards under links in messages (on by default). */
function LinkCardsRow() {
  const on = useStore((s) => s.linkCards);
  return (
    <Row title={t("set.linkCards")} desc={t("set.linkCardsDesc")}>
      <button className="tg" type="button" role="switch" aria-checked={on} aria-label={t("set.linkCards")} onClick={() => setLinkCards(!on)} />
    </Row>
  );
}

/** Your message bubbles: grey, or the agent's colour (light / solid). */
function BubbleSeg() {
  const v = useStore((s) => s.bubbles);
  const sw = (b: Bubbles) =>
    b === "neutral" ? "var(--bubble)" : b === "tint" ? "color-mix(in srgb, var(--accent) 22%, var(--page))" : "var(--accent)";
  return (
    <div className="seg bubseg">
      {(["neutral", "tint", "solid"] as const).map((b) => (
        <button key={b} type="button" aria-pressed={v === b} onClick={() => setBubbles(b)}>
          <span className="sw" style={{ background: sw(b) }} aria-hidden />
          {t(`set.bubbles.${b}` as Key)}
        </button>
      ))}
    </div>
  );
}

/** Permissions without a system prompt: "Allow" opens System Settings with
 *  the guide panel (apps/desktop/permissions.go). */
const guidedPerms = new Set<string>(["full-disk-access", "accessibility", "screen-recording"]);

/** 系统权限: what macOS lets the app do, with a way to give each. Rows
 *  refresh when the window comes back from System Settings. */
function SystemPermissions() {
  const d = desktop();
  const [rows, setRows] = useState<DesktopPermission[] | null>(null);
  const [busy, setBusy] = useState("");
  const load = () => d?.permissions?.().then(setRows, () => setRows(null));
  useEffect(() => {
    void load();
    const back = () => document.visibilityState === "visible" && void load();
    window.addEventListener("focus", back);
    document.addEventListener("visibilitychange", back);
    return () => {
      window.removeEventListener("focus", back);
      document.removeEventListener("visibilitychange", back);
    };
  }, []);
  if (!rows || rows.length === 0) return null;
  const ask = async (id: string) => {
    setBusy(id);
    try {
      const r = await d?.requestPermission?.(id);
      if (r) setRows(r);
    } catch (e: any) {
      toast(e?.message ?? String(e));
    } finally {
      setBusy("");
    }
  };
  return (
    <>
      <div className="vsec">{t("perm.title")}</div>
      <div className="sbox">
        {rows.map((p) => (
          <Row key={p.id} title={t(`perm.${p.id}` as Key)} desc={t(`perm.${p.id}.desc` as Key)}>
            <span className="permr">
              <span className={"pill sm " + (p.status === "granted" ? "ok" : p.status === "denied" ? "red" : "")}>{t(`perm.st.${p.status}` as Key)}</span>
              {p.status !== "granted" &&
                (p.canRequest || guidedPerms.has(p.id) ? (
                  <button className="btn sm" type="button" disabled={busy === p.id} onClick={() => void ask(p.id)}>
                    {p.id === "notifications" ? t("perm.test") : t("perm.allow")}
                  </button>
                ) : (
                  <button className="btn sm" type="button" onClick={() => d?.openPermissionSettings?.(p.id)}>
                    {t("perm.open")}
                  </button>
                ))}
              {p.status === "granted" && p.id === "notifications" && (
                <button className="btn sm ghost" type="button" onClick={() => d?.openPermissionSettings?.(p.id)}>
                  {t("perm.open")}
                </button>
              )}
            </span>
          </Row>
        ))}
      </div>
    </>
  );
}


/** Browser notifications on this device (phones and other browsers). */
function NotifyHere() {
  const [perm, setPerm] = useState(notifyPermission());
  return (
    <Row title={t("comp.notifyOn")} desc={perm === "denied" ? t("comp.notifyDenied") : t("comp.notifyOnDesc")}>
      {perm === "granted" ? (
        <span className="pill ok">{t("comp.notifyEnabled")}</span>
      ) : (
        <button className="btn sm" type="button" disabled={perm === "denied"} onClick={() => void askNotify().then(setPerm)}>
          {t("comp.notifyAllow")}
        </button>
      )}
    </Row>
  );
}

export function General() {
  const manage = useStore((s) => s.tier === "desktop");
  const theme = useStore((s) => s.theme);
  const lang = useStore((s) => s.lang);
  const connected = useStore((s) => s.connected);
  return (
    <>
      <Head title={t("set.general")} sub={t("sub.general")} />
      <div className="vb">
        <div className="vsec">{t("set.appearance")}</div>
        <div className="sbox">
          <Row title={t("set.theme")} desc={t("set.themeDesc")}>
            <ThemeSeg theme={theme} />
          </Row>
          <Row title={t("set.bubbles")} desc={t("set.bubblesDesc")}>
            <BubbleSeg />
          </Row>
          <LinkCardsRow />
          <Row title={t("set.language")} desc={t("set.languageDesc")}>
            <span className="ssel">
              <select value={lang} onChange={(e) => setLang(e.target.value as Lang)}>
                <option value="zh">简体中文</option>
                <option value="en">English</option>
              </select>
            </span>
          </Row>
          {notifySupported() && <NotifyHere />}
        </div>
        {manage && desktop()?.keepRunning && (
          <>
            <div className="vsec">{t("set.startup")}</div>
            <div className="sbox">
              <KeepRunning />
            </div>
          </>
        )}
        {manage && desktop()?.permissions && <SystemPermissions />}
        {manage && (
          <>
            <div className="vsec">{t("set.defaults")}</div>
            <Defaults />
          </>
        )}
        <div className="vsec">{t("set.connection")}</div>
        <div className="sbox">
          <Row title={connected ? t("set.connected") : t("set.disconnected")} desc={window.location.host}>
            <i className={"hdot" + (connected ? " on" : "")} />
          </Row>
        </div>
        <div className="vsec">{t("set.about")}</div>
        <div className="sbox">
          <Updates />
          <Row title={t("set.license")} desc={t("set.licenseDesc")}>
            <span className="small muted">MIT</span>
          </Row>
        </div>
      </div>
    </>
  );
}

/** 关于 › 版本与更新: the desktop app updates itself in place
 *  (apps/desktop/updates.go; the sidebar shows it too); in a browser the
 *  daemon checks GitHub once a day and links the release. */
function Updates() {
  const d = desktop();
  const du = useDesktopUpdate();
  const [info, setInfo] = useState<UpdateInfo | null>(null);
  const [busy, setBusy] = useState(false);
  const manage = useStore((s) => s.tier === "desktop");
  const inPlace = inPlaceUpdates();
  useEffect(() => {
    if (!inPlace) API.updates().then(setInfo, () => setInfo(null));
  }, []);
  if (inPlace) return du ? <DesktopUpdates st={du} setAuto={(on) => void d!.setAutoUpdate?.(on)} /> : null;
  if (!info) return null;
  const check = async () => {
    setBusy(true);
    try {
      const i = await API.checkUpdates();
      setInfo(i);
      toast(i.error ? t("upd.failed", { error: i.error }) : i.available ? t("upd.found", { v: i.latest ?? "" }) : t("upd.upToDate"));
    } catch (e: any) {
      toast(e?.message ?? String(e));
    } finally {
      setBusy(false);
    }
  };
  const desc = info.available
    ? t("upd.availableWeb", { v: info.latest ?? "" })
    : info.checked
      ? t("upd.lastCheck", { when: ago(info.checked) }) + (info.error ? " · " + t("upd.failed", { error: info.error }) : "")
      : info.enabled
        ? t("upd.autoDaily")
        : t("upd.never");
  return (
    <>
      <Row title={t("upd.version", { v: info.current })} desc={desc}>
        {info.available && info.url ? (
          <a className="btn pri" href={info.url} target="_blank" rel="noreferrer noopener">
            {t("upd.see")}
          </a>
        ) : (
          <button className="btn" type="button" disabled={busy || !manage} onClick={() => void check()}>
            {busy ? t("upd.checking") : t("upd.check")}
          </button>
        )}
      </Row>
      {/* the notes are written in Chinese (CHANGELOG.md); the release page link stands in for them in English */}
      {info.available && info.notes && (getLang() === "zh" || !/[\u4e00-\u9fff]/.test(info.notes)) && (
        <div className="updnotes">
          <Markdown text={info.notes} />
        </div>
      )}
    </>
  );
}

/** The desktop app's version, a status line that follows the update in
 *  place, and the automatic-download switch. */
function DesktopUpdates({ st, setAuto }: { st: DesktopUpdateState; setAuto: (on: boolean) => void }) {
  const v = st.version ?? "";
  const pct = percent(st);
  let status: ReactNode;
  let action: ReactNode = null;
  const check = (
    <button className="btn" type="button" onClick={checkForUpdates}>
      {t("upd.check")}
    </button>
  );
  const notes = st.version ? (
    <button className="updlink" type="button" onClick={() => openUpdateNotes()}>
      {t("upd.notes")}
    </button>
  ) : null;
  switch (st.state) {
    case "disabled":
      status = t("upd.disabled");
      break;
    case "checking":
      status = (
        <span className="updst">
          <UpdateRing spin size={14} />
          {t("upd.stChecking")}
        </span>
      );
      action = (
        <button className="btn" type="button" disabled>
          {t("upd.checking")}
        </button>
      );
      break;
    case "available":
      status = (
        <span className="updst">
          {t("upd.stAvailable", { v })} · {notes}
        </span>
      );
      action = (
        <button className="btn pri" type="button" onClick={downloadUpdate}>
          {t("upd.download")}
        </button>
      );
      break;
    case "downloading":
      status = (
        <span className="updst">
          <UpdateRing progress={st.progress} spin={!st.total} size={14} />
          <span className="num">{st.progress >= 1 ? t("upd.stInstalling", { v }) : t("upd.stDownloading", { v, pct })}</span>
          {notes && <> · {notes}</>}
        </span>
      );
      break;
    case "ready":
      status = (
        <span className="updst">
          <i className="upddot" />
          {t("upd.stReady", { v })} · {notes}
        </span>
      );
      action = (
        <button className="btn pri" type="button" onClick={() => void restartToUpdate()}>
          {t("upd.restart")}
        </button>
      );
      break;
    case "error":
      status = (
        <span className="updst err">
          {st.errorDuring === "download" ? t("upd.stDownloadFailed", { v, error: st.error ?? "" }) : t("upd.stCheckFailed", { error: st.error ?? "" })}
        </span>
      );
      action = (
        <button className="btn" type="button" onClick={downloadUpdate}>
          {t("common.retry")}
        </button>
      );
      break;
    default:
      status = st.lastCheck ? t("upd.stUpToDate", { when: ago(st.lastCheck) }) : t("upd.never");
      action = check;
  }
  return (
    <>
      <div className="srow">
        <div className="g">
          <b>{t("upd.version", { v: st.current })}</b>
          <span>{status}</span>
        </div>
        <div className="c">{action}</div>
      </div>
      {st.state !== "disabled" && (
        <Row title={t("upd.autoInstall")} desc={t("upd.autoInstallDesc")}>
          <button className="tg" type="button" role="switch" aria-checked={st.autoDownload} aria-label={t("upd.autoInstall")} onClick={() => setAuto(!st.autoDownload)} />
        </Row>
      )}
    </>
  );
}

export function useSettingsData() {
  const [st, setSt] = useState<SettingsData | null>(null);
  useEffect(() => {
    API.settings().then(setSt, () => {});
  }, []);
  const patch = async (b: Partial<SettingsData>) => {
    try {
      const r = await API.patchSettings(b);
      setSt(r);
      toast(t("panel.saved"));
    } catch (e: any) {
      toast(e?.message ?? String(e));
    }
  };
  return [st, patch] as const;
}

function Defaults() {
  const [st, patch] = useSettingsData();
  const cur = st?.defaultMode || "sandbox";
  return (
    <div className="sbox">
      <Row title={t("set.defaultMode")} desc={t("set.defaultModeDesc")}>
        <div className="seg">
          {(["read-only", "sandbox"] as const).map((m) => (
            <button key={m} type="button" aria-pressed={cur === m} disabled={!st} onClick={() => patch({ defaultMode: m })}>
              {modeLabel(m)}
            </button>
          ))}
        </div>
      </Row>
      <Row title={t("set.defaultModels")} desc={t("set.defaultModelsDesc", { n: st?.defaultModels.length ?? 0 })}>
        <button className="btn" type="button" onClick={() => setState({ settings: "models", modelsTab: "routing" })}>
          {t("set.routing")}
        </button>
      </Row>
      <WorkspaceDir />
    </div>
  );
}

/** 默认工作区: the folder new agents' own folders are made in. */
function WorkspaceDir() {
  const [v, setV] = useState<{ dir: string; isDefault: boolean; default: string } | null>(null);
  const [edit, setEdit] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    API.workspaceDir().then(setV, () => setV(null));
  }, []);
  if (!v) return null;
  const save = async (dir: string) => {
    setBusy(true);
    try {
      const r = await API.setWorkspaceDir(dir);
      setV(r);
      setEdit(null);
      toast(r.isDefault ? t("set.wsReset") : t("set.wsSaved", { dir: shortPath(r.dir) }));
    } catch (e: any) {
      const k = `set.ws.${String(e?.code ?? "").replace(/^workspace_/, "")}` as Key;
      toast(t(k) !== k ? t(k, { dir: shortPath(dir) }) : e?.message ?? String(e));
    } finally {
      setBusy(false);
    }
  };
  const pick = async () => {
    const d = desktop();
    if (!d?.pickFolder) return setEdit(v.isDefault ? "" : v.dir);
    const dir = await d.pickFolder(v.dir).catch(() => "");
    if (dir) await save(dir);
  };
  return (
    <div className="srow wsrow">
      <span className="g">
        <b>{t("set.wsTitle")}</b>
        <span>{t("set.wsDesc")}</span>
        {edit === null ? (
          <code className="wspath" title={v.dir}>
            {shortPath(v.dir)}
            {v.isDefault && <span className="pill sm">{t("set.wsDefault")}</span>}
          </code>
        ) : (
          <form
            className="wsedit"
            onSubmit={(e) => {
              e.preventDefault();
              void save(edit);
            }}
          >
            <input className="fi sm mono" autoFocus value={edit} placeholder="~/Crewbase" spellCheck={false} onChange={(e) => setEdit(e.target.value)} />
            <button className="btn sm pri" type="submit" disabled={busy || !edit.trim()}>
              {t("common.save")}
            </button>
            <button className="btn sm ghost" type="button" onClick={() => setEdit(null)}>
              {t("common.cancel")}
            </button>
          </form>
        )}
      </span>
      {edit === null && (
        <span className="c">
          {!v.isDefault && (
            <button className="btn sm ghost" type="button" disabled={busy} onClick={() => void save("")}>
              {t("set.wsResetBtn")}
            </button>
          )}
          <button className="btn sm" type="button" disabled={busy} onClick={() => void pick()}>
            {t("set.wsChange")}
          </button>
        </span>
      )}
    </div>
  );
}
