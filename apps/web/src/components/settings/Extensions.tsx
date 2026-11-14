import { useEffect, useState } from "react";
import { Download, FolderInput, Plug, Plus, RefreshCw, Server, ShieldAlert, Trash, X, LogIn } from "lucide-react";
import { API, type MarketEntry, type McpServer, type McpTool, type Permissions, type PluginView } from "../../api";
import { t, type Key } from "../../i18n";
import { ago } from "../../format";
import { toast, useStore, setState } from "../../store";
import { Head } from "../Settings";
import { catLabel } from "../Settings";
import { PanelView, SlotIcon, useSlot } from "../UISlots";

type Tab = "plugins" | "mcp";

export function Extensions() {
  const want = useStore((s) => s.settingsTab);
  const [tab, setTab] = useState<Tab>(want === "mcp" ? "mcp" : "plugins");
  useEffect(() => {
    if (want === "mcp" || want === "plugins") {
      setTab(want);
      setState({ settingsTab: null });
    }
  }, [want]);
  return (
    <>
      <Head
        title={t("set.market")}
        sub={t("ext.host")}
        right={
          <div className="seg">
            <button type="button" aria-pressed={tab === "plugins"} onClick={() => setTab("plugins")}>
              {t("ext.plugins")}
            </button>
            <button type="button" aria-pressed={tab === "mcp"} onClick={() => setTab("mcp")}>
              {t("ext.mcp")}
            </button>
          </div>
        }
      />
      <div className="vb">
        {tab === "plugins" ? (
          <>
            <PluginCards />
            <Plugins />
          </>
        ) : (
          <Mcp />
        )}
      </div>
    </>
  );
}

/** settings.card contributions from enabled plugins. */
function PluginCards() {
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

function errMsg(e: any) {
  return e?.message ?? String(e);
}

// ---------------------------------------------------------------- plugins

const PERM_KEYS: (keyof Permissions)[] = ["files", "network", "subprocess", "credentials", "services", "events"];

/** Every requested permission, plainly; nothing is labelled "safe". */
function PermList({ p, need }: { p: Permissions; need?: string[] }) {
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

function Plugins() {
  const [installed, setInstalled] = useState<PluginView[] | null>(null);
  const [market, setMarket] = useState<MarketEntry[] | null>(null);
  const [marketErr, setMarketErr] = useState<string | null>(null);
  const [path, setPath] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const [approve, setApprove] = useState<PluginView | null>(null);
  const load = () => API.plugins().then((r) => setInstalled(r.plugins ?? []), () => setInstalled([]));
  const loadMarket = () =>
    API.market().then(
      (r) => {
        setMarket(r.plugins ?? []);
        setMarketErr(null);
      },
      (e) => setMarketErr(errMsg(e)),
    );
  useEffect(() => {
    void load();
    void loadMarket();
  }, []);

  const install = async (b: { source?: string; market?: string }, label: string) => {
    setBusy(label);
    try {
      const v = await API.install(b);
      toast(t("ext.installed", { name: v.name }));
      await load();
      void loadMarket();
      if (!v.approved) setApprove(v);
      setPath("");
    } catch (e) {
      toast(t("ext.installFailed", { error: errMsg(e) }));
    } finally {
      setBusy(null);
    }
  };
  const act = async (p: PluginView, a: "enable" | "disable" | "reload") => {
    setBusy(p.name);
    try {
      await API.pluginAction(p.name, a);
      void load();
    } catch (e) {
      toast(errMsg(e));
    } finally {
      setBusy(null);
    }
  };
  const remove = async (p: PluginView) => {
    if (!window.confirm(t("ext.removeConfirm", { name: p.name }))) return;
    try {
      await API.deletePlugin(p.name);
      void load();
      void loadMarket();
    } catch (e) {
      toast(errMsg(e));
    }
  };

  return (
    <>
      <div className="vsec">{t("ext.installed")}</div>
      {installed === null ? (
        <p className="small">{t("common.loading")}</p>
      ) : installed.length === 0 ? (
        <p className="small" style={{ margin: 0 }}>
          {t("ext.noneInstalled")}
        </p>
      ) : (
        <div className="cards">
          {installed.map((p) => (
            <div className={"rcard" + (p.enabled ? "" : " off")} key={p.name}>
              <div className="rch">
                <span className="wfi">
                  <Plug size={15} />
                </span>
                <div className="g">
                  <b>
                    {p.name} <span className="small">v{p.version}</span>
                  </b>
                  <span className="small">
                    {p.sourceKind} · {t("ext.installedAgo", { time: ago(p.installedAt) })}
                    {p.author ? ` · ${p.author}` : ""}
                  </span>
                </div>
                <button
                  className="tg"
                  type="button"
                  role="switch"
                  aria-checked={p.enabled}
                  aria-label={t("auto.enabled")}
                  disabled={busy === p.name}
                  onClick={() => act(p, p.enabled ? "disable" : "enable")}
                />
              </div>
              {p.description && <p className="rprompt">{p.description}</p>}
              <div className="rmeta small">
                <span className={"pill " + (p.state === "running" ? "ok" : p.error ? "red" : !p.approved ? "amber" : "")}>{stateLabel(p)}</span>
                <span>{p.sandbox ? t("ext.runSandbox") : t("ext.runProcess")}</span>
                {p.process && <span>{t("ext.enforcement", { e: p.process.enforcement || "none" })}</span>}
                {p.process?.tools?.length ? <span>{t("ext.toolsN", { n: p.process.tools.length })}</span> : null}
              </div>
              {p.error && <div className="small errt">{p.error}</div>}
              <div className="acts sm">
                {!p.approved || (p.needsApproval?.length ?? 0) > 0 ? (
                  <button className="btn warn" type="button" onClick={() => setApprove(p)}>
                    <ShieldAlert size={12} />
                    {t("ext.reviewPerms")}
                  </button>
                ) : (
                  <button className="btn ghost" type="button" onClick={() => setApprove(p)}>
                    {t("ext.viewPerms")}
                  </button>
                )}
                <button className="btn ghost" type="button" disabled={busy === p.name || !p.enabled} onClick={() => act(p, "reload")}>
                  <RefreshCw size={12} />
                  {t("ext.reload")}
                </button>
                <span className="sp" />
                <button className="ix" type="button" aria-label={t("common.delete")} title={t("common.delete")} onClick={() => remove(p)}>
                  <Trash size={13} />
                </button>
              </div>
            </div>
          ))}
        </div>
      )}

      <div className="vsec">{t("ext.fromPath")}</div>
      <form
        className="runbar"
        onSubmit={(e) => {
          e.preventDefault();
          if (path.trim()) void install({ source: path.trim() }, "path");
        }}
      >
        <input className="fi sm mono" value={path} placeholder={t("ext.pathPh")} onChange={(e) => setPath(e.target.value)} />
        <button className="btn" type="submit" disabled={!path.trim() || busy === "path"}>
          <FolderInput size={13} />
          {t("ext.install")}
        </button>
      </form>
      <p className="small">{t("ext.pathHint")}</p>

      <div className="vsec">{t("ext.market")}</div>
      {marketErr ? (
        <p className="small">{t("ext.marketErr", { error: marketErr })}</p>
      ) : market === null ? (
        <p className="small">{t("common.loading")}</p>
      ) : market.length === 0 ? (
        <p className="small">{t("ext.marketEmpty")}</p>
      ) : (
        <div className="cards">
          {market.map((m) => (
            <div className="rcard" key={m.name}>
              <div className="rch">
                <span className="wfi">
                  <Plug size={15} />
                </span>
                <div className="g">
                  <b>
                    {m.name} <span className="small">v{m.version}</span>
                  </b>
                  <span className="small">{m.author ?? m.source}</span>
                </div>
                {m.installed ? (
                  <span className="pill">{t("ext.installedV", { v: m.installed })}</span>
                ) : (
                  <button className="btn" type="button" disabled={busy === m.name} onClick={() => install({ market: m.name }, m.name)}>
                    <Download size={12} />
                    {t("ext.install")}
                  </button>
                )}
              </div>
              <p className="rprompt">{m.description}</p>
              <div className="small" style={{ marginBottom: 4 }}>
                {t("ext.requests")}
              </div>
              <PermList p={m.permissions ?? {}} />
            </div>
          ))}
        </div>
      )}
      {approve && (
        <ApproveDialog
          p={approve}
          onClose={() => setApprove(null)}
          onDone={() => {
            setApprove(null);
            void load();
          }}
        />
      )}
    </>
  );
}

function stateLabel(p: PluginView): string {
  if (!p.approved) return t("ext.st.needsApproval");
  const k = `ext.st.${p.state}` as Key;
  return t(k) === k ? p.state || "—" : t(k);
}

function ApproveDialog({ p, onClose, onDone }: { p: PluginView; onClose: () => void; onDone: () => void }) {
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

const MCP_TONE: Record<string, string> = { ready: "ok", connecting: "blue", error: "red", disabled: "", login: "amber" };

function Mcp() {
  const tick = useStore((s) => s.mcpTick);
  const agents = useStore((s) => s.agents);
  const [list, setList] = useState<McpServer[] | null>(null);
  const [adding, setAdding] = useState(false);
  const [open, setOpen] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const load = () =>
    API.mcp().then(
      (r) => setList(r.servers ?? []),
      (e) => setErr(errMsg(e)),
    );
  useEffect(() => {
    void load();
  }, [tick]);

  const toggle = async (s: McpServer) => {
    try {
      await API.patchMcp(s.name, { enabled: !s.enabled });
      void load();
    } catch (e) {
      toast(errMsg(e));
    }
  };
  const remove = async (s: McpServer) => {
    if (!window.confirm(t("ext.removeConfirm", { name: s.name }))) return;
    try {
      await API.deleteMcp(s.name);
      void load();
    } catch (e) {
      toast(errMsg(e));
    }
  };

  return (
    <>
      <div className="vsec row">
        <span>{t("ext.mcpServers")}</span>
        <span className="sp" />
        {!adding && (
          <button className="btn" type="button" onClick={() => setAdding(true)}>
            <Plus size={13} />
            {t("ext.addMcp")}
          </button>
        )}
      </div>
      <p className="small" style={{ margin: "0 0 10px" }}>
        {t("ext.mcpDesc")}
      </p>
      {adding && (
        <McpForm
          onCancel={() => setAdding(false)}
          onSaved={(s) => {
            setAdding(false);
            setOpen(s.name);
            void load();
          }}
        />
      )}
      {err && <p className="small errt">{err}</p>}
      {list?.length === 0 && !adding && <div className="empty">{t("ext.noMcp")}</div>}
      <div className="cards">
        {list?.map((s) => (
          <div className={"rcard" + (s.enabled ? "" : " off")} key={s.name}>
            <div className="rch">
              <span className="wfi">
                <Server size={15} />
              </span>
              <div className="g">
                <b>{s.name}</b>
                <span className="small mono ell">{s.transport === "http" ? s.url : (s.command ?? []).join(" ")}</span>
              </div>
              <button className="tg" type="button" role="switch" aria-checked={s.enabled} aria-label={t("auto.enabled")} disabled={s.source === "config"} onClick={() => toggle(s)} />
            </div>
            <div className="rmeta small">
              <span className={"pill " + (MCP_TONE[s.status?.state] ?? "")}>{t(`ext.mcpSt.${s.status?.state}` as Key) === `ext.mcpSt.${s.status?.state}` ? s.status?.state : t(`ext.mcpSt.${s.status?.state}` as Key)}</span>
              <span>{s.transport}</span>
              <span>{s.scope === "global" || !s.scope ? t("ext.scopeGlobal") : t("ext.scopeAgent", { name: agents[s.scope]?.name ?? s.scope })}</span>
              {s.transport === "stdio" && <span>{s.trust === "host" ? t("ext.trustHost") : t("ext.trustSandbox")}</span>}
              {s.status?.enforcement && <span>{t("ext.enforcement", { e: s.status.enforcement })}</span>}
              <span>{t("ext.toolsN", { n: s.status?.tools ?? 0 })}</span>
              {s.source === "config" && <span>{t("ext.fromConfig")}</span>}
            </div>
            {s.status?.error && <div className="small errt">{s.status.error}</div>}
            {s.status?.state === "login" && s.status.loginUrl && (
              <div className="mcplogin">
                <span className="small">{t("ext.mcpLoginNeed")}</span>
                <a className="btn pri sm" href={s.status.loginUrl} target="_blank" rel="noreferrer noopener">
                  <LogIn size={13} /> {t("ext.mcpLogin")}
                </a>
              </div>
            )}
            {s.env && Object.keys(s.env).length > 0 && (
              <div className="kvs small">
                {Object.entries(s.env).map(([k, v]) => (
                  <span key={k} className="mono">
                    {k}={v}
                  </span>
                ))}
              </div>
            )}
            <div className="acts sm">
              <button className="btn ghost" type="button" aria-expanded={open === s.name} onClick={() => setOpen(open === s.name ? null : s.name)}>
                {t("ext.tools")}
              </button>
              <span className="sp" />
              {s.oauth && s.status?.state === "ready" && (
                <button
                  className="btn ghost"
                  type="button"
                  onClick={async () => {
                    await API.mcpSignOut(s.name).catch(() => {});
                    toast(t("ext.mcpSignedOut", { name: s.name }));
                  }}
                >
                  {t("ext.mcpSignOut")}
                </button>
              )}
              {s.source !== "config" && (
                <button className="ix" type="button" aria-label={t("common.delete")} title={t("common.delete")} onClick={() => remove(s)}>
                  <Trash size={13} />
                </button>
              )}
            </div>
            {open === s.name && <McpTools name={s.name} onProbed={load} />}
          </div>
        ))}
      </div>
    </>
  );
}

function McpTools({ name, onProbed }: { name: string; onProbed: () => void }) {
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

function McpForm({ onCancel, onSaved }: { onCancel: () => void; onSaved: (s: McpServer) => void }) {
  const agents = useStore((s) => s.agents);
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
            {Object.values(agents).map((a) => (
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
