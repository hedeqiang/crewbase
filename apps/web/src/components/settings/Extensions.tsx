import { useEffect, useMemo, useRef, useState, type CSSProperties, type MouseEvent, type ReactNode } from "react";
import { createPortal } from "react-dom";
import {
  BadgeCheck, Bug, CalendarDays, Check, FolderInput, GitCommitHorizontal, Globe, ListChecks, LogIn, Megaphone,
  MoreHorizontal, Plug, Plus, Search, SearchCode, Server, ShieldAlert, Sparkles, Table, X,
} from "lucide-react";
import { API, api, type MarketEntry, type McpServer, type PluginView } from "../../api";
import { getLang, t, type Key } from "../../i18n";
import { setState, toast, useStore } from "../../store";
import { Head } from "../Settings";
import { MonoMark } from "../ProviderMark";
import { ApproveDialog, errMsg, MCP_TONE, McpForm, McpTools, PluginCards, stateLabel } from "./ExtParts";

// Brand icons from LobeHub Icons (@lobehub/icons-static-svg, MIT), as in
// ProviderMark: only marks that exist in the package; everything else gets
// our own letter tile.
import github from "@lobehub/icons-static-svg/icons/github.svg?raw";
import notion from "@lobehub/icons-static-svg/icons/notion.svg?raw";
import cloudflare from "@lobehub/icons-static-svg/icons/cloudflare-color.svg?raw";
import google from "@lobehub/icons-static-svg/icons/google-color.svg?raw";
import figma from "@lobehub/icons-static-svg/icons/figma-color.svg?raw";
import huggingface from "@lobehub/icons-static-svg/icons/huggingface-color.svg?raw";
import devin from "@lobehub/icons-static-svg/icons/devin-color.svg?raw";
import exa from "@lobehub/icons-static-svg/icons/exa-color.svg?raw";
import brave from "@lobehub/icons-static-svg/icons/brave-color.svg?raw";
import mcp from "@lobehub/icons-static-svg/icons/mcp.svg?raw";

const BRAND: Record<string, string> = { github, notion, cloudflare, google, figma, huggingface, devin, exa, brave, mcp };
const SKILL_ICONS: Record<string, ReactNode> = {
  "search-code": <SearchCode size={19} />, "git-commit": <GitCommitHorizontal size={19} />, bug: <Bug size={19} />,
  megaphone: <Megaphone size={19} />, globe: <Globe size={19} />, table: <Table size={19} />,
  "list-checks": <ListChecks size={19} />, "calendar-days": <CalendarDays size={19} />,
};

// ---------------------------------------------------------------- catalog API

type Text = { zh: string; en: string };
interface CatInput { id: string; label: Text; help?: Text; placeholder?: string; secret?: boolean; optional?: boolean }
interface CatItem {
  id: string;
  kind: "mcp" | "skill";
  category: string;
  name: string;
  icon?: string;
  desc: Text;
  auth: "none" | "oauth" | "token" | "setup";
  docs?: string;
  setup?: Text;
  inputs?: CatInput[];
  transport?: string;
  endpoint?: string;
  installed: boolean;
  server?: string;
  scope?: string;
}
interface CatCategory { id: string; title: Text; desc: Text }
interface Catalog { categories: CatCategory[]; items: CatItem[] }
interface SkillView { name: string; description: string; dir: string; source: string; agent?: string }

const catalogAPI = {
  list: () => api<Catalog>("GET", "/api/catalog"),
  install: (id: string, b: { inputs?: Record<string, string>; scope?: string }) => api<{ kind: string; server?: McpServer }>("POST", `/api/catalog/${encodeURIComponent(id)}/install`, b),
  skills: () => api<{ skills: SkillView[] }>("GET", "/api/skills"),
  removeSkill: (name: string, agent?: string) =>
    api("DELETE", `/api/skills/${encodeURIComponent(name)}${agent ? "?agent=" + encodeURIComponent(agent) : ""}`),
};

const tx = (x?: Text) => (x ? (getLang() === "en" ? x.en : x.zh) || x.en || x.zh : "");

// ---------------------------------------------------------------- page

type View = "market" | "installed";

interface Data {
  catalog: Catalog | null;
  servers: McpServer[] | null;
  skills: SkillView[] | null;
  plugins: PluginView[] | null;
  market: MarketEntry[] | null;
  marketErr: string | null;
}

/** 设置 › 插件与技能: a marketplace of built-in MCP presets, skills and
 *  third-party plugins, and everything installed. */
export function Extensions() {
  const want = useStore((s) => s.settingsTab);
  const tick = useStore((s) => s.mcpTick);
  const [view, setView] = useState<View>(want === "mcp" || want === "installed" ? "installed" : "market");
  const [q, setQ] = useState("");
  const [d, setD] = useState<Data>({ catalog: null, servers: null, skills: null, plugins: null, market: null, marketErr: null });
  const [approve, setApprove] = useState<PluginView | null>(null);
  useEffect(() => {
    if (want) {
      setView(want === "mcp" || want === "installed" ? "installed" : "market");
      setState({ settingsTab: null });
    }
  }, [want]);
  const reload = () => {
    catalogAPI.list().then((catalog) => setD((x) => ({ ...x, catalog })), () => setD((x) => ({ ...x, catalog: { categories: [], items: [] } })));
    API.mcp().then((r) => setD((x) => ({ ...x, servers: r.servers ?? [] })), () => setD((x) => ({ ...x, servers: [] })));
    catalogAPI.skills().then((r) => setD((x) => ({ ...x, skills: r.skills ?? [] })), () => setD((x) => ({ ...x, skills: [] })));
    API.plugins().then((r) => setD((x) => ({ ...x, plugins: r.plugins ?? [] })), () => setD((x) => ({ ...x, plugins: [] })));
  };
  const reloadMarket = () =>
    API.market().then(
      (r) => setD((x) => ({ ...x, market: r.plugins ?? [], marketErr: null })),
      (e) => setD((x) => ({ ...x, market: [], marketErr: errMsg(e) })),
    );
  useEffect(() => {
    reload();
  }, [tick]);
  useEffect(() => {
    void reloadMarket();
  }, []);

  return (
    <>
      <Head
        title={t("set.market")}
        sub={t("mk.sub")}
        right={
          <div className="seg" role="group">
            <button type="button" aria-pressed={view === "market"} onClick={() => setView("market")}>
              {t("mk.tabMarket")}
            </button>
            <button type="button" aria-pressed={view === "installed"} onClick={() => setView("installed")}>
              {t("mk.tabInstalled")}
            </button>
          </div>
        }
      />
      <div className="vb mkwrap">
        <label className="search mksearch">
          <Search size={15} />
          <input type="search" value={q} placeholder={t("mk.search")} aria-label={t("mk.search")} onChange={(e) => setQ(e.target.value)} />
          {q && (
            <button type="button" className="ix" aria-label={t("common.close")} onClick={() => setQ("")}>
              <X size={13} />
            </button>
          )}
        </label>
        {view === "market" ? (
          <MarketView d={d} q={q.trim().toLowerCase()} reload={reload} reloadMarket={reloadMarket} onApprove={setApprove} />
        ) : (
          <InstalledView d={d} q={q.trim().toLowerCase()} reload={reload} reloadMarket={reloadMarket} onApprove={setApprove} />
        )}
      </div>
      {approve && (
        <ApproveDialog
          p={approve}
          onClose={() => setApprove(null)}
          onDone={() => {
            setApprove(null);
            reload();
          }}
        />
      )}
    </>
  );
}

interface ViewProps {
  d: Data;
  q: string;
  reload: () => void;
  reloadMarket: () => Promise<void>;
  onApprove: (p: PluginView) => void;
}

function matches(q: string, ...xs: (string | undefined)[]) {
  return !q || xs.some((x) => x?.toLowerCase().includes(q));
}

/** The 40px tile in front of a row. */
function ItemIcon({ icon, id, label, kind }: { icon?: string; id: string; label: string; kind?: string }) {
  if (icon && BRAND[icon]) return <span className="mkic brand" aria-hidden dangerouslySetInnerHTML={{ __html: BRAND[icon] }} />;
  if (kind === "skill") return <span className="mkic">{(icon && SKILL_ICONS[icon]) || <Sparkles size={19} />}</span>;
  if (kind === "plugin") return <span className="mkic"><Plug size={19} /></span>;
  if (kind === "server") return <span className="mkic"><Server size={19} /></span>;
  return <MonoMark id={id} label={label} size={40} />;
}

// ---------------------------------------------------------------- marketplace

const PREVIEW = 6;

interface Row {
  key: string;
  name: string;
  desc: string;
  icon: ReactNode;
  installed: boolean;
  busy?: boolean;
  onAdd: () => void;
}

function MarketView({ d, q, reload, reloadMarket, onApprove }: ViewProps) {
  const [busy, setBusy] = useState<string | null>(null);
  const [dialog, setDialog] = useState<CatItem | null>(null);
  const servers = d.servers ?? [];

  /** Installs at once when nothing must be asked; else opens the dialog. */
  const add = async (it: CatItem) => {
    const ask = it.auth === "oauth" || it.auth === "setup" || (it.inputs ?? []).some((x) => !x.optional);
    if (ask) return setDialog(it);
    setBusy(it.id);
    try {
      await catalogAPI.install(it.id, {});
      toast(t("mk.added", { name: it.name }));
      reload();
    } catch (e) {
      toast(t("mk.addFailed", { error: errMsg(e) }));
    } finally {
      setBusy(null);
    }
  };
  const addPlugin = async (m: MarketEntry) => {
    setBusy("plugin:" + m.name);
    try {
      const v = await API.install({ market: m.name });
      toast(t("mk.added", { name: v.name }));
      reload();
      void reloadMarket();
      if (!v.approved) onApprove(v);
    } catch (e) {
      toast(t("mk.addFailed", { error: errMsg(e) }));
    } finally {
      setBusy(null);
    }
  };

  if (!d.catalog) return <p className="small">{t("common.loading")}</p>;
  const sections: { id: string; title: string; desc: string; rows: Row[] }[] = d.catalog.categories.map((c) => ({
    id: c.id,
    title: tx(c.title),
    desc: tx(c.desc),
    rows: d.catalog!.items
      .filter((it) => it.category === c.id && matches(q, it.name, it.desc.zh, it.desc.en, c.title.zh, c.title.en))
      .map((it) => ({
        key: it.id,
        name: it.name,
        desc: tx(it.desc),
        icon: <ItemIcon icon={it.icon} id={it.id} label={it.name} kind={it.kind} />,
        installed: it.installed,
        busy: busy === it.id,
        onAdd: () => void add(it),
      })),
  }));
  sections.push({
    id: "plugins",
    title: t("mk.plugins"),
    desc: d.marketErr ? t("ext.marketErr", { error: d.marketErr }) : t("mk.pluginsDesc"),
    rows: (d.market ?? [])
      .filter((m) => matches(q, m.name, m.description, m.author))
      .map((m) => ({
        key: "plugin:" + m.name,
        name: m.name,
        desc: m.description,
        icon: <ItemIcon id={m.name} label={m.name} kind="plugin" />,
        installed: !!m.installed,
        busy: busy === "plugin:" + m.name,
        onAdd: () => void addPlugin(m),
      })),
  });
  const shown = sections.filter((s) => s.rows.length > 0);
  return (
    <>
      {shown.length === 0 && <div className="empty">{t("mk.noResults", { q })}</div>}
      {shown.map((s) => (
        <Section key={s.id} title={s.title} desc={s.desc} rows={s.rows} all={!!q} />
      ))}
      {dialog && (
        <InstallDialog
          item={dialog}
          servers={servers}
          onClose={() => setDialog(null)}
          onInstalled={reload}
        />
      )}
    </>
  );
}

function Section({ title, desc, rows, all }: { title: string; desc: string; rows: Row[]; all: boolean }) {
  const [open, setOpen] = useState(false);
  const list = all || open ? rows : rows.slice(0, PREVIEW);
  return (
    <section className="mksec">
      <h3>{title}</h3>
      {desc && <p className="small">{desc}</p>}
      <div className="mkgrid">
        {list.map((r) => (
          <div className="mkrow" key={r.key}>
            {r.icon}
            <div className="g">
              <b>{r.name}</b>
              <span title={r.desc}>{r.desc}</span>
            </div>
            {r.installed ? (
              <span className="mkadd done" title={t("mk.installed")} aria-label={t("mk.installed")}>
                <Check size={15} />
              </span>
            ) : (
              <button className="mkadd" type="button" disabled={r.busy} aria-label={t("mk.add", { name: r.name })} title={t("mk.add", { name: r.name })} onClick={r.onAdd}>
                {r.busy ? <span className="spin sm" /> : <Plus size={15} />}
              </button>
            )}
          </div>
        ))}
      </div>
      {!all && rows.length > PREVIEW && (
        <button className="mkmore" type="button" onClick={() => setOpen(!open)}>
          {open ? t("mk.seeLess") : t("mk.seeMore", { n: rows.length - PREVIEW })}
        </button>
      )}
    </section>
  );
}

function ScopeSelect({ value, onChange, teamsToo }: { value: string; onChange: (v: string) => void; teamsToo: boolean }) {
  const agents = useStore((s) => s.agents);
  const teams = useStore((s) => s.teams);
  return (
    <span className="ssel">
      <select value={value} onChange={(e) => onChange(e.target.value)}>
        <option value="global">{t("ext.scopeGlobal")}</option>
        {teamsToo &&
          teams.length > 1 &&
          teams.map((tm) => (
            <option key={"team:" + tm.id} value={"team:" + tm.id}>
              {t("ext.scopeTeam", { name: tm.name })}
            </option>
          ))}
        {Object.values(agents)
          .filter((a) => a.kind !== "chat")
          .map((a) => (
            <option key={a.id} value={a.id}>
              {t("ext.scopeAgent", { name: a.name })}
            </option>
          ))}
      </select>
    </span>
  );
}

/** Asks only what a preset needs (values to paste, who may use it), then
 *  for OAuth presets follows the connection until the person has signed in. */
function InstallDialog({ item, servers, onClose, onInstalled }: { item: CatItem; servers: McpServer[]; onClose: () => void; onInstalled: () => void }) {
  const [vals, setVals] = useState<Record<string, string>>({});
  const [scope, setScope] = useState("global");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [name, setName] = useState<string | null>(null); // installed server (OAuth: follow it)
  const live = name ? servers.find((s) => s.name === name) : undefined;
  const st = live?.status;
  const inputs = item.inputs ?? [];
  const partOf = item.server && !item.installed ? item.server : null; // a further part of an existing server
  const ready = inputs.every((x) => x.optional || (vals[x.id] ?? "").trim());
  useEffect(() => {
    const esc = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", esc);
    return () => window.removeEventListener("keydown", esc);
  }, [onClose]);

  const submit = async () => {
    setBusy(true);
    setErr(null);
    try {
      const r = await catalogAPI.install(item.id, { inputs: vals, scope: partOf ? undefined : scope });
      onInstalled();
      if (item.auth === "oauth" && r.server) setName(r.server.name);
      else {
        toast(t("mk.added", { name: item.name }));
        onClose();
      }
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  // Portalled: the page body is a size container, which would otherwise be
  // the containing block of the fixed scrim and modal.
  return createPortal(
    <>
      <div className="scrim" onClick={onClose} />
      <div className="modal mkdlg" role="dialog" aria-modal="true" aria-labelledby="mkTitle">
        <div className="mhd">
          <ItemIcon icon={item.icon} id={item.id} label={item.name} kind={item.kind} />
          <h3 id="mkTitle">{t("mk.dlgTitle", { name: item.name })}</h3>
          <button className="ix" type="button" aria-label={t("common.close")} onClick={onClose}>
            <X size={14} />
          </button>
        </div>
        <div className="mbd">
          <p className="small mkdesc">{tx(item.desc)}</p>
          {item.endpoint && (
            <div className="mkep small">
              <span>{item.transport === "http" ? t("mk.remote") : t("mk.local")}</span>
              <code className="ell">{item.endpoint}</code>
            </div>
          )}
          {name ? (
            <div className="mksign">
              {st?.state === "ready" ? (
                <p className="small okt">
                  <Check size={13} /> {t("mk.connected", { n: st.tools })}
                </p>
              ) : st?.state === "login" && st.loginUrl ? (
                <a className="btn pri" href={st.loginUrl} target="_blank" rel="noreferrer noopener">
                  <LogIn size={14} /> {t("mk.signIn", { name: item.name })}
                </a>
              ) : st?.state === "error" ? (
                <p className="small errt">{st.error}</p>
              ) : (
                <p className="small">
                  <span className="spin sm" /> {t("mk.waitLogin")}
                </p>
              )}
              <div className="mft">
                <button className="btn pri" type="button" onClick={onClose}>
                  {t("mk.done")}
                </button>
              </div>
            </div>
          ) : (
            <>
              {item.setup && <p className="mknote">{tx(item.setup)}</p>}
              {partOf && <p className="small">{t("mk.partOf", { server: partOf })}</p>}
              {inputs.map((x) => (
                <div key={x.id}>
                  <label className="fl" htmlFor={"mk-" + x.id}>
                    {tx(x.label)}
                  </label>
                  <input
                    id={"mk-" + x.id}
                    className="fi sm mono"
                    type={x.secret ? "password" : "text"}
                    autoComplete="off"
                    spellCheck={false}
                    value={vals[x.id] ?? ""}
                    placeholder={x.placeholder ?? (x.optional ? t("mk.optional") : "")}
                    onChange={(e) => setVals({ ...vals, [x.id]: e.target.value })}
                  />
                  {x.secret && <div className="small">{t("mk.secretNote")}</div>}
                </div>
              ))}
              {item.auth === "oauth" && <p className="small">{t("mk.oauthNote", { name: item.name })}</p>}
              {!partOf && (
                <>
                  <label className="fl">{t("mk.who")}</label>
                  <ScopeSelect value={scope} onChange={setScope} teamsToo />
                </>
              )}
              {err && <div className="ferr">{err}</div>}
              <div className="mft">
                {item.docs && (
                  <a className="btn ghost" href={item.docs} target="_blank" rel="noreferrer noopener" style={{ marginRight: "auto" }}>
                    {t("mk.docs")}
                  </a>
                )}
                <button className="btn ghost" type="button" onClick={onClose}>
                  {t("common.cancel")}
                </button>
                <button className="btn pri" type="button" disabled={busy || !ready} onClick={submit}>
                  {busy ? t("mk.adding") : t("mk.addBtn")}
                </button>
              </div>
            </>
          )}
        </div>
      </div>
    </>,
    document.body,
  );
}

// ---------------------------------------------------------------- installed

interface MenuItem {
  label: string;
  danger?: boolean;
  disabled?: boolean;
  onClick: () => void;
}

/** ⋯ button with a small menu (fixed, so it escapes scrolling lists). */
function MoreMenu({ items }: { items: MenuItem[] }) {
  const [at, setAt] = useState<{ x: number; y: number } | null>(null);
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!at) return;
    const close = (e: Event) => {
      if (ref.current && e.target instanceof Node && ref.current.contains(e.target)) return;
      setAt(null);
    };
    const esc = (e: KeyboardEvent) => e.key === "Escape" && setAt(null);
    window.addEventListener("mousedown", close);
    window.addEventListener("keydown", esc);
    window.addEventListener("resize", close);
    return () => {
      window.removeEventListener("mousedown", close);
      window.removeEventListener("keydown", esc);
      window.removeEventListener("resize", close);
    };
  }, [at]);
  const open = (e: MouseEvent) => {
    const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
    setAt(at ? null : { x: r.right, y: r.bottom + 4 });
  };
  return (
    <>
      <button className="ix" type="button" aria-label={t("mk.more")} title={t("mk.more")} aria-haspopup="menu" aria-expanded={!!at} onClick={open}>
        <MoreHorizontal size={15} />
      </button>
      {at &&
        createPortal(
        <div ref={ref} className="menu" role="menu" style={{ top: at.y, right: Math.max(8, window.innerWidth - at.x), minWidth: 180 } as CSSProperties}>
          {items.map((m) => (
            <button
              key={m.label}
              type="button"
              role="menuitem"
              className={"mi" + (m.danger ? " dng" : "")}
              disabled={m.disabled}
              onClick={() => {
                setAt(null);
                m.onClick();
              }}
            >
              {m.label}
            </button>
          ))}
        </div>,
          document.body,
        )}
    </>
  );
}

function stLabel(state?: string) {
  const k = `ext.mcpSt.${state}` as Key;
  return t(k) === k ? state || "—" : t(k);
}

function InstalledView({ d, q, reload, reloadMarket, onApprove }: ViewProps) {
  const agents = useStore((s) => s.agents);
  const teams = useStore((s) => s.teams);
  const [adding, setAdding] = useState(false);
  const [open, setOpen] = useState<string | null>(null);
  const [path, setPath] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const byServer = useMemo(() => {
    const m: Record<string, CatItem> = {};
    for (const it of d.catalog?.items ?? []) if (it.kind === "mcp" && it.server && !m[it.server]) m[it.server] = it;
    return m;
  }, [d.catalog]);
  const builtinSkills = useMemo(() => new Map((d.catalog?.items ?? []).filter((it) => it.kind === "skill").map((it) => [it.name, it])), [d.catalog]);
  const sharedDir = (d.skills ?? []).find((s) => !s.agent)?.source;

  const scopeLabel = (scope?: string) =>
    !scope || scope === "global"
      ? t("ext.scopeGlobal")
      : scope.startsWith("team:")
        ? t("ext.scopeTeam", { name: teams.find((x) => "team:" + x.id === scope)?.name ?? scope.slice(5) })
        : t("ext.scopeAgent", { name: agents[scope]?.name ?? scope });

  const run = async (key: string, f: () => Promise<unknown>, ok?: string) => {
    setBusy(key);
    try {
      await f();
      if (ok) toast(ok);
      reload();
    } catch (e) {
      toast(errMsg(e));
    } finally {
      setBusy(null);
    }
  };

  const servers = (d.servers ?? []).filter((s) => matches(q, s.name, byServer[s.name]?.name, s.url, (s.command ?? []).join(" ")));
  const skills = (d.skills ?? []).filter((s) => matches(q, s.name, s.description));
  const plugins = (d.plugins ?? []).filter((p) => matches(q, p.name, p.description));
  const loading = d.servers === null || d.skills === null || d.plugins === null;
  if (loading) return <p className="small">{t("common.loading")}</p>;

  return (
    <>
      <section className="mksec">
        <div className="mksh">
          <h3>{t("mk.instMcp")}</h3>
          <span className="sp" />
          {!adding && (
            <button className="btn sm" type="button" onClick={() => setAdding(true)}>
              <Plus size={12} />
              {t("mk.custom")}
            </button>
          )}
        </div>
        {adding && (
          <McpForm
            onCancel={() => setAdding(false)}
            onSaved={(s) => {
              setAdding(false);
              setOpen(s.name);
              reload();
            }}
          />
        )}
        {servers.length === 0 ? (
          !adding && <p className="small">{t("mk.noneMcp")}</p>
        ) : (
          <div className="mklist">
            {servers.map((s) => {
              const it = byServer[s.name];
              const st = s.status?.state;
              return (
                <div className={"mkirow" + (s.enabled ? "" : " off")} key={s.name}>
                  <div className="mkrow">
                    <ItemIcon icon={it?.icon} id={s.name} label={it?.name ?? s.name} kind={it ? it.kind : "server"} />
                    <div className="g">
                      <b>
                        {it?.name ?? s.name}
                        <span className={"pill sm " + (MCP_TONE[st] ?? "")}>{stLabel(st)}</span>
                      </b>
                      <span className="mono" title={s.transport === "http" ? s.url : (s.command ?? []).join(" ")}>
                        {scopeLabel(s.scope)} · {t("ext.toolsN", { n: s.status?.tools ?? 0 })} · {s.transport === "http" ? s.url : (s.command ?? []).join(" ")}
                      </span>
                    </div>
                    {st === "login" && s.status.loginUrl && (
                      <a className="btn pri sm" href={s.status.loginUrl} target="_blank" rel="noreferrer noopener">
                        <LogIn size={12} /> {t("ext.mcpLogin")}
                      </a>
                    )}
                    <button
                      className="tg"
                      type="button"
                      role="switch"
                      aria-checked={s.enabled}
                      aria-label={t("auto.enabled")}
                      disabled={s.source === "config" || busy === s.name}
                      onClick={() => run(s.name, () => API.patchMcp(s.name, { enabled: !s.enabled }))}
                    />
                    <MoreMenu
                      items={[
                        { label: t("mk.tools"), onClick: () => setOpen(open === s.name ? null : s.name) },
                        ...(s.oauth ? [{ label: t("ext.mcpSignOut"), onClick: () => run(s.name, () => API.mcpSignOut(s.name), t("ext.mcpSignedOut", { name: s.name })) }] : []),
                        {
                          label: t("mk.remove"),
                          danger: true,
                          disabled: s.source === "config",
                          onClick: () => {
                            if (window.confirm(t("ext.removeConfirm", { name: s.name }))) void run(s.name, () => API.deleteMcp(s.name), t("mk.removed", { name: s.name }));
                          },
                        },
                      ]}
                    />
                  </div>
                  {s.status?.error && <div className="small errt mkerr">{s.status.error}</div>}
                  {open === s.name && (
                    <div className="mkdetail">
                      <McpTools name={s.name} onProbed={reload} />
                    </div>
                  )}
                </div>
              );
            })}
          </div>
        )}
      </section>

      <section className="mksec">
        <div className="mksh">
          <h3>{t("mk.instSkills")}</h3>
        </div>
        {skills.length === 0 ? (
          <p className="small">{t("mk.noneSkills")}</p>
        ) : (
          <div className="mklist">
            {skills.map((s) => {
              const b = builtinSkills.get(s.name);
              const removable = !!s.agent || s.source === sharedDir;
              return (
                <div className="mkirow" key={(s.agent ?? "") + "/" + s.name}>
                  <div className="mkrow">
                    <ItemIcon icon={b?.icon} id={s.name} label={s.name} kind="skill" />
                    <div className="g">
                      <b>
                        {s.name}
                        <span className="pill sm">{s.agent ? t("ext.scopeAgent", { name: agents[s.agent]?.name ?? s.agent }) : t("mk.skillAll")}</span>
                      </b>
                      <span title={t("mk.skillDir", { dir: s.dir })}>{(b && tx(b.desc)) || s.description}</span>
                    </div>
                    <MoreMenu
                      items={[
                        {
                          label: removable ? t("mk.remove") : t("mk.skillManaged"),
                          danger: removable,
                          disabled: !removable,
                          onClick: () => {
                            if (window.confirm(t("mk.removeSkill", { name: s.name }))) void run("skill:" + s.name, () => catalogAPI.removeSkill(s.name, s.agent), t("mk.removed", { name: s.name }));
                          },
                        },
                      ]}
                    />
                  </div>
                </div>
              );
            })}
          </div>
        )}
      </section>

      <section className="mksec">
        <div className="mksh">
          <h3>{t("mk.instPlugins")}</h3>
        </div>
        {plugins.length === 0 ? (
          <p className="small">{t("mk.nonePlugins")}</p>
        ) : (
          <div className="mklist">
            {plugins.map((p) => {
              const pending = !p.approved || (p.needsApproval?.length ?? 0) > 0;
              return (
                <div className={"mkirow" + (p.enabled ? "" : " off")} key={p.name}>
                  <div className="mkrow">
                    <ItemIcon id={p.name} label={p.name} kind="plugin" />
                    <div className="g">
                      <b>
                        {p.name}
                        <span className={"pill sm " + (p.state === "running" ? "ok" : p.error ? "red" : !p.approved ? "amber" : "")}>{stateLabel(p)}</span>
                        {p.publisher && (
                          <span className="pubok" title={p.integrity}>
                            <BadgeCheck size={12} />
                          </span>
                        )}
                      </b>
                      <span>
                        v{p.version} · {p.description || p.source}
                      </span>
                    </div>
                    {pending && (
                      <button className="btn warn sm" type="button" onClick={() => onApprove(p)}>
                        <ShieldAlert size={12} />
                        {t("ext.reviewPerms")}
                      </button>
                    )}
                    <button
                      className="tg"
                      type="button"
                      role="switch"
                      aria-checked={p.enabled}
                      aria-label={t("auto.enabled")}
                      disabled={busy === p.name}
                      onClick={() => run(p.name, () => API.pluginAction(p.name, p.enabled ? "disable" : "enable"))}
                    />
                    <MoreMenu
                      items={[
                        { label: t("mk.perms"), onClick: () => onApprove(p) },
                        { label: t("mk.reload"), disabled: !p.enabled, onClick: () => run(p.name, () => API.pluginAction(p.name, "reload")) },
                        {
                          label: t("mk.remove"),
                          danger: true,
                          onClick: () => {
                            if (window.confirm(t("ext.removeConfirm", { name: p.name })))
                              void run(p.name, async () => {
                                await API.deletePlugin(p.name);
                                void reloadMarket();
                              });
                          },
                        },
                      ]}
                    />
                  </div>
                  {p.error && <div className="small errt mkerr">{p.error}</div>}
                </div>
              );
            })}
          </div>
        )}
        <form
          className="runbar mkpath"
          onSubmit={(e) => {
            e.preventDefault();
            const src = path.trim();
            if (!src) return;
            void run(
              "path",
              async () => {
                const v = await API.install({ source: src });
                setPath("");
                if (!v.approved) onApprove(v);
              },
              t("ext.installed", { name: src }),
            );
          }}
        >
          <input className="fi sm mono" value={path} placeholder={t("ext.pathPh")} aria-label={t("ext.fromPath")} onChange={(e) => setPath(e.target.value)} />
          <button className="btn" type="submit" disabled={!path.trim() || busy === "path"}>
            <FolderInput size={13} />
            {t("ext.install")}
          </button>
        </form>
        <p className="small">{t("ext.pathHint")}</p>
      </section>
      <PluginCards />
    </>
  );
}
