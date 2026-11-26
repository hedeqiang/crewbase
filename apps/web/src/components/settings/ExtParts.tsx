// Pieces of 设置 › 插件与技能 shared by the marketplace and installed views
// (Extensions.tsx): plugin permission review, the hand-entry MCP form and
// an MCP server's tool list.
import { Skeleton } from "../Skeleton";
import { useEffect, useState } from "react";
import { Plus, RefreshCw, X } from "lucide-react";
import { API, desktop, openExternal, type McpServer, type McpSkill, type McpTool, type McpToolFilter, type Permissions, type PluginView } from "../../api";
import { t, type Key } from "../../i18n";
import { toast } from "../../store";
import { catLabel } from "./labels";
import { PanelView, SlotIcon, useSlot } from "../UISlots";

/** settings.card contributions from enabled plugins. */
export function PluginCards() {
  const cards = useSlot("settings.card");
  if (!cards.length) return null;
  return (
    <>
      <div className="vsec">{t("ui.cards")}</div>
      <div className="uicards">
        {cards.map((c) => (
          <div key={c.key} className="sbox uisc">
            <div className="uisch">
              <SlotIcon name={c.icon} size={16} />
              <b>{c.title}</b>
              <span className="uiown">{c.owner}</span>
            </div>
            {c.desc && <p className="small">{c.desc}</p>}
            <PanelView c={c} />
          </div>
        ))}
      </div>
    </>
  );
}

export function errMsg(e: any) {
  return e?.message ?? String(e);
}

// ---------------------------------------------------------------- plugins

const PERM_KEYS: (keyof Permissions)[] = ["unsandboxed", "files", "network", "subprocess", "credentials", "services", "events"];
const FLAGS = new Set<keyof Permissions>(["subprocess", "unsandboxed"]);

/** Every requested permission, plainly; nothing is labelled "safe". */
export function PermList({ p, need }: { p: Permissions; need?: string[] }) {
  const rows = PERM_KEYS.filter((k) => (FLAGS.has(k) ? !!p[k] : (p[k] as string[] | undefined)?.length));
  if (rows.length === 0) return <p className="small" style={{ margin: 0 }}>{t("ext.permNone")}</p>;
  return (
    <dl className="perms">
      {rows.map((k) => (
        <div key={k} className={(need?.some((n) => n.startsWith(k)) ? "need" : "") + (k === "unsandboxed" ? " danger" : "")}>
          <dt>{t(`ext.perm.${k}` as Key)}</dt>
          <dd>{FLAGS.has(k) ? t(`ext.perm.${k}Yes` as Key) : (p[k] as string[]).map((v) => <code key={v}>{v}</code>)}</dd>
        </div>
      ))}
    </dl>
  );
}

export function stateLabel(p: PluginView): string {
  if (!p.approved) return t("ext.st.needsApproval");
  // the kernel's plugin states (ACTIVE, FAILED…) and the process's (running…)
  const k = `ext.st.${(p.state || "").toLowerCase()}` as Key;
  return t(k) === k ? p.state || "—" : t(k);
}

export function ApproveDialog({ p, onClose, onDone }: { p: PluginView; onClose: () => void; onDone: () => void }) {
  const [trust, setTrust] = useState(p.grant?.trustFutureVersions ?? false);
  const [busy, setBusy] = useState(false);
  const pending = !p.approved || (p.needsApproval?.length ?? 0) > 0;
  const ok = async () => {
    setBusy(true);
    try {
      await API.approvePlugin(p.name, p.requested, trust);
      toast(t("ext.approved", { name: p.name }));
      onDone();
    } catch (e) {
      toast(errMsg(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <div className="scrim" onClick={onClose} />
      <div className="modal" role="dialog" aria-modal="true" aria-labelledby="apTitle">
        <div className="mhd">
          <h3 id="apTitle">{pending ? t("ext.approveTitle", { name: p.name }) : t("ext.permsTitle", { name: p.name })}</h3>
          <button className="ix" type="button" aria-label={t("common.close")} onClick={onClose}>
            <X size={14} />
          </button>
        </div>
        <div className="mbd">
          <p className="small" style={{ margin: "0 0 10px" }}>
            v{p.version} · {p.source}
          </p>
          <div className="fl">{t("ext.requests")}</div>
          <PermList p={p.requested ?? {}} need={p.needsApproval} />
          <div className="fl">{t("ext.howRuns")}</div>
          <p className="small" style={{ margin: 0 }}>
            {p.sandbox && !p.grant?.permissions.unsandboxed ? t("ext.runSandboxDesc") : t("ext.runProcessDesc")}
            {p.process?.enforcement ? " " + t("ext.enforcement", { e: p.process.enforcement }) : ""}
          </p>
          {(p.process?.limits?.length ?? 0) > 0 && (
            <ul className="small limits">
              {p.process!.limits!.map((l) => (
                <li key={l}>{l}</li>
              ))}
            </ul>
          )}
          {pending && (
            <label className="ckrow">
              <input type="checkbox" checked={trust} onChange={(e) => setTrust(e.target.checked)} />
              <span>{t("ext.trustFuture")}</span>
            </label>
          )}
          <p className="small warnline" style={{ marginTop: 10 }}>
            {t("ext.approveNote")}
          </p>
          <div className="mft">
            <button className="btn ghost" type="button" onClick={onClose}>
              {pending ? t("common.cancel") : t("common.close")}
            </button>
            {pending && (
              <button className="btn pri" type="button" disabled={busy} onClick={ok}>
                {t("ext.approve")}
              </button>
            )}
          </div>
        </div>
      </div>
    </>
  );
}

// ---------------------------------------------------------------- MCP

export const MCP_TONE: Record<string, string> = { ready: "ok", connecting: "blue", error: "red", disabled: "", login: "amber" };

/** The filter after switching tool x on or off: off adds it to exclude;
 *  on drops it from exclude (and adds it to include when an include list
 *  would still keep it out). */
export function toggleTool(f: McpToolFilter | null | undefined, x: McpTool, on: boolean): McpToolFilter {
  let include = [...(f?.include ?? [])];
  let exclude = [...(f?.exclude ?? [])];
  if (!on) exclude = [...new Set([...exclude, x.original])];
  else {
    exclude = exclude.filter((p) => p !== x.original && p !== x.name);
    if (include.length && !include.includes(x.original)) include = [...include, x.original];
  }
  return { include, exclude };
}

/** A server's tools as a checklist: switching one off keeps it from being
 *  registered (sent to models). server unset = read-only list. */
export function McpTools({ name, server, onProbed }: { name: string; server?: McpServer; onProbed: () => void }) {
  const [tools, setTools] = useState<McpTool[] | null>(null);
  const [probing, setProbing] = useState(false);
  const [msg, setMsg] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const filter = server?.tools;
  const load = () => API.mcpTools(name).then((r) => setTools(r.tools ?? []), () => setTools([]));
  useEffect(() => {
    void load();
  }, [name, JSON.stringify(filter ?? null), server?.status?.tools]);
  const toggle = async (x: McpTool, on: boolean) => {
    setBusy(true);
    try {
      await API.patchMcp(name, { tools: toggleTool(filter, x, on) });
      setTools((ts) => ts?.map((y) => (y.name === x.name ? { ...y, enabled: on } : y)) ?? ts);
      onProbed();
    } catch (e) {
      toast(errMsg(e));
    } finally {
      setBusy(false);
    }
  };
  const off = tools?.filter((x) => x.enabled === false).length ?? 0;
  const probe = async () => {
    setProbing(true);
    try {
      const r = await API.probeMcp(name);
      if (r.ok) {
        setTools(r.tools ?? []);
        setMsg(t("ext.probeOk", { n: r.tools?.length ?? 0 }));
      } else setMsg(t("ext.probeFail", { error: r.error ?? "" }));
      onProbed();
    } catch (e) {
      setMsg(errMsg(e));
    } finally {
      setProbing(false);
    }
  };
  return (
    <div className="runs">
      <div className="acts sm" style={{ marginTop: 0 }}>
        <button className="btn" type="button" disabled={probing} onClick={probe}>
          {probing ? <span className="spin sm" /> : <RefreshCw size={12} />}
          {t("ext.probe")}
        </button>
        {msg && <span className="small">{msg}</span>}
        {server && tools && tools.length > 0 && <span className="small">{t("ext.toolsOnOff", { on: tools.length - off, n: tools.length })}</span>}
      </div>
      {tools === null ? (
        <Skeleton />
      ) : tools.length === 0 ? (
        <p className="small">{t("ext.noTools")}</p>
      ) : (
        tools.map((x) => (
          <div className={"runrow" + (x.enabled === false ? " off" : "")} key={x.name}>
            <code>{x.original}</code>
            <span className="g small" title={x.description}>
              {x.description}
            </span>
            {x.large && (
              <span className="pill amber sm" title={t("ext.toolLargeTip", { n: Math.round((x.schemaSize ?? 0) / 1000) })}>
                {t("ext.toolLarge")}
              </span>
            )}
            <span className={"pill " + (x.category === "read" ? "" : "amber")}>{catLabel(x.category)}</span>
            {server && (
              <button
                className="tg"
                type="button"
                role="switch"
                disabled={busy}
                aria-checked={x.enabled !== false}
                aria-label={t("ext.toolSwitch", { name: x.original })}
                onClick={() => toggle(x, x.enabled === false)}
              />
            )}
          </div>
        ))
      )}
    </div>
  );
}

/** The skills a server publishes (MCP skills extension): what it ships,
 *  listed by name; their content is only fetched when an agent loads one.
 *  localNames: this computer's own skills, to flag same-named ones. */
export function McpSkills({ name, count, localNames }: { name: string; count?: number; localNames: Set<string> }) {
  const [skills, setSkills] = useState<McpSkill[] | null>(null);
  const [err, setErr] = useState("");
  useEffect(() => {
    API.mcpSkills(name).then(
      (r) => {
        setSkills(r.skills ?? []);
        setErr(r.error ?? "");
      },
      (e) => {
        setSkills([]);
        setErr(errMsg(e));
      },
    );
  }, [name, count]);
  return (
    <div className="runs">
      <div className="acts sm" style={{ marginTop: 0 }}>
        <b className="small">{t("ext.mcpSkills")}</b>
        {skills && skills.length > 0 && <span className="small">{t("ext.skillsN", { n: skills.length })}</span>}
      </div>
      <p className="small">{t("ext.mcpSkillsHint")}</p>
      {err && <p className="small errt">{t("ext.mcpSkillsErr", { error: err })}</p>}
      {skills === null ? (
        <Skeleton />
      ) : skills.length === 0 ? (
        <p className="small">{t("ext.mcpSkillsNone")}</p>
      ) : (
        skills.map((x) => (
          <div className={"runrow" + (x.invalid ? " off" : "")} key={x.uri} title={x.uri}>
            <code>{x.name}</code>
            <span className="g small" title={x.invalid ? t("ext.mcpSkillInvalid", { why: x.invalid }) : x.description}>
              {x.invalid ? t("ext.mcpSkillInvalid", { why: x.invalid }) : x.description}
            </span>
            {(x.collides || localNames.has(x.name)) && (
              <span className="pill amber sm" title={t("ext.mcpSkillSameNameTip")}>
                {t("ext.mcpSkillSameName")}
              </span>
            )}
            {x.dynamic ? (
              <span className="pill amber sm" title={t("ext.mcpSkillDynamicTip")}>
                {t("ext.mcpSkillDynamic")}
              </span>
            ) : (
              !x.invalid && <span className="pill sm">{t("ext.mcpSkillFiles", { n: x.files })}</span>
            )}
          </div>
        ))
      )}
    </div>
  );
}

export function McpForm({ onCancel, onSaved }: { onCancel: () => void; onSaved: (s: McpServer) => void }) {
  const [name, setName] = useState("");
  const [transport, setTransport] = useState<"stdio" | "http">("stdio");
  const [command, setCommand] = useState("");
  const [url, setUrl] = useState("");
  const [env, setEnv] = useState<[string, string][]>([["", ""]]);
  const scope = "global"; // per agent: the connector switches in each agent's settings
  const [trust, setTrust] = useState("sandboxed");
  const [oauth, setOauth] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const save = async () => {
    setBusy(true);
    setErr(null);
    const kv = Object.fromEntries(env.filter(([k]) => k.trim()).map(([k, v]) => [k.trim(), v]));
    const body: Partial<McpServer> =
      transport === "stdio"
        ? { name: name.trim(), transport, command: splitCommand(command), env: kv, scope, enabled: true, trust }
        : { name: name.trim(), transport, url: url.trim(), headers: kv, scope, enabled: true, ...(oauth ? { oauth: {} } : {}) };
    try {
      const s = await API.addMcp(body);
      toast(t("ext.mcpAdded", { name: s.name }));
      onSaved(s);
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="box form">
      <b>{t("ext.addMcp")}</b>
      <div className="fgrid">
        <label className="fl">{t("ext.mcpName")}</label>
        <input className="fi sm mono" value={name} placeholder="github" onChange={(e) => setName(e.target.value.toLowerCase())} />
        <label className="fl">{t("ext.transport")}</label>
        <div className="seg">
          <button type="button" aria-pressed={transport === "stdio"} onClick={() => setTransport("stdio")}>
            stdio
          </button>
          <button type="button" aria-pressed={transport === "http"} onClick={() => setTransport("http")}>
            HTTP
          </button>
        </div>
        {transport === "stdio" ? (
          <>
            <label className="fl">{t("ext.command")}</label>
            <input className="fi sm mono" value={command} placeholder="npx -y @modelcontextprotocol/server-github" onChange={(e) => setCommand(e.target.value)} />
            <label className="fl">{t("ext.trust")}</label>
            <div>
              <div className="seg">
                <button type="button" aria-pressed={trust === "sandboxed"} onClick={() => setTrust("sandboxed")}>
                  {t("ext.trustSandbox")}
                </button>
                <button type="button" aria-pressed={trust === "host"} onClick={() => setTrust("host")}>
                  {t("ext.trustHost")}
                </button>
              </div>
              {trust === "host" && <div className="small warnline" style={{ marginTop: 4 }}>{t("ext.trustHostWarn")}</div>}
            </div>
          </>
        ) : (
          <>
            <label className="fl">URL</label>
            <input className="fi sm mono" value={url} placeholder="https://mcp.example.com/mcp" onChange={(e) => setUrl(e.target.value)} />
            <label className="fl">{t("ext.mcpOAuth")}</label>
            <label className="chk">
              <input type="checkbox" checked={oauth} onChange={(e) => setOauth(e.target.checked)} />
              <span className="small">{t("ext.mcpOAuthDesc")}</span>
            </label>
          </>
        )}
        <label className="fl">{transport === "stdio" ? t("ext.env") : t("ext.headers")}</label>
        <div>
          {env.map(([k, v], i) => (
            <div className="envrow" key={i}>
              <input className="fi sm mono" value={k} placeholder={transport === "stdio" ? "GITHUB_TOKEN" : "Authorization"} onChange={(e) => setEnv(env.map((x, j) => (j === i ? [e.target.value, x[1]] : x)))} />
              <input className="fi sm mono" value={v} placeholder="vault:crewbase/github" onChange={(e) => setEnv(env.map((x, j) => (j === i ? [x[0], e.target.value] : x)))} />
              <button className="ix" type="button" aria-label={t("panel.remove")} onClick={() => setEnv(env.length > 1 ? env.filter((_, j) => j !== i) : [["", ""]])}>
                <X size={13} />
              </button>
            </div>
          ))}
          <button className="btn ghost sm" type="button" onClick={() => setEnv([...env, ["", ""]])}>
            <Plus size={12} />
            {t("ext.addVar")}
          </button>
          <div className="small">{t("ext.credHint")}</div>
        </div>
      </div>
      {err && <div className="ferr">{err}</div>}
      <div className="acts">
        <button className="btn pri" type="button" disabled={busy || !name.trim() || (transport === "stdio" ? !command.trim() : !url.trim())} onClick={save}>
          {t("ext.addAndConnect")}
        </button>
        <button className="btn ghost" type="button" onClick={onCancel}>
          {t("common.cancel")}
        </button>
      </div>
    </div>
  );
}

/** Shell-like split honouring "double" and 'single' quotes. */
function splitCommand(s: string): string[] {
  const out: string[] = [];
  const re = /"([^"]*)"|'([^']*)'|(\S+)/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(s))) out.push(m[1] ?? m[2] ?? m[3]);
  return out;
}

/** Polls the server list until name settles (ready, login, error). */
export async function settle(name: string, ms = 25000): Promise<McpServer | undefined> {
  const end = Date.now() + ms;
  let last: McpServer | undefined;
  while (Date.now() < end) {
    try {
      last = (await API.mcp()).servers?.find((s) => s.name === name);
    } catch {
      /* try again */
    }
    const st = last?.status?.state;
    if (st === "ready" || st === "error" || st === "disabled" || (st === "login" && last?.status.loginUrl)) return last;
    await sleep(500);
  }
  return last;
}

/** Opens a sign-in page outside the app. In a browser the window must be
 *  opened during the click (popup blockers), so it is opened at once and
 *  pointed at the URL when the server publishes it. The result returns
 *  false when nothing could be opened (blocked). */
export function signInWindow(): (url: string | null) => boolean {
  if (desktop()?.openExternal) return (u) => (u ? (openExternal(u), true) : true);
  const w = window.open("", "_blank");
  if (w) {
    try {
      w.opener = null;
      w.document.title = t("mk2.opening");
      w.document.body.style.cssText = "font: 14px system-ui, sans-serif; color: #666; display: grid; place-items: center; height: 100vh; margin: 0";
      w.document.body.textContent = t("mk2.opening");
    } catch {
      /* cross-origin already */
    }
  }
  return (u) => {
    if (!w || w.closed) return false;
    if (u) w.location.href = u;
    else w.close();
    return true;
  };
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
