// Pieces of 设置 › 插件与技能 shared by the marketplace and installed views
// (Extensions.tsx): plugin permission review, the hand-entry MCP form and
// an MCP server's tool list.
import { useEffect, useState } from "react";
import { Plus, RefreshCw, X } from "lucide-react";
import { API, type McpServer, type McpTool, type Permissions, type PluginView } from "../../api";
import { t, type Key } from "../../i18n";
import { toast, useStore } from "../../store";
import { catLabel } from "../Settings";
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

const PERM_KEYS: (keyof Permissions)[] = ["files", "network", "subprocess", "credentials", "services", "events"];

/** Every requested permission, plainly; nothing is labelled "safe". */
export function PermList({ p, need }: { p: Permissions; need?: string[] }) {
  const rows = PERM_KEYS.filter((k) => (k === "subprocess" ? p.subprocess : (p[k] as string[] | undefined)?.length));
  if (rows.length === 0) return <p className="small" style={{ margin: 0 }}>{t("ext.permNone")}</p>;
  return (
    <dl className="perms">
      {rows.map((k) => (
        <div key={k} className={need?.some((n) => n.startsWith(k)) ? "need" : ""}>
          <dt>{t(`ext.perm.${k}` as Key)}</dt>
          <dd>{k === "subprocess" ? t("ext.perm.subprocessYes") : (p[k] as string[]).map((v) => <code key={v}>{v}</code>)}</dd>
        </div>
      ))}
    </dl>
  );
}

export function stateLabel(p: PluginView): string {
  if (!p.approved) return t("ext.st.needsApproval");
  const k = `ext.st.${p.state}` as Key;
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
            {p.sandbox ? t("ext.runSandboxDesc") : t("ext.runProcessDesc")}
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

export function McpTools({ name, onProbed }: { name: string; onProbed: () => void }) {
  const [tools, setTools] = useState<McpTool[] | null>(null);
  const [probing, setProbing] = useState(false);
  const [msg, setMsg] = useState<string | null>(null);
  useEffect(() => {
    API.mcpTools(name).then((r) => setTools(r.tools ?? []), () => setTools([]));
  }, [name]);
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
      </div>
      {tools === null ? (
        <p className="small">{t("common.loading")}</p>
      ) : tools.length === 0 ? (
        <p className="small">{t("ext.noTools")}</p>
      ) : (
        tools.map((x) => (
          <div className="runrow" key={x.name}>
            <code>{x.original}</code>
            <span className="g small">{x.description}</span>
            <span className={"pill " + (x.category === "read" ? "" : "amber")}>{catLabel(x.category)}</span>
          </div>
        ))
      )}
    </div>
  );
}

export function McpForm({ onCancel, onSaved }: { onCancel: () => void; onSaved: (s: McpServer) => void }) {
  const agents = useStore((s) => s.agents);
  const teams = useStore((s) => s.teams);
  const [name, setName] = useState("");
  const [transport, setTransport] = useState<"stdio" | "http">("stdio");
  const [command, setCommand] = useState("");
  const [url, setUrl] = useState("");
  const [env, setEnv] = useState<[string, string][]>([["", ""]]);
  const [scope, setScope] = useState("global");
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
              <input className="fi sm mono" value={v} placeholder="keychain:crewbase/github" onChange={(e) => setEnv(env.map((x, j) => (j === i ? [x[0], e.target.value] : x)))} />
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
        <label className="fl">{t("ext.scope")}</label>
        <span className="ssel">
          <select value={scope} onChange={(e) => setScope(e.target.value)}>
            <option value="global">{t("ext.scopeGlobal")}</option>
            {teams.length > 1 &&
              teams.map((tm) => (
                <option key={"team:" + tm.id} value={"team:" + tm.id}>
                  {t("ext.scopeTeam", { name: tm.name })}
                </option>
              ))}
            {Object.values(agents).filter((a) => a.kind !== "chat").map((a) => (
              <option key={a.id} value={a.id}>
                {t("ext.scopeAgent", { name: a.name })}
              </option>
            ))}
          </select>
        </span>
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
