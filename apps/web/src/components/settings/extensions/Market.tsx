// 插件与技能 › 发现: the paste box, kinds, sections of cards and 热门技能库.

import { Skeleton } from "../../Skeleton";
import { Fragment, useState, type ReactNode } from "react";
import { AgentDocsDialog, AgentDocsIcon, useAgentDocs } from "../AgentDocs";
import { OpenArtifactDialog, OpenArtifactIcon, oaInstalled, useOpenArtifact } from "../OpenArtifact";
import { Check, ClipboardPaste, ExternalLink, LogIn } from "lucide-react";
import { API, openExternal, type MarketEntry } from "../../../api";
import { t } from "../../../i18n";
import { toast, useStore, refreshAgentDocs } from "../../../store";
import { errMsg, settle, signInWindow } from "../ExtParts";

// Brand icons from LobeHub Icons (@lobehub/icons-static-svg, MIT), as in
// ProviderMark, where the package has the mark; the others are the
// companies' own marks from their brand pages or sites (src/assets/brands).
// Anything else gets a letter tile.
import { confirmDialog } from "../../Confirm";
import { ConnectDialog } from "./Dialogs";
import { MoreMenu, type MenuItem } from "./Installed";
import { ItemIcon, catalogAPI, connectHow, matches, needsReauth, tx, verLabel, type CatItem, type Shared, type SkillLibrary } from "./shared";

// ---------------------------------------------------------------- marketplace

/** What a row's action area shows. */
export type RowState = "idle" | "busy" | "connected" | "added" | "login" | "reauth" | "error" | "connecting";

export interface Row {
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
export function badgeOf(it: CatItem): Row["badge"] {
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

export function MarketView({ d, q, reload, reloadMarket, onApprove, onSignIn, onManage, target, toAll, setToAll, onPaste, search }: Shared) {
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
export function Libraries({ libs, onOpen }: { libs: SkillLibrary[]; onOpen: (l: SkillLibrary) => void }) {
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
export function PasteHero({ onPaste }: { onPaste: (text: string) => void }) {
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

export function RowAction({ r }: { r: Row }) {
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

export function Section({ title, desc, rows }: { title: string; desc: string; rows: Row[] }) {
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
