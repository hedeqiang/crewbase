import { AddProviderDialog } from "./settings/AddProvider";
import { useEffect, useMemo, useState, type ReactNode } from "react";
import { ChevronLeft, Info, Plus, Search, Workflow } from "lucide-react";
import { NAV, SECTION_ICONS } from "./settings/nav";
import { API, desktop, type DesktopUpdates, type UpdateInfo, type ApprovalHistoryItem, type ProviderView, type RememberedChoice, type Settings as SettingsData, type UserProvider } from "../api";
import { getLang, t, type Key, type Lang } from "../i18n";
import { ago, kfmt, modeLabel, modelKind, providerName, until } from "../format";
import { openSession, setLang, setState, toast, useStore, type SettingsSection, resync } from "../store";
import { Automation } from "./settings/Automation";
import { Extensions } from "./settings/Extensions";
import { Profile } from "./settings/Profile";
import { Hosts } from "./settings/Hosts";
import { Team } from "./settings/Team";
import { Teams } from "./settings/Teams";
import { askNotify, notifyPermission, notifySupported } from "../companion";
import { Notify } from "./settings/Notify";
import { Im } from "./settings/Im";
import { Routing } from "./settings/Routing";
import { Avatar } from "./Avatar";
import { ApprovalActions } from "./Approval";
import { ProviderMark } from "./ProviderMark";
import { Markdown } from "./Markdown";
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



/** What a phone may open in settings. */
const MOBILE_SECTIONS = new Set<string>(["general", "keys", "approvals"]);

export function Settings() {
  const sec = useStore((s) => s.settings);
  const n = useStore((s) => s.approvals.length);
  const manage = useStore((s) => s.tier === "desktop");
  const [q, setQ] = useState("");
  const ql = q.trim().toLowerCase();
  return (
    <div className="setwrap">
      <nav className="snav" aria-label={t("tb.settings")}>
        <button className="back" type="button" onClick={() => setState({ view: "chat" })}>
          <ChevronLeft size={16} />
          {t("set.back")}
        </button>
        <label className="search sset">
          <Search size={14} />
          <input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("set.search")} />
        </label>
        {NAV.map((g) => {
          const items = g.items.filter((i) => (manage || MOBILE_SECTIONS.has(i.id)) && (!ql || t(`set.${i.id}` as Key).toLowerCase().includes(ql)));
          if (!items.length) return null;
          return (
            <div key={g.group} className="sgrp">
              <div className="sh">{t(g.group)}</div>
              {items.map((i) => (
                <button key={i.id} type="button" aria-current={sec === i.id} onClick={() => setState({ settings: i.id })}>
                  {i.icon}
                  {t(`set.${i.id}` as Key)}
                  {i.id === "approvals" && n > 0 && <span className="sn">{n}</span>}
                </button>
              ))}
            </div>
          );
        })}
      </nav>
      <div className="vw">
        {!manage && <p className="tiernote">{t("tier.mobileNote")}</p>}
        {(sec === "general" || (!manage && !MOBILE_SECTIONS.has(sec))) && <General />}
        {manage && sec === "models" && <Models />}
        {sec === "approvals" && <Approvals />}
        {manage && sec === "auto" && <Automation />}
        {manage && sec === "market" && <Extensions />}
        {manage && sec === "profile" && <Profile />}
        {sec === "keys" && <Keys />}
        {manage && sec === "notify" && <Notify />}
        {manage && sec === "hosts" && <Hosts />}
        {manage && sec === "team" && <Team />}
        {manage && sec === "teams" && <Teams />}
        {manage && sec === "im" && <Im />}
        {manage && !["general", "models", "approvals", "auto", "market", "profile", "keys", "notify", "hosts", "team", "teams", "im"].includes(sec) && <Soon sec={sec} />}
      </div>
    </div>
  );
}

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
                      <button className="linkbtn" type="button" onClick={() => resetBinding(s.id)} title={t("keys.reset")}>
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
          {t("comp.notifyOn")}
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
      <Head title={t("set.general")} />
      <div className="vb">
        <div className="vsec">{t("set.appearance")}</div>
        <div className="sbox">
          <Row title={t("set.theme")} desc={t("set.themeDesc")}>
            <ThemeSeg theme={theme} />
          </Row>
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

/** 关于 › 版本与更新: the desktop app updates itself (its updater); in a
 *  browser the daemon checks GitHub once a day and links the release. */
function Updates() {
  const d = desktop();
  const [du, setDu] = useState<DesktopUpdates | null>(null);
  const [info, setInfo] = useState<UpdateInfo | null>(null);
  const [busy, setBusy] = useState(false);
  const manage = useStore((s) => s.tier === "desktop");
  useEffect(() => {
    if (d?.updates) d.updates().then(setDu, () => setDu(null));
    else API.updates().then(setInfo, () => setInfo(null));
  }, []);
  if (du) {
    const when = du.lastCheck ? ago(du.lastCheck) : t("upd.never");
    return (
      <>
        <Row title={t("upd.version", { v: du.version })} desc={du.available ? t("upd.availableDesktop", { v: du.available }) : du.enabled ? t("upd.lastCheck", { when }) : t("upd.disabled")}>
          <button className={"btn" + (du.available ? " pri" : "")} type="button" disabled={!du.enabled} onClick={() => d!.checkForUpdates!()}>
            {du.available ? t("upd.install") : t("upd.check")}
          </button>
        </Row>
        {du.enabled && (
          <>
            <Row title={t("upd.autoCheck")} desc={t("upd.autoCheckDesc")}>
              <button
                className="tg"
                type="button"
                role="switch"
                aria-checked={du.automaticChecks}
                aria-label={t("upd.autoCheck")}
                onClick={() => d!.setUpdatePrefs!(!du.automaticChecks, du.automaticChecks ? false : du.automaticDownloads).then(setDu)}
              />
            </Row>
            <Row title={t("upd.autoInstall")} desc={t("upd.autoInstallDesc")}>
              <button
                className="tg"
                type="button"
                role="switch"
                aria-checked={du.automaticDownloads}
                aria-label={t("upd.autoInstall")}
                disabled={!du.automaticChecks}
                onClick={() => d!.setUpdatePrefs!(du.automaticChecks, !du.automaticDownloads).then(setDu)}
              />
            </Row>
          </>
        )}
      </>
    );
  }
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
  const setTab = (v: "providers" | "routing") => setState({ modelsTab: v });
  const [q, setQ] = useState("");
  const [adding, setAdding] = useState(false);
  const [userProv, setUserProv] = useState<UserProvider[]>([]);
  const pname = (id: string) => userProv.find((u) => u.id === id)?.name || providerName(id);
  const loadUser = () =>
    API.providers()
      .then((r) => setUserProv(r.providers))
      .catch(() => setUserProv([]));
  useEffect(() => {
    void loadUser();
  }, [models]);
  const providers = models?.providers ?? [];
  const exhaustedOf = (p: ProviderView) =>
    (models?.exhausted ?? []).filter((x) => x.model === p.id || x.model.startsWith(p.id + "/"));
  const isEx = (p: ProviderView) => exhaustedOf(p).length > 0 || (!!p.quota?.known && p.quota.remaining <= 0);
  const groups = useMemo(() => {
    const ql = q.trim().toLowerCase();
    const out: [string, ProviderView[]][] = [];
    for (const kind of ["subscription", "api", "local"]) {
      const ps = providers.filter((p) => (p.kind === kind || (kind === "api" && !["subscription", "local"].includes(p.kind))) && (!ql || p.id.toLowerCase().includes(ql) || providerName(p.id).toLowerCase().includes(ql)));
      if (ps.length) out.push([kind, ps]);
    }
    return out;
  }, [providers, q]);
  const cur = providers.find((p) => p.id === sel) ?? groups[0]?.[1][0];
  const users = cur ? Object.values(agents).filter((a) => (a.models ?? []).some((r) => r.provider === cur.id)) : [];

  return (
    <>
      <Head
        title={t("set.models")}
        right={
          <>
            <div className="seg">
              <button type="button" aria-pressed={tab === "providers"} onClick={() => setTab("providers")}>
                {t("set.providers")}
              </button>
              <button type="button" aria-pressed={tab === "routing"} onClick={() => setTab("routing")}>
                {t("set.routing")}
              </button>
            </div>
            <button className="btn" type="button" onClick={() => setAdding(true)}>
              <Plus size={14} />
              {t("set.addProvider")}
            </button>
          </>
        }
      />
      {adding && <AddProviderDialog existing={providers.map((p) => p.id)} onClose={() => setAdding(false)} />}
      {tab === "routing" ? (
        <Routing />
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
              <div className="pvh">
                <ProviderMark provider={cur.id} size={36} />
                <div className="g">
                  <b>{pname(cur.id)}</b>
                  <span>
                    {cur.id} · {t(`kind.${cur.kind}` as Key)}
                  </span>
                </div>
                <span className={"pill " + (cur.error ? "red" : isEx(cur) ? "red" : "ok")}>
                  {cur.error ? t("set.errorState") : isEx(cur) ? t("set.exhaustedState") : t("set.connectedState")}
                </span>
              </div>
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
              {(() => {
                const up = userProv.find((u) => u.id === cur.id);
                if (!up) return null;
                return (
                  <div className="kv2 upv">
                    <span className="small">
                      {t("ap.keyStored")} <code>{up.masked || "••••"}</code>
                      {up.baseURL ? <> · <span className="mono">{up.baseURL}</span></> : null}
                    </span>
                    <button
                      className="btn dng sm"
                      type="button"
                      onClick={async () => {
                        if (!(await confirmDialog(t("ap.removeQ", { name: up.name || up.id })))) return;
                        try {
                          await API.removeProvider(up.id);
                          toast(t("ap.removed", { name: up.name || up.id }));
                          await resync();
                          void loadUser();
                        } catch (e: any) {
                          toast(t("ap.failed", { error: e?.message ?? String(e) }));
                        }
                      }}
                    >
                      {t("ap.remove")}
                    </button>
                  </div>
                );
              })()}
              {exhaustedOf(cur).map((x) => (
                <div key={x.model} className="confirmbar errbar">
                  {t("set.exhaustedUntil", { model: x.model, time: until(x.until) })}
                </div>
              ))}
              <div className="psec2">{t("set.quota")}</div>
              <div className="box">
                {cur.quota?.known ? (
                  <QuotaRow label={t("set.quota")} remaining={cur.quota.remaining} resetsAt={cur.quota.resetsAt} />
                ) : (
                  <span className="small">{t("set.quotaUnknown")}</span>
                )}
              </div>
              <div className="psec2">{t("set.availableModels", { n: cur.models.length })}</div>
              {cur.models.length === 0 ? <div className="empty">{t("set.noModels")}</div> : <ModelTable provider={cur} />}
            </div>
          )}
        </div>
      )}
    </>
  );
}

/** A provider's models, grouped by what they produce (chat / image / video)
 *  when it serves more than one kind. */
function ModelTable({ provider }: { provider: ProviderView }) {
  const kinds = (["chat", "image", "video"] as const).filter((k) => provider.models.some((m) => modelKind(m) === k));
  return (
    <>
      {kinds.map((kind) => {
        const list = provider.models.filter((m) => modelKind(m) === kind);
        return (
          <div key={kind} className="mkgroup">
            {kinds.length > 1 && (
              <div className="mkh">
                <span className={"mkind k-" + kind}>{t(`set.kind.${kind}`)}</span>
                <span className="small">
                  {t(`set.kindDesc.${kind}`)} · {list.length}
                </span>
              </div>
            )}
            <div className="mtable">
              {list.map((m) => (
                <div className="mrow" key={m.id}>
                  <ProviderMark provider={provider.id} size={18} />
                  <span className="g">
                    <b>{m.name || m.id}</b>
                    <span className="mono">{m.id}</span>
                  </span>
                  <span className="caps">
                    {(m.capabilities ?? []).map((c) => (
                      <span key={c} className={"cap c-" + c.split("-")[0]}>
                        {t(`set.cap.${c}` as Key) === `set.cap.${c}` ? c : t(`set.cap.${c}` as Key)}
                      </span>
                    ))}
                  </span>
                  {kind === "chat" ? (
                    <span className="mctx">{m.contextWindow ? kfmt(m.contextWindow) : "—"}</span>
                  ) : (
                    <span className={"mkind sm k-" + kind}>{t(`set.kind.${kind}`)}</span>
                  )}
                  <span className="tg" role="switch" aria-checked={m.enabled} aria-disabled="true" title={t("common.soon")} />
                </div>
              ))}
            </div>
          </div>
        );
      })}
    </>
  );
}

function QuotaRow({ label, remaining, resetsAt }: { label: string; remaining: number; resetsAt?: string }) {
  const pct = Math.round(Math.max(0, Math.min(1, remaining)) * 100);
  const out = pct <= 0;
  return (
    <div className="qrow">
      <span>{label}</span>
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

function Approvals() {
  const approvals = useStore((s) => s.approvals);
  const agents = useStore((s) => s.agents);
  return (
    <>
      <Head title={t("set.approvals")} />
      <div className="vb">
        <div className="vsec">{t("set.pending", { n: approvals.length })}</div>
        {approvals.length === 0 && <p className="small">{t("appr.none")}</p>}
        {approvals.map((r) => {
          const a = agents[r.agent];
          return (
            <div className="ap" key={r.callId}>
              {a ? <Avatar agent={a} size={36} /> : <span className="wfi big"><Workflow size={17} /></span>}
              <div style={{ minWidth: 0 }}>
                <div className="small" style={{ display: "flex", gap: 6, alignItems: "center", flexWrap: "wrap" }}>
                  <span className="pill amber">{t("appr.waiting")}</span>
                  {a?.name ?? (r.agent || t("appr.workflow"))} · 「{r.reason || r.category}」 · {ago(r.asked)}
                </div>
                <h4>{r.summary || r.tool}</h4>
                <div className="small mono">
                  {r.tool}
                  {r.category ? ` · ${r.category}` : ""}
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
        <p className="small">{t("common.loading")}</p>
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

export function catLabel(c: string): string {
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
        <p className="small">{t("common.loading")}</p>
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
                    {h.decision?.message ? ` · 「${h.decision.message}」` : ""}
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
