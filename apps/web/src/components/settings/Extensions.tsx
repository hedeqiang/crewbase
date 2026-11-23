import { Skeleton } from "../Skeleton";
import { Fragment, useEffect, useMemo, useRef, useState, type CSSProperties, type MouseEvent, type ReactNode } from "react";
import { UpdatesSection, useAddonUpdates } from "./Updates";
import { AgentDocsDialog, AgentDocsIcon, useAgentDocs } from "./AgentDocs";
import { OpenArtifactDialog, OpenArtifactIcon, oaInstalled, useOpenArtifact } from "./OpenArtifact";
import { createPortal } from "react-dom";
import {
  BadgeCheck, Bug, CalendarDays, Check, ClipboardPaste, Copy, ExternalLink, GitCommitHorizontal, Globe, ListChecks, LogIn, Megaphone,
  MoreHorizontal, Plug, Plus, Search, SearchCode, ShieldAlert, Sparkles, Table, X,
} from "lucide-react";
import { API, api, openExternal, DEFAULT_TEAM, type AgentView, type MarketEntry, type McpServer, type PluginView } from "../../api";
import { canUse, connectorsFor, nextConnectors, useConnectors } from "../AgentConnectors";
import { getLang, t, type Key } from "../../i18n";
import { setState, toast, useStore, refreshAgentDocs } from "../../store";
import { Head } from "../Settings";
import { MonoMark } from "../ProviderMark";
import { PasteInstall } from "./PasteInstall";
import { ApproveDialog, errMsg, MCP_TONE, McpForm, McpTools, PluginCards, settle, signInWindow, stateLabel } from "./ExtParts";

// Brand icons from LobeHub Icons (@lobehub/icons-static-svg, MIT), as in
// ProviderMark, where the package has the mark; the others are the
// companies' own marks from their brand pages or sites (src/assets/brands).
// Anything else gets a letter tile.
import github from "@lobehub/icons-static-svg/icons/github.svg?raw";
import notion from "@lobehub/icons-static-svg/icons/notion.svg?raw";
import cloudflare from "@lobehub/icons-static-svg/icons/cloudflare-color.svg?raw";
import huggingface from "@lobehub/icons-static-svg/icons/huggingface-color.svg?raw";
import devin from "@lobehub/icons-static-svg/icons/devin-color.svg?raw";
import exa from "@lobehub/icons-static-svg/icons/exa-color.svg?raw";
import mcp from "@lobehub/icons-static-svg/icons/mcp.svg?raw";
import context7 from "../../assets/brands/context7.svg?raw";
import sentry from "../../assets/brands/sentry.svg?raw";
import linear from "../../assets/brands/linear.svg?raw";
import stripe from "../../assets/brands/stripe.svg?raw";
import atlassian from "../../assets/brands/atlassian.svg?raw";
import canva from "../../assets/brands/canva.svg?raw";
import chrome from "../../assets/brands/chrome.svg?raw";
import supabase from "../../assets/brands/supabase.svg?raw";
import firecrawl from "@lobehub/icons-static-svg/icons/firecrawl-color.svg?raw";
import { confirmDialog } from "../Confirm";

/** A mark and how it sits in the 40px tile: "tile" = the mark is its own
 *  rounded square and fills the tile; "wide" = a wordmark. */
const BRAND: Record<string, { svg: string; fit?: "tile" | "wide" }> = {
  github: { svg: github }, notion: { svg: notion }, cloudflare: { svg: cloudflare },
  huggingface: { svg: huggingface }, devin: { svg: devin }, exa: { svg: exa }, mcp: { svg: mcp },
  context7: { svg: context7, fit: "tile" }, stripe: { svg: stripe, fit: "tile" }, sentry: { svg: sentry },
  linear: { svg: linear }, atlassian: { svg: atlassian }, canva: { svg: canva, fit: "wide" },
  chrome: { svg: chrome }, supabase: { svg: supabase }, firecrawl: { svg: firecrawl },
};
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
  /** device = a built-in sign-in (signIn) runs; inputs are only its fallback. */
  auth: "none" | "oauth" | "token" | "setup" | "device";
  signIn?: string;
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
interface SkillLibrary { id: string; name: string; repo: string; author: string; desc: Text; picks?: string[] }
interface Catalog { categories: CatCategory[]; items: CatItem[]; libraries?: SkillLibrary[] }
interface SkillView { name: string; description: string; dir: string; source: string; agent?: string; version?: string }
/** A built-in device sign-in (GitHub). */
interface SignInView {
  state: "pending" | "done" | "denied" | "expired" | "error";
  userCode?: string;
  verificationUri?: string;
  expiresAt?: string;
  error?: string;
  server?: string;
  noInstallation?: boolean;
  installUrl?: string;
}

const enc = encodeURIComponent;
const catalogAPI = {
  list: () => api<Catalog>("GET", "/api/catalog"),
  install: (id: string, b: { inputs?: Record<string, string> } & Target) => api<{ kind: string; server?: McpServer }>("POST", `/api/catalog/${enc(id)}/install`, b),
  uninstall: (id: string) => api("DELETE", `/api/catalog/${enc(id)}`),
  signIn: (id: string, b: Target = {}) => api<SignInView>("POST", `/api/catalog/${enc(id)}/signin`, b),
  signInState: (id: string) => api<SignInView>("GET", `/api/catalog/${enc(id)}/signin`),
  cancelSignIn: (id: string) => api("DELETE", `/api/catalog/${enc(id)}/signin`),
  skills: () => api<{ skills: SkillView[] }>("GET", "/api/skills"),
  removeSkill: (name: string, agent?: string) => api("DELETE", `/api/skills/${enc(name)}${agent ? "?agent=" + enc(agent) : ""}`),
};

/** Who a new connection is for: an explicit scope ("global") or the
 *  context it is made from (a team: the server scopes it to that team). */
type Target = { scope?: string; context?: string };

/** A global connector with at least this many tools gets a notice
 *  suggesting to give it only to the agents that need it. */
const MANY_TOOLS = 20;

const tx = (x?: Text) => (x ? (getLang() === "en" ? x.en : x.zh) || x.en || x.zh : "");
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

/** How clicking 连接 proceeds for an item. A new kind of catalog item (e.g.
 *  a built-in connector) adds a case here and in MarketView.connect. */
type ConnectHow = "direct" | "oauth" | "device" | "dialog";
function connectHow(it: CatItem): ConnectHow {
  if (it.kind === "skill") return "direct";
  switch (it.auth) {
    case "device":
      return "device";
    case "oauth":
      return "oauth";
    case "none":
      return (it.inputs ?? []).some((x) => !x.optional) ? "dialog" : "direct";
    default: // token, setup: something must be pasted or done first
      return "dialog";
  }
}

/** The server answers 401/403: its token expired or was revoked. */
const needsReauth = (s?: McpServer) => s?.status?.state === "error" && /\b(401|403)\b|unauthori[sz]ed|forbidden|bad credentials/i.test(s.status.error ?? "");

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

/** 设置 › 插件与技能: a marketplace of built-in services, skills and
 *  third-party plugins, and everything connected or installed. */
let lastData: Data | null = null;

export function Extensions() {
  const want = useStore((s) => s.settingsTab);
  const tick = useStore((s) => s.mcpTick);
  const [view, setView] = useState<View>(want === "mcp" || want === "installed" ? "installed" : "market");
  const [q, setQ] = useState("");
  // what was shown last time: coming back shows it at once, then refreshes
  const [d, setD0] = useState<Data>(() => lastData ?? { catalog: null, servers: null, skills: null, plugins: null, market: null, marketErr: null });
  const setD = (f: (x: Data) => Data) =>
    setD0((x) => {
      const n = f(x);
      lastData = n;
      return n;
    });
  const [approve, setApprove] = useState<PluginView | null>(null);
  const upd = useAddonUpdates();
  const nUpd = upd.d?.items.length ?? 0;
  const [paste, setPaste] = useState<{ text: string; picks?: string[] } | null>(null);
  const [focus, setFocus] = useState<string | null>(null); // server to open in Installed
  const [gh, setGh] = useState<CatItem | null>(null); // GitHub sign-in dialog
  const [scopeOf, setScopeOf] = useState<McpServer | null>(null);
  const [accessOf, setAccessOf] = useState<McpServer | null>(null);
  const teams = useStore((s) => s.teams);
  const team = useStore((s) => s.team);
  // New connections go to the team on screen when there are several teams
  // (only its agents see them); "global" keeps an install for everyone.
  const [toAll, setToAll] = useState(false);
  const target: Target = teams.length > 1 && !toAll ? { context: "team:" + (team || DEFAULT_TEAM) } : {};
  useEffect(() => {
    if (want) {
      setView(want === "mcp" || want === "installed" ? "installed" : "market");
      setState({ settingsTab: null });
    }
  }, [want]);
  // events can trigger reloads in quick succession: only the newest answer
  // counts, and a failed reload keeps what is shown (empty only at first)
  const seq = useRef(0);
  const reload = () => {
    const n = ++seq.current;
    const ok = () => n === seq.current;
    catalogAPI.list().then(
      (catalog) => ok() && setD((x) => ({ ...x, catalog })),
      () => ok() && setD((x) => ({ ...x, catalog: x.catalog ?? { categories: [], items: [] } })),
    );
    API.mcp().then(
      (r) => ok() && setD((x) => ({ ...x, servers: r.servers ?? [] })),
      () => ok() && setD((x) => ({ ...x, servers: x.servers ?? [] })),
    );
    catalogAPI.skills().then(
      (r) => ok() && setD((x) => ({ ...x, skills: r.skills ?? [] })),
      () => ok() && setD((x) => ({ ...x, skills: x.skills ?? [] })),
    );
    API.plugins().then(
      (r) => ok() && setD((x) => ({ ...x, plugins: r.plugins ?? [] })),
      () => ok() && setD((x) => ({ ...x, plugins: x.plugins ?? [] })),
    );
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

  const shared: Shared = {
    d,
    q: q.trim().toLowerCase(),
    reload,
    reloadMarket,
    onApprove: setApprove,
    onSignIn: setGh,
    onScope: setScopeOf,
    onAccess: setAccessOf,
    target,
    toAll,
    setToAll,
    onManage: (server) => {
      setFocus(server);
      setView("installed");
    },
    focus,
    onPaste: (text, picks) => setPaste({ text, picks }),
    search: null,
  };
  const search = (
    <label className={"search mksearch" + (view === "market" ? " inline" : "")}>
      <Search size={15} />
      <input type="search" value={q} placeholder={t("mk.search")} aria-label={t("mk.search")} onChange={(e) => setQ(e.target.value)} />
      {q && (
        <button type="button" className="ix" aria-label={t("common.close")} onClick={() => setQ("")}>
          <X size={13} />
        </button>
      )}
    </label>
  );
  const installedN = (d.servers ?? []).filter((x) => !isDocsServer(x.name)).length + (d.skills?.length ?? 0) + (d.plugins?.length ?? 0);
  return (
    <>
      <Head
        title={t("set.market")}
        sub={t("mk.sub")}
        right={
          <div className="mkhr">
          <div className="seg" role="group">
            <button type="button" aria-pressed={view === "market"} onClick={() => setView("market")}>
              {t("mk3.tabDiscover")}
            </button>
            <button type="button" aria-pressed={view === "installed"} onClick={() => setView("installed")}>
              {t("mk.tabInstalled")}
              {nUpd > 0 ? <span className="segn upd" title={t("aup.available", { n: nUpd })}>{nUpd}</span> : installedN > 0 && <span className="segn">{installedN}</span>}
            </button>
          </div>
          </div>
        }
      />
      <div className="vb mkwrap">
        {view === "market" ? (
          <>
            {nUpd > 0 && (
              <button className="updbar" type="button" onClick={() => setView("installed")}>
                <span className="updot static" />
                {t("aup.available", { n: nUpd })}
                <span className="sp" />
                {t("aup.see")}
              </button>
            )}
            <MarketView {...shared} search={search} />
          </>
        ) : (
          <>
            <UpdatesSection u={upd} onChanged={reload} onApprove={setApprove} />
            {search}
            <InstalledView {...shared} />
          </>
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
      {paste !== null && (
        <PasteInstall
          initial={paste.text}
          picks={paste.picks}
          onClose={() => setPaste(null)}
          onDone={() => {
            reload();
            setView("installed");
          }}
          onApprove={setApprove}
        />
      )}
      {gh && <GitHubDialog item={gh} target={target} servers={d.servers ?? []} onClose={() => setGh(null)} onChanged={reload} />}
      {accessOf && <AccessDialog server={accessOf} label={d.catalog?.items.find((it) => it.server === accessOf.name)?.name ?? accessOf.name} onClose={() => setAccessOf(null)} />}
      {scopeOf && <ScopeDialog server={scopeOf} label={d.catalog?.items.find((it) => it.server === scopeOf.name)?.name ?? scopeOf.name} onClose={() => setScopeOf(null)} onSaved={reload} />}
    </>
  );
}

interface Shared {
  onPaste: (text: string, picks?: string[]) => void;
  /** the search box, placed by the view */
  search: ReactNode;
  d: Data;
  q: string;
  reload: () => void;
  reloadMarket: () => Promise<void>;
  onApprove: (p: PluginView) => void;
  /** Opens the built-in sign-in (GitHub device flow) for an item. */
  onSignIn: (it: CatItem) => void;
  onScope: (s: McpServer) => void;
  /** Opens the per-agent checklist for a connector. */
  onAccess: (s: McpServer) => void;
  /** Where new connections go (Target). */
  target: Target;
  toAll: boolean;
  setToAll: (v: boolean) => void;
  onManage: (server: string) => void;
  focus: string | null;
}

/** v1.2.3 (a version that already starts with v or is not a number as is). */
export const verLabel = (v: string) => (/^\d/.test(v.trim()) ? "v" + v.trim() : v.trim());

/** The MCP servers features/agentdocs makes (one per agent): shown as the
 *  Open Agent Docs app, not as connectors. */
const isDocsServer = (name: string) => /^docs-[0-9a-f]{8}$/.test(name);

function matches(q: string, ...xs: (string | undefined)[]) {
  return !q || xs.some((x) => x?.toLowerCase().includes(q));
}

/** The 40px tile in front of a row. */
function ItemIcon({ icon, id, label, kind }: { icon?: string; id: string; label: string; kind?: string }) {
  const b = icon ? BRAND[icon] : undefined;
  if (b) return <span className={"mkic brand" + (b.fit ? " " + b.fit : "")} aria-hidden dangerouslySetInnerHTML={{ __html: b.svg }} />;
  if (kind === "skill") return <span className="mkic">{(icon && SKILL_ICONS[icon]) || <Sparkles size={19} />}</span>;
  if (kind === "plugin") return <span className="mkic"><Plug size={19} /></span>;
  return <MonoMark id={id} label={label} size={40} />;
}

// ---------------------------------------------------------------- marketplace

/** What a row's action area shows. */
type RowState = "idle" | "busy" | "connected" | "added" | "login" | "reauth" | "error" | "connecting";

interface Row {
  key: string;
  name: string;
  desc: string;
  icon: ReactNode;
  state: RowState;
  /** connect (MCP) or add (skill, plugin) */
  verb: "connect" | "add";
  error?: string;
  onAct: () => void;
  menu?: MenuItem[];
  /** how connecting goes: 免登录 / 官方登录 / 需要令牌 … */
  badge?: { label: string; tone: "free" | "login" | "token" | "" };
  /** connected, with a 管理 button instead of the 已连接 label */
  manage?: boolean;
  /** shown by the name (plugins: the marketplace's; installed: what is installed) */
  version?: string;
}

/** The badge that says what connecting an item takes. */
function badgeOf(it: CatItem): Row["badge"] {
  if (it.kind === "skill") return undefined;
  switch (it.auth) {
    case "none":
      return (it.inputs ?? []).some((x) => !x.optional) ? { label: t("mk3.b.token"), tone: "token" } : { label: t("mk3.b.free"), tone: "free" };
    case "oauth":
      return { label: t("mk3.b.login"), tone: "login" };
    case "device":
      return { label: t("mk3.b.device"), tone: "login" };
    case "token":
      return { label: t("mk3.b.token"), tone: "token" };
    default:
      return { label: t("mk3.b.setup"), tone: "token" };
  }
}

function MarketView({ d, q, reload, reloadMarket, onApprove, onSignIn, onManage, target, toAll, setToAll, onPaste, search }: Shared) {
  const [kind, setKind] = useState<"all" | "mcp" | "skill" | "plugin">("all");
  const teams = useStore((s) => s.teams);
  const team = useStore((s) => s.team);
  const teamName = teams.find((x) => x.id === team)?.name ?? team;
  const [busy, setBusy] = useState<Record<string, boolean>>({});
  const [errs, setErrs] = useState<Record<string, string>>({});
  const [dialog, setDialog] = useState<CatItem | null>(null);
  const docs = useAgentDocs();
  const [docsOpen, setDocsOpen] = useState(false);
  const oa = useOpenArtifact();
  const [oaOpen, setOaOpen] = useState(false);
  const servers = d.servers ?? [];
  const mark = (id: string, on: boolean) => setBusy((b) => ({ ...b, [id]: on }));
  const fail = (id: string, e: unknown) => setErrs((x) => ({ ...x, [id]: errMsg(e) }));

  /** 连接: straight to the service where nothing must be asked (no-auth
   *  servers connect, OAuth servers open the service's own sign-in page);
   *  a dialog only for what must be entered or done first. */
  const connect = async (it: CatItem) => {
    setErrs((x) => ({ ...x, [it.id]: "" }));
    const how = connectHow(it);
    if (how === "dialog") return setDialog(it);
    if (how === "device") return onSignIn(it);
    const win = how === "oauth" ? signInWindow() : null;
    mark(it.id, true);
    try {
      const r = await catalogAPI.install(it.id, it.kind === "skill" ? {} : target);
      reload();
      if (it.kind === "skill") return toast(t("mk.added", { name: it.name }));
      const s = r.server && (await settle(r.server.name));
      reload();
      const st = s?.status;
      if (st?.state === "ready") {
        win?.(null);
        toast(t("mk2.connectedToast", { name: it.name }));
      } else if (st?.state === "login" && st.loginUrl) {
        if (win?.(st.loginUrl)) toast(t("mk2.signInOpened", { name: it.name }));
        else setErrs((x) => ({ ...x, [it.id]: t("mk2.popupBlocked") }));
      } else {
        win?.(null);
        if (st?.error) fail(it.id, st.error);
      }
    } catch (e) {
      win?.(null);
      fail(it.id, e);
    } finally {
      mark(it.id, false);
    }
  };
  const disconnect = async (it: CatItem) => {
    if (!(await confirmDialog(t("mk2.disconnectConfirm", { name: it.name })))) return;
    mark(it.id, true);
    catalogAPI
      .uninstall(it.id)
      .then(() => toast(t("mk2.disconnected", { name: it.name })), (e) => toast(errMsg(e)))
      .finally(() => {
        mark(it.id, false);
        reload();
      });
  };
  const addPlugin = async (m: MarketEntry) => {
    mark("plugin:" + m.name, true);
    try {
      const v = await API.install({ market: m.name });
      toast(t("mk.added", { name: v.name }));
      reload();
      void reloadMarket();
      if (!v.approved) onApprove(v);
    } catch (e) {
      toast(t("mk.addFailed", { error: errMsg(e) }));
    } finally {
      mark("plugin:" + m.name, false);
    }
  };

  const rowOf = (it: CatItem): Row => {
    const base = { key: it.id, name: it.name, desc: tx(it.desc), badge: badgeOf(it), icon: <ItemIcon icon={it.icon} id={it.id} label={it.name} kind={it.kind} /> };
    if (it.kind === "skill")
      return { ...base, verb: "add", state: busy[it.id] ? "busy" : it.installed ? "added" : "idle", error: errs[it.id], onAct: () => void connect(it) };
    const s = it.installed ? servers.find((x) => x.name === it.server) : undefined;
    const st = s?.status;
    let state: RowState = "idle";
    let error = errs[it.id];
    if (busy[it.id]) state = "busy";
    else if (it.installed) {
      if (!s || st?.state === "ready" || st?.state === "disabled") state = "connected";
      else if (st?.state === "login") state = "login";
      else if (needsReauth(s)) state = "reauth";
      else if (st?.state === "error") {
        state = "error";
        error = st.error || error;
      } else state = "connecting";
    }
    return {
      ...base,
      verb: "connect",
      state,
      error: state === "idle" || state === "error" ? error : undefined,
      onAct: () => {
        if (state === "login" && st?.loginUrl) openExternal(st.loginUrl);
        else if (state === "reauth" && it.auth === "device") onSignIn(it);
        else if (state === "reauth" || state === "error") onManage(it.server!);
        else void connect(it);
      },
      menu: it.installed
        ? [
            { label: t("mk2.manage"), onClick: () => onManage(it.server!) },
            { label: t("mk2.disconnect"), danger: true, onClick: () => disconnect(it) },
          ]
        : undefined,
    };
  };

  if (!d.catalog) return <Skeleton />;
  type Sec = { id: string; kind: "mcp" | "skill" | "plugin"; title: string; desc: string; rows: Row[] };
  const cats = d.catalog.categories;
  const pick = (ids: string[]) =>
    d
      .catalog!.items.filter((it) => ids.includes(it.category))
      .filter((it) => {
        const c = cats.find((x) => x.id === it.category);
        return matches(q, it.name, it.desc.zh, it.desc.en, c?.title.zh, c?.title.en);
      })
      .map(rowOf);
  const connectorCats = cats.filter((c) => c.id !== "skills").map((c) => c.id);
  const skillCat = cats.find((c) => c.id === "skills");
  // every connector in one grid (categories only order it), skills in another
  const sections: Sec[] = [
    { id: "connectors", kind: "mcp", title: t("mk3.k.mcp"), desc: t("mk3.connectorsDesc"), rows: pick(connectorCats) },
    { id: "skills", kind: "skill", title: skillCat ? tx(skillCat.title) : t("mk3.k.skill"), desc: skillCat ? tx(skillCat.desc) : "", rows: pick(["skills"]) },
  ];
  sections.push({
    id: "plugins",
    kind: "plugin",
    title: t("mk.plugins"),
    desc: d.marketErr ? t("ext.marketErr", { error: d.marketErr }) : t("mk.pluginsDesc"),
    rows: (d.market ?? [])
      .filter((m) => matches(q, m.name, m.description, m.author))
      .map((m) => ({
        key: "plugin:" + m.name,
        name: m.name,
        desc: m.description,
        version: m.installed && m.installed !== m.version ? m.installed + " → " + m.version : m.version,
        icon: <ItemIcon id={m.name} label={m.name} kind="plugin" />,
        badge: { label: t("mk3.b.perms"), tone: "" as const },
        verb: "add" as const,
        state: (busy["plugin:" + m.name] ? "busy" : m.installed ? "added" : "idle") as RowState,
        onAct: () => void addPlugin(m),
      })),
  });
  const libs = (d.catalog.libraries ?? []).filter((l) => matches(q, l.name, l.author, l.desc.zh, l.desc.en));
  const count = (k: Sec["kind"]) => sections.filter((x) => x.kind === k).reduce((a, x) => a + x.rows.length, 0);
  const shown = sections.filter((x) => x.rows.length > 0 && (kind === "all" || x.kind === kind));
  const kinds = [
    { id: "all" as const, label: t("mk3.k.all"), n: count("mcp") + count("skill") + count("plugin") },
    { id: "mcp" as const, label: t("mk3.k.mcp"), n: count("mcp") },
    { id: "skill" as const, label: t("mk3.k.skill"), n: count("skill") },
    { id: "plugin" as const, label: t("mk3.k.plugin"), n: count("plugin") },
  ];
  const docsShown = (kind === "all" || kind === "mcp") && matches(q, "Open Agent Docs", t("ad.short"), "文档 docs notion");
  const docsRow: Row = {
    key: "app:agentdocs",
    name: "Open Agent Docs",
    desc: t("ad.short"),
    icon: <AgentDocsIcon />,
    badge: { label: t("ad.badge"), tone: "" as const },
    verb: "connect",
    state: docs.st?.connected ? (docs.st.error ? "error" : "connected") : "idle",
    error: docs.st?.connected ? (docs.st.needsAdmin ? t("ad.needsAdmin") : docs.st.error) : undefined,
    onAct: () => setDocsOpen(true),
    manage: true,
  };
  const oaShown = (kind === "all" || kind === "mcp") && matches(q, "Open Artifact", t("oa.short"), "幻灯片 slides 设计 design 白板 whiteboard 表格 sheet ppt");
  const oaRow: Row = {
    key: "app:open-artifact",
    name: "Open Artifact",
    desc: t("oa.short"),
    icon: <OpenArtifactIcon />,
    badge: { label: t("oa.badge"), tone: "" as const },
    verb: "add",
    version: oaInstalled(oa.st) ? oa.st!.version : undefined,
    state: oa.st?.busy ? "busy" : oaInstalled(oa.st) ? (oa.st!.enabled && oa.st!.error ? "error" : "added") : "idle",
    error: oa.st?.enabled && oa.st.error ? oa.st.error : undefined,
    onAct: () => setOaOpen(true),
    manage: true,
  };
  const apps = [...(docsShown ? [docsRow] : []), ...(oaShown ? [oaRow] : [])];
  return (
    <>
      {!q && <PasteHero onPaste={onPaste} />}
      <div className="mkkinds" role="tablist" aria-label={t("mk3.kinds")}>
        {kinds.map((k) => (
          <button key={k.id} type="button" role="tab" aria-selected={kind === k.id} className={"mkkind" + (kind === k.id ? " on" : "")} onClick={() => setKind(k.id)}>
            {k.label}
            <span>{k.n}</span>
          </button>
        ))}
        <span className="sp" />
        {search}
        {teams.length > 1 && (
          <span className="mkto small">
            <span>{t("mk2.installTo")}</span>
            <span className="ssel">
              <select value={toAll ? "global" : "team"} aria-label={t("mk2.installTo")} onChange={(e) => setToAll(e.target.value === "global")}>
                <option value="team">{t("ext.scopeTeam", { name: teamName })}</option>
                <option value="global">{t("ext.scopeGlobal")}</option>
              </select>
            </span>
          </span>
        )}
      </div>
      {apps.length > 0 && <Section title={t("ad.section")} desc={t("ad.sectionDesc")} rows={apps} />}
      {shown.length === 0 && apps.length === 0 && <div className="empty">{t("mk.noResults", { q })}</div>}
      {shown.map((x) => (
        <Fragment key={x.id}>
          <Section title={x.title} desc={x.desc} rows={x.rows} />
          {x.kind === "skill" && libs.length > 0 && <Libraries libs={libs} onOpen={(l) => onPaste(l.repo, l.picks)} />}
        </Fragment>
      ))}
      {kind === "skill" && !shown.some((x) => x.kind === "skill") && libs.length > 0 && <Libraries libs={libs} onOpen={(l) => onPaste(l.repo, l.picks)} />}
      {docsOpen && <AgentDocsDialog st={docs.st} onClose={() => setDocsOpen(false)} onChanged={(x) => {
            docs.setSt(x);
            void refreshAgentDocs();
          }} />}
      {oaOpen && <OpenArtifactDialog onClose={() => setOaOpen(false)} />}
      {dialog && <ConnectDialog item={dialog} target={target} servers={servers} onClose={() => setDialog(null)} onChanged={reload} onSignIn={onSignIn} />}
    </>
  );
}

/** 热门技能库: popular GitHub repositories of skills; opening one lists its
 *  skills in the install dialog, the suggested ones ticked. */
function Libraries({ libs, onOpen }: { libs: SkillLibrary[]; onOpen: (l: SkillLibrary) => void }) {
  return (
    <section className="mksec">
      <div className="mkst2">
        <h3>{t("mk3.libs")}</h3>
        <span className="small">{t("mk3.libsDesc")}</span>
      </div>
      <div className="mkcards">
        {libs.map((l) => (
          <div className="mkcard" key={l.id}>
            <div className="mkctop">
              <img className="mkav" src={`https://github.com/${l.author}.png?size=80`} alt="" loading="lazy" referrerPolicy="no-referrer" />
              <div className="g">
                <b title={l.name}>{l.name}</b>
                <span className="mkbadge">{t("mk3.libBy", { author: l.author })}</span>
              </div>
              <a className="ix" href={l.repo} target="_blank" rel="noreferrer noopener" title={l.repo} aria-label={l.repo}>
                <ExternalLink size={13} />
              </a>
            </div>
            <p className="mkcdesc" title={tx(l.desc)}>
              {tx(l.desc)}
            </p>
            <div className="mkcfoot">
              <button className="btn sm mkconn" type="button" onClick={() => onOpen(l)}>
                {t("mk3.libPick")}
              </button>
            </div>
          </div>
        ))}
      </div>
    </section>
  );
}

/** The install-anything box at the top of 发现: paste and it is recognised. */
function PasteHero({ onPaste }: { onPaste: (text: string) => void }) {
  const [v, setV] = useState("");
  return (
    <div className="mkhero">
      <div className="mkherot">
        <ClipboardPaste size={18} />
        <div>
          <b>{t("mk3.heroTitle")}</b>
          <span>{t("mk3.heroDesc")}</span>
        </div>
      </div>
      <form
        className="mkheroin"
        onSubmit={(e) => {
          e.preventDefault();
          if (v.trim()) onPaste(v);
        }}
      >
        <input
          className="fi mono"
          value={v}
          spellCheck={false}
          autoComplete="off"
          placeholder={t("mk3.heroPh")}
          aria-label={t("mk3.heroTitle")}
          onChange={(e) => setV(e.target.value)}
          onPaste={(e) => {
            const text = e.clipboardData.getData("text");
            if (text.trim() && !v.trim()) {
              e.preventDefault();
              onPaste(text);
            }
          }}
        />
        <button className="btn pri" type="submit" disabled={!v.trim()}>
          {t("mk3.heroGo")}
        </button>
      </form>
      <div className="mkherofmt">
        {(["mk3.f.config", "mk3.f.cmd", "mk3.f.url", "mk3.f.github", "mk3.f.npm"] as const).map((k) => (
          <span key={k}>{t(k)}</span>
        ))}
      </div>
    </div>
  );
}

function RowAction({ r }: { r: Row }) {
  // set up (connected, or installed like Open Artifact): 管理 opens it again
  if (r.manage && (r.state === "connected" || r.state === "added"))
    return (
      <span className="mkmanage">
        <span className="mkst ok">
          <Check size={13} />
          {r.state === "connected" ? t("mk2.connected") : t("mk2.added")}
        </span>
        <button className="btn sm" type="button" onClick={r.onAct}>
          {t("mk2.manage")}
        </button>
      </span>
    );
  const label = r.verb === "connect" ? t("mk2.connectName", { name: r.name }) : t("mk2.addName", { name: r.name });
  switch (r.state) {
    case "connected":
    case "added":
      return (
        <span className="mkst ok">
          <Check size={13} />
          {r.state === "connected" ? t("mk2.connected") : t("mk2.added")}
        </span>
      );
    case "connecting":
      return (
        <span className="mkst">
          <span className="spin sm" />
          {t("mk2.connecting")}
        </span>
      );
    case "login":
    case "reauth":
      return (
        <button className="btn sm pri" type="button" onClick={r.onAct}>
          <LogIn size={12} />
          {r.state === "login" ? t("mk2.signIn") : t("mk2.signInAgain")}
        </button>
      );
    case "error":
      // something went wrong: say so, and offer the way to fix it
      return (
        <span className="mkerr">
          <span className="mkst err">
            <i className="sdot err" />
            {t("mk2.failed")}
          </span>
          {r.onAct && (
            <button className="btn sm" type="button" onClick={r.onAct}>
              {t("mk2.fix")}
            </button>
          )}
        </span>
      );
    default:
      return (
        <button className="btn sm mkconn" type="button" disabled={r.state === "busy"} aria-label={label} title={label} onClick={r.onAct}>
          {r.state === "busy" ? <span className="spin sm" /> : null}
          {r.state === "busy" ? t("mk2.connecting") : r.verb === "connect" ? t("mk2.connect") : t("mk2.add")}
        </button>
      );
  }
}

function Section({ title, desc, rows }: { title: string; desc: string; rows: Row[] }) {
  return (
    <section className="mksec">
      <div className="mkst2">
        <h3>{title}</h3>
        {desc && <span className="small">{desc}</span>}
      </div>
      <div className="mkcards">
        {rows.map((r) => (
          <div className={"mkcard" + (r.state === "connected" || r.state === "added" ? " done" : "")} key={r.key}>
            <div className="mkctop">
              {r.icon}
              <div className="g">
                <b title={r.name}>
                  {r.name}
                  {r.version && <span className="mkver">{r.version.includes("→") ? r.version.split(" → ").map(verLabel).join(" → ") : verLabel(r.version)}</span>}
                </b>
                {r.badge && <span className={"mkbadge " + r.badge.tone}>{r.badge.label}</span>}
              </div>
              {r.menu && <MoreMenu items={r.menu} />}
            </div>
            <p className="mkcdesc" title={r.desc}>
              {r.desc}
            </p>
            {r.error && (
              <p className="errt mkcerr" title={r.error}>
                {r.error}
              </p>
            )}
            <div className="mkcfoot">
              <RowAction r={r} />
            </div>
          </div>
        ))}
      </div>
    </section>
  );
}

function useEsc(onClose: () => void) {
  useEffect(() => {
    const esc = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", esc);
    return () => window.removeEventListener("keydown", esc);
  }, [onClose]);
}

/** A portalled modal (the page body is a size container, which would
 *  otherwise be the containing block of the fixed scrim and modal). */
function Modal({ item, title, onClose, children }: { item: CatItem; title: string; onClose: () => void; children: ReactNode }) {
  useEsc(onClose);
  return createPortal(
    <>
      <div className="scrim" onClick={onClose} />
      <div className="modal mkdlg" role="dialog" aria-modal="true" aria-labelledby="mkTitle">
        <div className="mhd">
          <ItemIcon icon={item.icon} id={item.id} label={item.name} kind={item.kind} />
          <h3 id="mkTitle">{title}</h3>
          <button className="ix" type="button" aria-label={t("common.close")} onClick={onClose}>
            <X size={14} />
          </button>
        </div>
        <div className="mbd">{children}</div>
      </div>
    </>,
    document.body,
  );
}

/** For items that need something first: values to paste (a token, the
 *  Google client) or steps to take (Figma's Dev Mode server). Then follows
 *  the connection until it is up or fails. */
function ConnectDialog({ item, target, servers, onClose, onChanged, onSignIn }: { item: CatItem; target: Target; servers: McpServer[]; onClose: () => void; onChanged: () => void; onSignIn?: (it: CatItem) => void }) {
  const [vals, setVals] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [name, setName] = useState<string | null>(null); // the server, once added
  const st = name ? servers.find((s) => s.name === name)?.status : undefined;
  const inputs = item.inputs ?? [];
  const partOf = item.server && !item.installed ? item.server : null; // a further part of an existing server
  const ready = inputs.every((x) => x.optional || (vals[x.id] ?? "").trim());
  useEffect(() => {
    if (st?.state === "ready") {
      toast(t("mk2.connectedToast", { name: item.name }));
      onClose();
    }
  }, [st?.state]);

  const submit = async () => {
    setBusy(true);
    setErr(null);
    try {
      const r = await catalogAPI.install(item.id, { inputs: vals, ...target });
      onChanged();
      if (r.server) setName(r.server.name);
      else onClose();
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal item={item} title={t("mk2.dlgTitle", { name: item.name })} onClose={onClose}>
      <p className="small mkdesc">{tx(item.desc)}</p>
      {name ? (
        <div className="mksign">
          {st?.state === "error" ? (
            <p className="small errt">{st.error}</p>
          ) : (
            <p className="small">
              <span className="spin sm" /> {t("mk2.connecting")}
            </p>
          )}
          <div className="mft">
            <button className="btn pri" type="button" onClick={onClose}>
              {t("mk2.done")}
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
          {err && <div className="ferr">{err}</div>}
          <div className="mft">
            {item.signIn && onSignIn ? (
              <button className="btn ghost" type="button" style={{ marginRight: "auto" }} onClick={() => (onClose(), onSignIn(item))}>
                {t("mk2.useSignIn", { name: item.name })}
              </button>
            ) : (
              item.docs && (
                <a className="btn ghost" href={item.docs} target="_blank" rel="noreferrer noopener" style={{ marginRight: "auto" }}>
                  {t("mk.docs")}
                </a>
              )
            )}
            <button className="btn ghost" type="button" onClick={onClose}>
              {t("common.cancel")}
            </button>
            <button className="btn pri" type="button" disabled={busy || !ready} onClick={submit}>
              {busy ? t("mk2.connecting") : t("mk2.connect")}
            </button>
          </div>
        </>
      )}
    </Modal>
  );
}

/** GitHub's device sign-in: shows the code to enter at github.com/login/device
 *  and waits for the person to approve; Crewbase then keeps the token in the
 *  vault and connects GitHub's MCP server. A personal access token is
 *  the fallback. */
function GitHubDialog({ item, target, servers, onClose, onChanged }: { item: CatItem; target: Target; servers: McpServer[]; onClose: () => void; onChanged: () => void }) {
  const [v, setV] = useState<SignInView | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [pat, setPat] = useState(false);
  const [copied, setCopied] = useState(false);
  const [n, setN] = useState(0); // restarts
  const done = useRef(false);
  useEffect(() => {
    let live = true;
    setV(null);
    setErr(null);
    catalogAPI.signIn(item.id, target).then(
      async (first) => {
        if (!live) return;
        setV(first);
        let cur = first;
        while (live && cur.state === "pending") {
          await sleep(1500);
          if (!live) return;
          try {
            cur = await catalogAPI.signInState(item.id);
          } catch {
            continue;
          }
          if (live) setV(cur);
        }
        if (live && cur.state === "done") {
          done.current = true;
          onChanged();
          if (!cur.noInstallation) {
            toast(t("mk2.connectedToast", { name: item.name }));
            onClose();
          }
        }
      },
      (e) => live && setErr(errMsg(e)),
    );
    return () => {
      live = false;
    };
  }, [n]);
  const close = () => {
    if (!done.current) void catalogAPI.cancelSignIn(item.id).catch(() => {});
    onClose();
  };
  // the live server list: the dialog follows the server's state after the token is saved
  if (pat) return <ConnectDialog item={{ ...item, auth: "token" }} target={target} servers={servers} onClose={onClose} onChanged={onChanged} />;
  const mins = v?.expiresAt ? Math.max(1, Math.round((Date.parse(v.expiresAt) - Date.now()) / 60000)) : 15;
  const copy = () =>
    void navigator.clipboard?.writeText(v?.userCode ?? "").then(
      () => {
        setCopied(true);
        setTimeout(() => setCopied(false), 1500);
      },
      () => {},
    );
  const url = v?.verificationUri || "https://github.com/login/device";
  return (
    <Modal item={item} title={t("mk2.ghTitle")} onClose={close}>
      {err ? (
        <>
          <p className="small errt mkghmsg">{err}</p>
          <div className="mft">
            <button className="btn ghost" type="button" style={{ marginRight: "auto" }} onClick={() => setPat(true)}>
              {t("mk2.usePat")}
            </button>
            <button className="btn pri" type="button" onClick={() => setN(n + 1)}>
              {t("mk2.retry")}
            </button>
          </div>
        </>
      ) : !v ? (
        <p className="small mkghmsg">
          <span className="spin sm" /> {t("mk2.ghGetting")}
        </p>
      ) : v.state === "done" ? (
        <>
          <p className="small okt mkghmsg">
            <Check size={13} /> {t("mk2.ghDone")}
          </p>
          {v.noInstallation && <p className="mknote">{t("mk2.ghNoInstall")}</p>}
          <div className="mft">
            {v.installUrl && (
              <a className="btn" href={v.installUrl} target="_blank" rel="noreferrer noopener" style={{ marginRight: "auto" }}>
                <ExternalLink size={13} /> {t("mk2.ghInstall")}
              </a>
            )}
            <button className="btn pri" type="button" onClick={onClose}>
              {t("mk2.done")}
            </button>
          </div>
        </>
      ) : v.state === "pending" ? (
        <>
          <p className="small mkdesc">{t("mk2.ghStep")}</p>
          <div className="mkcode">
            <code aria-label={t("mk2.ghCode")}>{v.userCode}</code>
            <button className="btn sm" type="button" onClick={copy}>
              {copied ? <Check size={13} /> : <Copy size={13} />}
              {copied ? t("mk2.copied") : t("mk2.copy")}
            </button>
          </div>
          <button className="btn pri mkghopen" type="button" onClick={() => openExternal(url)}>
            <ExternalLink size={14} /> {t("mk2.ghOpen", { host: url.replace(/^https?:\/\//, "") })}
          </button>
          <p className="small mkghwait">
            <span className="spin sm" /> {t("mk2.ghWaiting", { min: mins })}
          </p>
          <div className="mft">
            <button className="btn ghost" type="button" style={{ marginRight: "auto" }} onClick={() => (void catalogAPI.cancelSignIn(item.id).catch(() => {}), setPat(true))}>
              {t("mk2.usePat")}
            </button>
            <button className="btn ghost" type="button" onClick={close}>
              {t("common.cancel")}
            </button>
          </div>
        </>
      ) : (
        <>
          <p className="small errt mkghmsg">{v.state === "denied" ? t("mk2.ghDenied") : v.state === "expired" ? t("mk2.ghExpired") : v.error}</p>
          <div className="mft">
            <button className="btn ghost" type="button" style={{ marginRight: "auto" }} onClick={() => setPat(true)}>
              {t("mk2.usePat")}
            </button>
            <button className="btn pri" type="button" onClick={() => setN(n + 1)}>
              {t("mk2.retry")}
            </button>
          </div>
        </>
      )}
    </Modal>
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

/** Who may use a connected service: everyone (default), a team or an agent. */
function ScopeDialog({ server, label, onClose, onSaved }: { server: McpServer; label: string; onClose: () => void; onSaved: () => void }) {
  const [scope, setScope] = useState(server.scope || "global");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  useEsc(onClose);
  const save = async () => {
    setBusy(true);
    try {
      await API.patchMcp(server.name, { scope });
      toast(t("mk2.scopeSaved", { name: label }));
      onSaved();
      onClose();
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  };
  return createPortal(
    <>
      <div className="scrim" onClick={onClose} />
      <div className="modal sm mkdlg" role="dialog" aria-modal="true" aria-labelledby="mkScope">
        <div className="mhd">
          <h3 id="mkScope">{t("mk2.scopeTitle", { name: label })}</h3>
          <button className="ix" type="button" aria-label={t("common.close")} onClick={onClose}>
            <X size={14} />
          </button>
        </div>
        <div className="mbd">
          <ScopeSelect value={scope} onChange={setScope} teamsToo />
          {err && <div className="ferr">{err}</div>}
          <div className="mft">
            <button className="btn ghost" type="button" onClick={onClose}>
              {t("common.cancel")}
            </button>
            <button className="btn pri" type="button" disabled={busy || scope === (server.scope || "global")} onClick={save}>
              {t("mk2.save")}
            </button>
          </div>
        </div>
      </div>
    </>,
    document.body,
  );
}

/** Which agents may use one connector: each switch edits that agent's
 *  connector list (可用的连接器), so the others never get its tools. */
function AccessDialog({ server, label, onClose }: { server: McpServer; label: string; onClose: () => void }) {
  const agents = useStore((s) => s.agents);
  const all = useConnectors();
  const [busy, setBusy] = useState<string | null>(null);
  useEsc(onClose);
  const list = Object.values(agents).filter((a) => a.kind !== "chat" && !a.ephemeral && all && connectorsFor(all, a).some((c) => c.name === server.name));
  const set = async (a: AgentView, on: boolean) => {
    setBusy(a.id);
    try {
      await API.patchAgent(a.id, { connectors: nextConnectors(a, connectorsFor(all ?? [], a), server.name, on) });
    } catch (e) {
      toast(errMsg(e));
    } finally {
      setBusy(null);
    }
  };
  return createPortal(
    <>
      <div className="scrim" onClick={onClose} />
      <div className="modal sm mkdlg" role="dialog" aria-modal="true" aria-labelledby="mkAccess">
        <div className="mhd">
          <h3 id="mkAccess">{t("mk2.accessTitle", { name: label })}</h3>
          <button className="ix" type="button" aria-label={t("common.close")} onClick={onClose}>
            <X size={14} />
          </button>
        </div>
        <div className="mbd">
          <p className="small">{t("mk2.accessDesc", { n: server.status?.tools ?? 0 })}</p>
          {!all ? (
            <Skeleton />
          ) : list.length === 0 ? (
            <p className="small">{t("mk2.accessNone")}</p>
          ) : (
            <div className="tglist">
              {list.map((a) => (
                <div className="tgrow" key={a.id}>
                  <span className="g">
                    <b>{a.name}</b>
                    <span>{a.connectors == null ? t("mk2.accessAll") : t("mk2.accessListed", { n: a.connectors.length })}</span>
                  </span>
                  <button
                    className="tg"
                    type="button"
                    role="switch"
                    disabled={busy === a.id}
                    aria-checked={canUse(a, server.name)}
                    aria-label={a.name}
                    onClick={() => set(a, !canUse(a, server.name))}
                  />
                </div>
              ))}
            </div>
          )}
          <div className="mft">
            <button className="btn pri" type="button" onClick={onClose}>
              {t("mk2.done")}
            </button>
          </div>
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

/** A connected server's status in words: 已连接 / 需要重新登录 / 出错 … */
function stLabel(s: McpServer) {
  if (needsReauth(s)) return t("mk2.reauth");
  const k = `ext.mcpSt.${s.status?.state}` as Key;
  return t(k) === k ? s.status?.state || "—" : t(k);
}

function InstalledView({ d, q, reload, reloadMarket, onApprove, onSignIn, onScope, onAccess, focus }: Shared) {
  const agents = useStore((s) => s.agents);
  const teams = useStore((s) => s.teams);
  const [adding, setAdding] = useState(false);
  const [open, setOpen] = useState<string | null>(focus);
  const [busy, setBusy] = useState<string | null>(null);
  const byServer = useMemo(() => {
    const m: Record<string, CatItem> = {};
    for (const it of d.catalog?.items ?? []) if (it.kind === "mcp" && it.server && !m[it.server]) m[it.server] = it;
    return m;
  }, [d.catalog]);
  const builtinSkills = useMemo(() => new Map((d.catalog?.items ?? []).filter((it) => it.kind === "skill").map((it) => [it.name, it])), [d.catalog]);
  const sharedDir = (d.skills ?? []).find((s) => !s.agent)?.source;
  useEffect(() => {
    if (focus) document.getElementById("mk-srv-" + focus)?.scrollIntoView({ block: "center" });
  }, [focus, d.servers === null]);

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

  const servers = (d.servers ?? []).filter((s) => !isDocsServer(s.name)).filter((s) => matches(q, s.name, byServer[s.name]?.name, s.url, (s.command ?? []).join(" ")));
  const skills = (d.skills ?? []).filter((s) => matches(q, s.name, s.description));
  const plugins = (d.plugins ?? []).filter((p) => matches(q, p.name, p.description));
  const loading = d.servers === null || d.skills === null || d.plugins === null;
  if (loading) return <Skeleton />;

  return (
    <>
      <section className="mksec">
        <div className="mksh">
          <h3>
            {t("mk2.instServices")} <span className="mkcount">{servers.length}</span>
          </h3>
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
          !adding && <p className="small">{t("mk2.noneServices")}</p>
        ) : (
          <div className="mklist">
            {servers.map((s) => {
              const it = byServer[s.name];
              const label = it?.name ?? s.name;
              const st = s.status?.state;
              const reauth = needsReauth(s);
              return (
                <div className={"mkirow" + (s.enabled ? "" : " off")} key={s.name} id={"mk-srv-" + s.name}>
                  <div className="mkrow">
                    <ItemIcon icon={it?.icon} id={s.name} label={label} kind={it ? it.kind : "server"} />
                    <div className="g">
                      <b>
                        {label}
                        <span className={"pill sm " + (reauth ? "amber" : MCP_TONE[st] ?? "")}>{stLabel(s)}</span>
                      </b>
                      <span className="mkmeta">
                        {s.status?.version && <span className="mkver">{verLabel(s.status.version)}</span>}
                        {scopeLabel(s.scope)} · {t("ext.toolsN", { n: s.status?.tools ?? 0 })}
                        {(s.transport === "http" || (s.command ?? []).some((a) => /@latest$/.test(a))) && (
                          <span className="autoupd" title={s.transport === "http" ? t("aup.autoRemote") : t("aup.autoNpx")}>
                            {" · "}
                            {t("aup.auto")}
                          </span>
                        )}
                        {it?.name ? "" : (
                          <span className="mono" title={s.transport === "http" ? s.url : (s.command ?? []).join(" ")}>
                            {" · "}
                            {s.transport === "http" ? s.url : (s.command ?? []).join(" ")}
                          </span>
                        )}
                      </span>
                    </div>
                    {st === "login" && s.status.loginUrl && (
                      <a className="btn pri sm" href={s.status.loginUrl} target="_blank" rel="noreferrer noopener">
                        <LogIn size={12} /> {t("ext.mcpLogin")}
                      </a>
                    )}
                    {reauth && it?.auth === "device" && (
                      <button className="btn pri sm" type="button" onClick={() => onSignIn(it)}>
                        <LogIn size={12} /> {t("mk2.signInAgain")}
                      </button>
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
                        { label: t("mk2.scopeMenu"), disabled: s.source === "config", onClick: () => onScope(s) },
                        { label: t("mk2.pickAgents"), onClick: () => onAccess(s) },
                        ...(s.oauth ? [{ label: t("ext.mcpSignOut"), onClick: () => run(s.name, () => API.mcpSignOut(s.name), t("ext.mcpSignedOut", { name: label })) }] : []),
                        {
                          label: t("mk2.disconnect"),
                          danger: true,
                          disabled: s.source === "config",
                          onClick: async () => {
                            if (await confirmDialog(t("mk2.disconnectConfirm", { name: label }))) void run(s.name, () => API.deleteMcp(s.name), t("mk2.disconnected", { name: label }));
                          },
                        },
                      ]}
                    />
                  </div>
                  {s.status?.error && <div className="small errt mkerr">{s.status.error}</div>}
                  {s.enabled && (!s.scope || s.scope === "global") && (s.status?.tools ?? 0) >= MANY_TOOLS && (
                    <div className="small mknotice">
                      <span>{t("mk2.globalMany", { name: label, n: s.status.tools })}</span>
                      <button className="linkbtn" type="button" onClick={() => onAccess(s)}>
                        {t("mk2.pickAgents")}
                      </button>
                    </div>
                  )}
                  {open === s.name && (
                    <div className="mkdetail">
                      <McpTools name={s.name} server={s} onProbed={reload} />
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
          <h3>
            {t("mk.instSkills")} <span className="mkcount">{skills.length}</span>
          </h3>
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
                      <span title={t("mk.skillDir", { dir: s.dir })}>
                        {s.version && <span className="mkver">{verLabel(s.version)}</span>}
                        {(b && tx(b.desc)) || s.description}
                      </span>
                    </div>
                    <MoreMenu
                      items={[
                        {
                          label: removable ? t("mk.remove") : t("mk.skillManaged"),
                          danger: removable,
                          disabled: !removable,
                          onClick: async () => {
                            if (await confirmDialog(t("mk.removeSkill", { name: s.name }))) void run("skill:" + s.name, () => catalogAPI.removeSkill(s.name, s.agent), t("mk.removed", { name: s.name }));
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
          <h3>
            {t("mk.instPlugins")} <span className="mkcount">{plugins.length}</span>
          </h3>
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
                        <span className={"pill sm " + (p.state === "running" || p.state === "ACTIVE" ? "ok" : p.error ? "red" : !p.approved ? "amber" : "")}>{stateLabel(p)}</span>
                        {p.publisher && (
                          <span className="pubok" title={p.integrity}>
                            <BadgeCheck size={12} />
                          </span>
                        )}
                      </b>
                      <span>
                        <span className="mkver">{verLabel(p.version)}</span>
                        {p.description || p.source}
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
                          onClick: async () => {
                            if (await confirmDialog(t("ext.removeConfirm", { name: p.name })))
                              void run(p.name, async () => {
                                await API.deletePlugin(p.name);
                                void reloadMarket();
                              });
                          },
                        },
                      ]}
                    />
                  </div>
                  {p.error?.includes("SANDBOX_UNAVAILABLE") && p.approved ? (
                    // no sandbox works on this computer: a person may let it run without one
                    <div className="small mkerr mknosb">
                      <span className="errt" title={p.error}>
                        {t("ext.noSandbox")}
                      </span>
                      <button
                        className="btn sm dng"
                        type="button"
                        disabled={busy === p.name}
                        onClick={async () => {
                          if (await confirmDialog({ text: t("ext.allowUnsandboxedConfirm", { name: p.name }), ok: t("ext.allowUnsandboxed"), danger: true }))
                            void run(p.name, () => API.approvePlugin(p.name, { ...(p.grant?.permissions ?? p.requested), unsandboxed: true }, p.grant?.trustFutureVersions ?? false));
                        }}
                      >
                        {t("ext.allowUnsandboxed")}
                      </button>
                    </div>
                  ) : (
                    p.error && <div className="small errt mkerr">{p.error}</div>
                  )}
                </div>
              );
            })}
          </div>
        )}
        <p className="small mkhint">{t("mk3.useHero")}</p>
      </section>
      <PluginCards />
    </>
  );
}
