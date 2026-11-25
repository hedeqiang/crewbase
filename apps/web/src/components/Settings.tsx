import { EmptyState } from "./EmptyState";
import { Skeleton } from "./Skeleton";
import { AddProviderDialog } from "./settings/AddProvider";
import { MagpieModelsDialog, ModelCatalog } from "./settings/ModelSources";
import { useEffect, useMemo, useState, type ReactNode } from "react";
import { CheckCheck, ChevronLeft, Globe, Info, KeyRound, ListChecks, Pencil, Plus, Search, Trash2, Workflow } from "lucide-react";
import { NAV, SECTION_ICONS } from "./settings/nav";
import { API, desktop, type DesktopPermission, type DesktopUpdateState, type UpdateInfo, type ApprovalHistoryItem, type ModelInfo, type ProviderView, type RememberedChoice, type Settings as SettingsData, type UserProvider } from "../api";
import { getLang, t, type Key, type Lang } from "../i18n";
import { ago, kfmt, modeLabel, modelKind, priceLong, priceShort, providerName, until } from "../format";
import { openSession, refreshModels, setBubbles, setLang, setLinkCards, setState, toast, useStore, type Bubbles, type SettingsSection, resync } from "../store";
import { Webhooks } from "./settings/Webhooks";
import { ProxyPage } from "./settings/Proxy";
import { Extensions } from "./settings/Extensions";
import { Profile } from "./settings/Profile";
import { Vault } from "./settings/Vault";
import { shortPath, toolLabel } from "./Conversation";
import { Hosts } from "./settings/Hosts";
import { Team } from "./settings/Team";
import { Teams } from "./settings/Teams";
import { Computer } from "./settings/Computer";
import { askNotify, notifyPermission, notifySupported } from "../companion";
import { Notify } from "./settings/Notify";
import { Im } from "./settings/Im";
import { Routing } from "./settings/Routing";
import { Avatar } from "./Avatar";
import { ApprovalActions } from "./Approval";
import { ProviderMark } from "./ProviderMark";
import { Markdown } from "./Markdown";
import { UpdateRing } from "./Update";
import { checkForUpdates, downloadUpdate, inPlaceUpdates, openUpdateNotes, percent, restartToUpdate, useDesktopUpdate } from "../updates";
import {
  SHORTCUTS,
  binding,
  comboOf,
  conflict,
  display,
  enterSends,
  fixedKeys,
  isCustom,
  onShortcutsChange,
  resetAll,
  resetBinding,
  setBinding,
  setEnterSends,
  type ActionId,
  type Shortcut,
} from "../shortcuts";
import { ThemeSeg } from "./SessionList";
import { confirmDialog } from "./Confirm";
import { DocsPage } from "./settings/AgentDocs";
import { OpenArtifactPage } from "./settings/OpenArtifact";
import { leaveTo } from "../router";



/** What a phone may open in settings. */
const MOBILE_SECTIONS = new Set<string>(["general", "keys", "approvals"]);

export function Settings() {
  const sec = useStore((s) => s.settings);
  const list = useStore((s) => s.settingsList);
  const n = useStore((s) => s.approvals.length);
  const nUpd = useStore((s) => s.addonUpdates);
  const docsOn = useStore((s) => !!s.agentDocs);
  const oaOn = useStore((s) => s.openArtifact);
  // apps set up under 插件与技能 get a section once they are there
  const shown = (id: string) => (id !== "docs" || docsOn) && (id !== "artifacts" || oaOn);
  const manage = useStore((s) => s.tier === "desktop");
  const [q, setQ] = useState("");
  const ql = q.trim().toLowerCase();
  return (
    <div className={"setwrap" + (list ? " slist" : " spage")}>
      <nav className="snav" aria-label={t("tb.settings")}>
        <button className="back" type="button" onClick={() => leaveTo("chat")}>
          <ChevronLeft size={16} />
          {t("set.back")}
        </button>
        <label className="search sset">
          <Search size={14} />
          <input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("set.search")} />
        </label>
        {NAV.map((g) => {
          const items = g.items.filter((i) => shown(i.id) && (manage || MOBILE_SECTIONS.has(i.id)) && (!ql || matchesSection(i.id, ql)));
          if (!items.length) return null;
          return (
            <div key={g.group} className="sgrp">
              <div className="sh">{t(g.group)}</div>
              {items.map((i) => (
                <button key={i.id} type="button" aria-current={sec === i.id && !list} onClick={() => setState({ settings: i.id, settingsList: false })}>
                  {i.icon}
                  {t(`set.${i.id}` as Key)}
                  {i.id === "approvals" && n > 0 && <span className="sn">{n}</span>}
                  {i.id === "market" && nUpd > 0 && <span className="sn upd" title={t("aup.dot", { n: nUpd })}>{nUpd}</span>}
                </button>
              ))}
            </div>
          );
        })}
        {ql && !NAV.some((g) => g.items.some((i) => (manage || MOBILE_SECTIONS.has(i.id)) && matchesSection(i.id, ql))) && (
          <p className="small muted snomatch">{t("set.searchNone")}</p>
        )}
      </nav>
      {/* a phone: from a section back to the list of sections */}
      <button className="sback" type="button" onClick={() => setState({ settingsList: true })}>
        <ChevronLeft size={16} />
        {t("tb.settings")}
      </button>
      <div className="vw" data-sec={sec} data-width={FULL_SECTIONS.has(sec) ? "full" : WIDE_SECTIONS.has(sec) ? "wide" : undefined}>
        {!manage && <p className="tiernote">{t("tier.mobileNote")}</p>}
        {(sec === "general" || (!manage && !MOBILE_SECTIONS.has(sec))) && <General />}
        {manage && sec === "models" && <Models />}
        {sec === "approvals" && <Approvals />}
        {manage && sec === "webhooks" && <Webhooks />}
        {manage && sec === "proxy" && <ProxyPage />}
        {manage && sec === "market" && <Extensions />}
        {manage && sec === "docs" && <DocsPage />}
        {manage && sec === "artifacts" && <OpenArtifactPage />}
        {manage && sec === "profile" && <Profile />}
        {manage && sec === "vault" && <Vault />}
        {sec === "keys" && <Keys />}
        {manage && sec === "notify" && <Notify />}
        {manage && sec === "hosts" && <Hosts />}
        {manage && sec === "team" && <Team />}
        {manage && sec === "teams" && <Teams />}
        {manage && sec === "im" && <Im />}
        {manage && sec === "computer" && <Computer />}
        {manage && !["general", "models", "approvals", "webhooks", "proxy", "market", "docs", "artifacts", "profile", "vault", "keys", "notify", "hosts", "team", "teams", "im", "computer"].includes(sec) && <Soon sec={sec} />}
      </div>
    </div>
  );
}

/** pages laid out wider (tables, grids, a file browser); the others read
 *  in one column of about 760px */
const WIDE_SECTIONS = new Set<string>(["market", "profile"]);
/** pages with their own panes (a list beside details, a file browser) */
const FULL_SECTIONS = new Set<string>(["models", "docs", "artifacts"]);

const GROUPS: { id: Shortcut["group"]; title: Key }[] = [
  { id: "general", title: "keys.general" },
  { id: "nav", title: "keys.nav" },
  { id: "chat", title: "keys.chat" },
  { id: "panel", title: "keys.panel" },
];

function Combo({ combo }: { combo: string }) {
  const parts = display(combo);
  if (!parts.length) return <span className="small muted">{t("keys.none")}</span>;
  return (
    <span className="kcombo">
      {parts.map((p, i) => (
        <kbd key={i} className="kbig">
          {p}
        </kbd>
      ))}
    </span>
  );
}

/** 设置 › 快捷键: every shortcut, click one to record a new key. */
function Keys() {
  const [, bump] = useState(0);
  const [rec, setRec] = useState<ActionId | null>(null);
  const [msg, setMsg] = useState("");
  useEffect(() => onShortcutsChange(() => bump((n) => n + 1)), []);
  useEffect(() => {
    if (!rec) return;
    const key = (e: KeyboardEvent) => {
      e.preventDefault();
      e.stopPropagation();
      if (e.key === "Escape") {
        setRec(null);
        setMsg("");
        return;
      }
      if (e.key === "Backspace" || e.key === "Delete") {
        setBinding(rec, "");
        setRec(null);
        setMsg("");
        return;
      }
      const c = comboOf(e);
      if (!c) return; // a modifier alone: keep waiting
      const why = conflict(rec, c);
      if (why === "reserved") {
        setMsg(t("keys.reserved", { keys: display(c).join("") }));
        return;
      }
      if (why === "needsModifier") {
        setMsg(t("keys.needsModifier"));
        return;
      }
      if (why) {
        const other = SHORTCUTS.find((s) => s.id === why);
        // swap: the other action takes this one's old key
        setBinding(why as ActionId, binding(rec));
        setMsg(t("keys.swapped", { other: other ? t(other.label) : why }));
      } else setMsg("");
      setBinding(rec, c);
      setRec(null);
    };
    window.addEventListener("keydown", key, true);
    return () => window.removeEventListener("keydown", key, true);
  }, [rec]);
  const fixed = fixedKeys();
  const custom = SHORTCUTS.some((s) => isCustom(s.id));
  return (
    <>
      <Head
        title={t("keys.title")}
        sub={t("keys.sub")}
        right={
          custom ? (
            <button className="btn sm" type="button" onClick={() => resetAll()}>
              {t("keys.resetAll")}
            </button>
          ) : undefined
        }
      />
      <div className="vb">
        <p className="small" style={{ margin: "0 0 10px" }}>
          {rec ? t("keys.recording") : t("keys.howto")}
        </p>
        {msg && <p className="small warnq">{msg}</p>}
        {GROUPS.map((g) => (
          <div key={g.id} className="kgrp">
            <div className="vsec">{t(g.title)}</div>
            <div className="sbox">
              {SHORTCUTS.filter((s) => s.group === g.id).map((s) => (
                <Row key={s.id} title={t(s.label)}>
                  <span className="krow">
                    <button
                      type="button"
                      className={"kbtn" + (rec === s.id ? " rec" : "")}
                      data-recording={rec === s.id ? "" : undefined}
                      onClick={() => {
                        setMsg("");
                        setRec(rec === s.id ? null : s.id);
                      }}
                      title={t("keys.change")}
                    >
                      {rec === s.id ? <span className="small">{t("keys.pressKeys")}</span> : <Combo combo={binding(s.id)} />}
                    </button>
                    {isCustom(s.id) && (
                      <button className="btn sm ghost" type="button" onClick={() => resetBinding(s.id)} title={t("keys.reset")}>
                        {t("keys.reset")}
                      </button>
                    )}
                  </span>
                </Row>
              ))}
              {fixed
                .filter((f) => f.group === g.id)
                .map((f) => (
                  <Row key={f.label} title={t(f.label)} dim>
                    <Combo combo={f.keys} />
                  </Row>
                ))}
              {g.id === "chat" && (
                <Row title={t("keys.enterSends")} desc={t("keys.enterSendsDesc")}>
                  <button className="tg" type="button" role="switch" aria-checked={enterSends()} aria-label={t("keys.enterSends")} onClick={() => setEnterSends(!enterSends())} />
                </Row>
              )}
            </div>
          </div>
        ))}
      </div>
    </>
  );
}

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

/** A settings section matches its title or what is on the page (set.kw.*,
 *  comma-separated, in the current language). */
function matchesSection(id: string, ql: string): boolean {
  const kw = t(`set.kw.${id}` as Key);
  return [t(`set.${id}` as Key), ...(kw.startsWith("set.kw.") ? [] : kw.split(","))].some((x) => x.trim().toLowerCase().includes(ql));
}

export function Head({ title, sub, right }: { title: string; sub?: string; right?: ReactNode }) {
  return (
    <div className="vh">
      <h2>{title}</h2>
      {sub && <span className="sub">{sub}</span>}
      {right && <div className="r">{right}</div>}
    </div>
  );
}

function Soon({ sec }: { sec: SettingsSection }) {
  return (
    <>
      <Head title={t(`set.${sec}` as Key)} />
      <div className="vb">
        <div className="soon">
          <div className="chico">{SECTION_ICONS[sec]}</div>
          <b>{t("common.soon")}</b>
          <span className="small">{t("common.soonDesc")}</span>
        </div>
      </div>
    </>
  );
}

export function Row({ title, desc, children, dim }: { title: string; desc?: string; children: ReactNode; dim?: boolean }) {
  return (
    <div className={"srow" + (dim ? " dim" : "")}>
      <div className="g">
        <b>{title}</b>
        {desc && <span>{desc}</span>}
      </div>
      <div className="c">{children}</div>
    </div>
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

function General() {
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

function providerState(p: ProviderView, exhausted: boolean): "ok" | "err" | "" {
  if (p.error || exhausted) return "err";
  return "ok";
}

function Models() {
  const models = useStore((s) => s.models);
  const agents = useStore((s) => s.agents);
  const [sel, setSel] = useState<string | null>(null);
  const tab = useStore((s) => s.modelsTab);
  const setTab = (v: "providers" | "routing" | "catalog") => setState({ modelsTab: v });
  const [q, setQ] = useState("");
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<UserProvider | null>(null);
  const [choosingMagpie, setChoosingMagpie] = useState(false);
  const [userProv, setUserProv] = useState<UserProvider[]>([]);
  const pname = (id: string) => userProv.find((u) => u.id === id)?.name || providerName(id);
  const loadUser = () =>
    API.providers()
      .then((r) => setUserProv(r.providers))
      .catch(() => setUserProv([]));
  useEffect(() => {
    void loadUser();
  }, [models]);
  const providers = useMemo(() => models?.providers ?? [], [models]);
  const exhaustedOf = (p: ProviderView) =>
    (models?.exhausted ?? []).filter((x) => x.model === p.id || x.model.startsWith(p.id + "/"));
  const isEx = (p: ProviderView) => exhaustedOf(p).length > 0 || (!!p.quota?.known && p.quota.remaining <= 0);
  const groups = useMemo(() => {
    const ql = q.trim().toLowerCase();
    const out: [string, ProviderView[]][] = [];
    for (const kind of ["api", "local"]) {
      const ps = providers.filter((p) => (p.kind === kind || (kind === "api" && p.kind !== "local")) && (!ql || p.id.toLowerCase().includes(ql) || pname(p.id).toLowerCase().includes(ql)));
      if (ps.length) out.push([kind, ps]);
    }
    return out;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [providers, q, userProv]);
  const cur = providers.find((p) => p.id === sel) ?? groups[0]?.[1][0];
  // every model of the selected provider with its on/off switch (the
  // pickers' list leaves out what magpie / added providers have off)
  const [sw, setSw] = useState<{ provider: string; models: ModelInfo[] } | null>(null);
  const [swBusy, setSwBusy] = useState<string | null>(null);
  // the full list with switches loads only when the person asks to edit
  const [editingList, setEditingList] = useState(false);
  useEffect(() => setEditingList(false), [cur?.id]);
  useEffect(() => {
    if (!cur || !editingList) return;
    let live = true;
    API.modelSwitches(cur.id).then(
      (r) => live && setSw({ provider: cur.id, models: r.models ?? [] }),
      () => live && setSw(null),
    );
    return () => {
      live = false;
    };
  }, [cur?.id, cur?.models, editingList]);
  // by default only the models in use (enabled); 编辑 shows every model the
  // provider offers, with its switch
  const listed = cur
    ? editingList && sw?.provider === cur.id && sw.models.length
      ? sw.models
      : cur.models.filter((m) => m.enabled !== false)
    : [];
  const flip = async (m: ModelInfo) => {
    if (!cur) return;
    setSwBusy(m.id);
    try {
      const r = await API.setModelSwitch(cur.id, m.id, m.enabled === false);
      setSw({ provider: cur.id, models: r.models ?? [] });
      toast(t(m.enabled === false ? "set.modelOnToast" : "set.modelOffToast", { name: m.name || m.id }));
      void refreshModels();
    } catch (e: any) {
      toast(e?.message ?? String(e));
    } finally {
      setSwBusy(null);
    }
  };
  const users = cur ? Object.values(agents).filter((a) => (a.models ?? []).some((r) => r.provider === cur.id)) : [];

  return (
    <>
      <Head
        title={t("set.models")}
        sub={t("sub.models")}
        right={
          <>
            <div className="seg">
              <button type="button" aria-pressed={tab === "providers"} onClick={() => setTab("providers")}>
                {t("set.providers")}
              </button>
              <button type="button" aria-pressed={tab === "routing"} onClick={() => setTab("routing")}>
                {t("set.routing")}
              </button>
              <button type="button" aria-pressed={tab === "catalog"} onClick={() => setTab("catalog")}>
                {t("mcat.tab")}
              </button>
            </div>
            <button className="btn" type="button" onClick={() => setAdding(true)}>
              <Plus size={14} />
              {t("set.addProvider")}
            </button>
          </>
        }
      />
      {adding && (
        <AddProviderDialog
          existing={providers.map((p) => p.id)}
          onClose={() => setAdding(false)}
        />
      )}
      {editing && (
        <AddProviderDialog
          existing={providers.map((p) => p.id)}
          edit={editing}
          onClose={() => {
            setEditing(null);
            void loadUser();
          }}
        />
      )}
      {choosingMagpie && <MagpieModelsDialog onClose={() => setChoosingMagpie(false)} />}
      {tab === "routing" ? (
        <Routing />
      ) : tab === "catalog" ? (
        <ModelCatalog />
      ) : providers.length === 0 ? (
        <div className="vb">
          <div className="empty">{models ? t("set.noProviders") : t("common.loading")}</div>
        </div>
      ) : (
        <div className="pvwrap">
          <div className="pvlist">
            <label className="search">
              <Search size={14} />
              <input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("set.searchProviders")} />
            </label>
            {groups.map(([kind, ps]) => (
              <div key={kind}>
                <div className="pvg">{t(`kind.${kind}` as Key)}</div>
                {ps.map((p) => {
                  const st = providerState(p, isEx(p));
                  return (
                    <button key={p.id} type="button" className="pvi" aria-current={cur?.id === p.id} onClick={() => setSel(p.id)}>
                      <ProviderMark provider={p.id} size={22} />
                      <span className="g">
                        <b>{pname(p.id)}</b>
                        <span>{p.error ? t("set.errorState") : isEx(p) ? t("set.exhaustedState") : t("set.connectedState")}</span>
                      </span>
                      {st && <span className={"sdot " + st} />}
                    </button>
                  );
                })}
              </div>
            ))}
          </div>
          {cur && (
            <div className="pvd">
              {(() => {
                const up = userProv.find((u) => u.id === cur.id);
                const remove = async () => {
                  if (!up || !(await confirmDialog(t("ap.removeQ", { name: up.name || up.id })))) return;
                  try {
                    await API.removeProvider(up.id);
                    toast(t("ap.removed", { name: up.name || up.id }));
                    await resync();
                    void loadUser();
                  } catch (e: any) {
                    toast(t("ap.failed", { error: e?.message ?? String(e) }));
                  }
                };
                return (
                  <>
                    <div className="pvh">
                      <ProviderMark provider={cur.id} size={40} />
                      <div className="g">
                        <b>
                          {pname(cur.id)}
                          <span className={"pill sm " + (cur.error || isEx(cur) ? "red" : "ok")}>
                            {cur.error ? t("set.errorState") : isEx(cur) ? t("set.exhaustedState") : t("set.connectedState")}
                          </span>
                        </b>
                        <span>
                          {cur.id} · {t(`kind.${cur.kind}` as Key)}
                        </span>
                      </div>
                      {up && (
                        <span className="pvacts">
                          <button className="btn sm" type="button" onClick={() => setEditing(up)}>
                            <Pencil size={13} />
                            {t("ap.editBtn")}
                          </button>
                          <button className="btn sm ghost ib-dng" type="button" aria-label={t("ap.remove")} title={t("ap.remove")} onClick={() => void remove()}>
                            <Trash2 size={14} />
                          </button>
                        </span>
                      )}
                    </div>
                    {up && (
                      <div className="pvmeta">
                        <span>
                          <KeyRound size={13} />
                          <code>{up.masked || "••••"}</code>
                        </span>
                        {up.baseURL && (
                          <span className="mono" title={up.baseURL}>
                            <Globe size={13} />
                            {up.baseURL}
                          </span>
                        )}
                      </div>
                    )}
                  </>
                );
              })()}
              <div className="pvusers">
                {users.length ? (
                  <>
                    {t("set.inUse")}
                    {users.map((a) => (
                      <button key={a.id} className="uchip" type="button" onClick={() => openSession(a.session)}>
                        <Avatar agent={a} size={18} />
                        {a.name}
                      </button>
                    ))}
                  </>
                ) : (
                  <span className="small">{t("set.inUseNone")}</span>
                )}
              </div>
              {cur.error && <div className="confirmbar errbar">{cur.error}</div>}
              {exhaustedOf(cur).map((x) => (
                <div key={x.model} className="confirmbar errbar">
                  {t("set.exhaustedUntil", { model: x.model, time: until(x.until) })}
                </div>
              ))}
              <div className="psec2">{t("set.quota")}</div>
              <div className="box">
                {cur.quota?.known ? (
                  // a gateway fronts several accounts: each window, named
                  cur.quota.windows?.length ? (
                    cur.quota.windows.map((w) => <QuotaRow key={w.label} label={w.label} remaining={w.remaining} resetsAt={w.resetsAt} />)
                  ) : (
                    <QuotaRow remaining={cur.quota.remaining} resetsAt={cur.quota.resetsAt} />
                  )
                ) : (
                  <span className="small">{t("set.quotaUnknown")}</span>
                )}
              </div>
              <div className="psec2 psec2r">
                <span>
                  {t("set.availableModels", { n: listed.length })}
                  {listed.some((m) => m.enabled === false) && <span className="small"> · {t("set.modelsOff", { n: listed.filter((m) => m.enabled === false).length })}</span>}
                </span>
                {cur.id === "magpie" ? (
                  <button className="btn sm" type="button" onClick={() => setChoosingMagpie(true)}>
                    <ListChecks size={13} />
                    {t("mp.choose")}
                  </button>
                ) : userProv.some((u) => u.id === cur.id) ? (
                  <button className="btn sm" type="button" onClick={() => setEditing(userProv.find((u) => u.id === cur.id)!)}>
                    <ListChecks size={13} />
                    {t("ap.editModels")}
                  </button>
                ) : (
                  <button className="btn sm" type="button" aria-pressed={editingList} onClick={() => setEditingList(!editingList)}>
                    <ListChecks size={13} />
                    {editingList ? t("set.doneEditing") : t("ap.editModels")}
                  </button>
                )}
              </div>
              {editingList && listed.length > 0 && <p className="small mswnote">{t("set.modelSwitchNote")}</p>}
              {listed.length === 0 ? (
                <div className="empty">{t("set.noModels")}</div>
              ) : (
                <ModelTable provider={cur} models={listed} busy={swBusy} onFlip={flip} switches={editingList} />
              )}
            </div>
          )}
        </div>
      )}
    </>
  );
}

const capName = (c: string) => (t(`set.cap.${c}` as Key) === `set.cap.${c}` ? c : t(`set.cap.${c}` as Key));

/** capabilities at least 80% of the models share */
function commonCaps(list: ModelInfo[]): string[] {
  const n: Record<string, number> = {};
  for (const m of list) for (const c of m.capabilities ?? []) n[c] = (n[c] ?? 0) + 1;
  return Object.keys(n).filter((c) => n[c] >= Math.ceil(list.length * 0.8));
}

/** A provider's models, grouped by what they produce (chat / image / video)
 *  when it serves more than one kind, each with its on/off switch. */
function ModelTable({ provider, models, busy, onFlip, switches = true }: { provider: ProviderView; models: ModelInfo[]; busy: string | null; onFlip: (m: ModelInfo) => void; switches?: boolean }) {
  const kinds = (["chat", "image", "video"] as const).filter((k) => models.some((m) => modelKind(m) === k));
  return (
    <>
      {kinds.map((kind) => {
        const list = models.filter((m) => modelKind(m) === kind);
        // what nearly every model here can do is said once above the list;
        // a row shows only what sets it apart
        const common = commonCaps(list);
        return (
          <div key={kind} className="mkgroup">
            {common.length > 0 && list.length > 3 && (
              <div className="mcommon small">
                {t("set.capsAll")}
                {common.map((c) => (
                  <span key={c} className={"cap c-" + c.split("-")[0]}>
                    {capName(c)}
                  </span>
                ))}
              </div>
            )}
            {kinds.length > 1 && (
              <div className="mkh">
                <span className={"mkind k-" + kind}>{t(`set.kind.${kind}`)}</span>
                <span className="small">
                  {t(`set.kindDesc.${kind}`)} · {list.length}
                </span>
              </div>
            )}
            <div className="mtable">
              {list.map((m) => {
                const on = m.enabled !== false;
                const levels = (m.efforts ?? []).length;
                return (
                  <div className={"mrow" + (on ? "" : " off")} key={m.id}>
                    <ProviderMark provider={provider.id} size={18} />
                    <span className="g">
                      <b>{m.name || m.id}</b>
                      <span className="mono">{m.id}</span>
                    </span>
                    <span className="caps">
                      {(m.capabilities ?? [])
                        .filter((c) => list.length <= 3 || !common.includes(c))
                        .map((c) => (
                          <span key={c} className={"cap c-" + c.split("-")[0]}>
                            {capName(c)}
                          </span>
                        ))}
                      {/* models without something most have: say what is missing */}
                      {list.length > 3 &&
                        common
                          .filter((c) => !(m.capabilities ?? []).includes(c))
                          .map((c) => (
                            <span key={"no" + c} className="cap c-none" title={t("set.capMissing", { cap: capName(c) })}>
                              {t("set.capNo", { cap: capName(c) })}
                            </span>
                          ))}
                      {levels > 1 && (
                        <span className="cap c-reasoning" title={(m.efforts ?? []).join(" · ")}>
                          {t("set.capLevels", { n: levels })}
                        </span>
                      )}
                    </span>
                    <span className="mprice" title={priceLong(m.pricing) || t("mc.noPrice")}>
                      {priceShort(m.pricing) || "—"}
                    </span>
                    {kind === "chat" ? (
                      <span className="mctx" title={m.maxOutput ? t("mc.maxOut", { n: kfmt(m.maxOutput) }) : undefined}>
                        {m.contextWindow ? kfmt(m.contextWindow) : "—"}
                      </span>
                    ) : (
                      <span className={"mkind sm k-" + kind}>{t(`set.kind.${kind}`)}</span>
                    )}
                    {switches && <button
                      className="tg"
                      type="button"
                      role="switch"
                      aria-checked={on}
                      aria-disabled={busy === m.id}
                      aria-label={t("set.modelSwitch", { name: m.name || m.id })}
                      title={on ? t("set.modelOnTitle") : t("set.modelOffTitle")}
                      onClick={() => busy !== m.id && onFlip(m)}
                    />}
                  </div>
                );
              })}
            </div>
          </div>
        );
      })}
    </>
  );
}

function QuotaRow({ label, remaining, resetsAt }: { label?: string; remaining: number; resetsAt?: string }) {
  const pct = Math.round(Math.max(0, Math.min(1, remaining)) * 100);
  const out = pct <= 0;
  return (
    <div className={"qrow" + (label ? "" : " nolabel")}>
      {label && <span>{label}</span>}
      <div className="meter">
        <i className={out ? "red" : pct <= 10 ? "amber" : ""} style={{ width: Math.max(out ? 100 : pct, 2) + "%" }} />
      </div>
      <span className={out ? "errt" : ""}>
        {out ? t("set.exhaustedState") : t("set.quotaLeft", { pct })}
        {resetsAt && !resetsAt.startsWith("0001") ? ` · ${t("set.quotaResets", { time: until(resetsAt) })}` : ""}
      </span>
    </div>
  );
}

/** 设置 › 审批: what waits for the person, the remembered choices and what
 *  was decided. */
function Approvals() {
  const approvals = useStore((s) => s.approvals);
  const agents = useStore((s) => s.agents);
  return (
    <>
      <Head title={t("set.approvals")} sub={t("sub.approvals")} />
      <div className="vb">
        <div className="vsec">{t("set.pending", { n: approvals.length })}</div>
        {approvals.length === 0 && <EmptyState icon={<CheckCheck size={22} />} title={t("appr.none")} desc={t("es.approvalsDesc")} />}
        {approvals.map((r) => {
          const a = agents[r.agent];
          return (
            <div className="ap" key={r.callId}>
              {a ? <Avatar agent={a} size={36} /> : <span className="wfi big"><Workflow size={17} /></span>}
              <div style={{ minWidth: 0 }}>
                <div className="small" style={{ display: "flex", gap: 6, alignItems: "center", flexWrap: "wrap" }}>
                  <span className="pill amber">{t("appr.waiting")}</span>
                  <span title={r.reason}>
                    {a?.name ?? (r.agent || t("appr.workflow"))} · {whyLabel(r)} · {ago(r.asked)}
                  </span>
                </div>
                <h4>{r.summary || r.tool}</h4>
                <div className="small" title={[r.tool, r.category].filter(Boolean).join(" · ")}>
                  {toolLabel(r.tool) || r.tool}
                </div>
              </div>
              <ApprovalActions
                req={r}
                extra={
                  <button className="btn ghost" type="button" onClick={() => openSession(r.session)}>
                    {t("appr.open")}
                  </button>
                }
              />
            </div>
          );
        })}
        <Remembered />
        <History />
        <div className="box rulenote">
          <Info size={16} />
          <div>
            <b>{t("set.whichAsk")}</b>
            <span>{t("set.whichAskDesc")}</span>
          </div>
        </div>
      </div>
    </>
  );
}

function Remembered() {
  const agents = useStore((s) => s.agents);
  const n = useStore((s) => s.approvals.length);
  const [list, setList] = useState<RememberedChoice[] | null>(null);
  const load = () => API.remembered().then((r) => setList(r.remembered ?? []), () => setList([]));
  useEffect(() => {
    void load();
  }, [n]);
  const revoke = async (c: RememberedChoice) => {
    const name = agents[c.agent]?.name ?? c.agent;
    if (!(await confirmDialog(t("set.revokeConfirm", { agent: name, category: c.category })))) return;
    try {
      await API.forget(c.agent, c.category);
      toast(t("set.revoked", { agent: name, category: c.category }));
      void load();
    } catch (e: any) {
      toast(e?.message ?? String(e));
    }
  };
  return (
    <>
      <div className="vsec">
        {t("set.remembered")} {list && list.length > 0 ? `· ${list.length}` : ""}
      </div>
      {list === null ? (
        <Skeleton />
      ) : list.length === 0 ? (
        <p className="small" style={{ margin: 0 }}>
          {t("set.rememberedNone")}
        </p>
      ) : (
        <div className="sbox">
          {list.map((c) => {
            const a = agents[c.agent];
            return (
              <div className="srow" key={c.agent + "/" + c.category}>
                <Avatar agent={a} size={28} />
                <div className="g">
                  <b>{t("set.rememberedRow", { agent: a?.name ?? c.agent, category: catLabel(c.category) })}</b>
                  <span>
                    {t("set.rememberedAt", { time: ago(c.at) })}
                    {c.overridden ? ` · ${t("set.overridden")}` : ""}
                  </span>
                </div>
                <div className="c">
                  <button className="btn" type="button" onClick={() => revoke(c)}>
                    {t("set.revoke")}
                  </button>
                </div>
              </div>
            );
          })}
        </div>
      )}
    </>
  );
}

/** Why a request asks, in people's words (the engine's reason, in English,
 *  only as a tooltip). */
function whyLabel(r: { tool: string; category?: string; reason?: string }): string {
  if (r.tool === "routine_create" || r.tool === "routine_update") return t("aprx.routine");
  if (r.category && r.category !== "none") return t("aprx.cat", { cat: catLabel(r.category) });
  return t("aprx.other");
}

export function catLabel(c: string): string {
  // computer:<bundle id> is operating that app (features/computertools)
  if (c.startsWith("computer:")) return t("cat.computer", { app: c.slice("computer:".length) });
  const k = `cat.${c}` as Key;
  const v = t(k);
  return v === k ? c : v;
}

function History() {
  const agents = useStore((s) => s.agents);
  const n = useStore((s) => s.approvals.length);
  const [list, setList] = useState<ApprovalHistoryItem[] | null>(null);
  useEffect(() => {
    API.approvalHistory(60).then((r) => setList((r.history ?? []).filter((h) => h.decision)), () => setList([]));
  }, [n]);
  return (
    <>
      <div className="vsec">{t("set.history")}</div>
      {list === null ? (
        <Skeleton />
      ) : list.length === 0 ? (
        <p className="small" style={{ margin: 0 }}>
          {t("set.historyNone")}
        </p>
      ) : (
        <div className="htable">
          {list.map((h) => {
            const a = agents[h.agent];
            const o = h.decision?.outcome ?? "";
            const tone = o === "allowed-once" ? "ok" : o === "rejected" ? "red" : "";
            return (
              <button className="hrow" type="button" key={h.callId} onClick={() => openSession(h.session)}>
                <Avatar agent={a} size={22} />
                <span className="g">
                  <b>{h.summary || h.tool}</b>
                  <span>
                    {a?.name ?? h.agent} · {catLabel(h.category || h.tool)}
                    {/* a person's words on rejecting (a cancel's message is the system's) */}
                    {o === "rejected" && h.decision?.message ? ` · 「${h.decision.message}」` : ""}
                  </span>
                </span>
                <span className={"pill " + tone}>
                  {t(o === "allowed-once" ? (h.decision?.remember ? "set.hAllowedRemember" : "set.hAllowed") : o === "rejected" ? "set.hRejected" : o === "cancelled" ? "set.hCancelled" : "set.hOther")}
                </span>
                <span className="small tm">{ago(h.decided || h.asked)}</span>
              </button>
            );
          })}
        </div>
      )}
    </>
  );
}
