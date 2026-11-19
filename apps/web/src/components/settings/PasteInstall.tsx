import { useEffect, useState } from "react";
import { Lock, X } from "lucide-react";
import { MonoMark } from "../ProviderMark";
import { API, api, type PluginView } from "../../api";
import { getLang, t } from "../../i18n";
import { toast } from "../../store";
import { errMsg, settle, signInWindow } from "./ExtParts";

type Text = { zh: string; en: string };
interface PlanVar {
  key: string;
  value: string;
  secret?: boolean;
  missing?: boolean;
}
interface PlanServer {
  name: string;
  transport: "stdio" | "http";
  command?: string[];
  url?: string;
  oauth?: boolean;
  env?: PlanVar[];
  headers?: PlanVar[];
  exists?: boolean;
}
interface PlanSkill {
  name: string;
  description?: string;
  path: string;
  exists?: boolean;
}
interface Plan {
  kind: "mcp" | "skill" | "plugin";
  source: string;
  servers?: PlanServer[];
  skills?: PlanSkill[];
  plugin?: string;
  note?: Text;
}

const detect = (input: string) => api<Plan>("POST", "/api/install/detect", { input });
const apply = (b: { kind: string; source: string; servers?: PlanServer[]; skills?: string[]; scope?: string }) =>
  api<{ servers?: { name: string }[]; skills?: { name: string }[] }>("POST", "/api/install/apply", b);

const tx = (x?: Text) => (x ? (getLang() === "en" ? x.en : x.zh) || x.zh || x.en : "");

/** 粘贴安装: paste an MCP config, a command, a server URL, a GitHub link,
 *  a folder or an npm package; see what it installs; install it. */
export function PasteInstall({ initial = "", onClose, onDone, onApprove }: { initial?: string; onClose: () => void; onDone: () => void; onApprove: (p: PluginView) => void }) {
  const [input, setInput] = useState(initial);
  const [plan, setPlan] = useState<Plan | null>(null);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [picked, setPicked] = useState<Set<string>>(new Set());

  const run = async (text = input) => {
    if (!text.trim()) return;
    setBusy(true);
    setErr("");
    setPlan(null);
    try {
      const p = await detect(text);
      setPlan(p);
      setPicked(new Set((p.skills ?? []).filter((s) => !s.exists).map((s) => s.path)));
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  };
  // pasted on the page: recognise it straight away
  useEffect(() => {
    if (initial.trim()) void run(initial);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  const editServer = (i: number, f: (s: PlanServer) => PlanServer) => setPlan((p) => (p ? { ...p, servers: p.servers!.map((s, j) => (j === i ? f(s) : s)) } : p));
  const editVar = (i: number, field: "env" | "headers", k: number, value: string) =>
    editServer(i, (s) => ({ ...s, [field]: (s[field] ?? []).map((v, j) => (j === k ? { ...v, value } : v)) }));

  const missing =
    plan?.kind === "mcp" && (plan.servers ?? []).some((s) => !s.name.trim() || [...(s.env ?? []), ...(s.headers ?? [])].some((v) => !v.value.trim()));
  const missingN =
    plan?.kind === "mcp" ? (plan.servers ?? []).reduce((a, x) => a + [...(x.env ?? []), ...(x.headers ?? [])].filter((v) => !v.value.trim()).length, 0) : 0;
  const ready = !!plan && !busy && (plan.kind === "plugin" || (plan.kind === "skill" ? picked.size > 0 : !missing));

  const install = async () => {
    if (!plan) return;
    setBusy(true);
    setErr("");
    try {
      if (plan.kind === "plugin") {
        const v = await API.install({ source: plan.plugin! });
        toast(t("mk.added", { name: v.name }));
        onDone();
        if (!v.approved) onApprove(v);
        return onClose();
      }
      if (plan.kind === "skill") {
        const r = await apply({ kind: "skill", source: plan.source, skills: [...picked] });
        toast(t("pst.skillsAdded", { names: (r.skills ?? []).map((s) => s.name).join("、") }));
        onDone();
        return onClose();
      }
      // a server that signs in opens its page: the window is opened during the click
      const signIn = (plan.servers ?? []).find((s) => s.oauth);
      const win = signIn ? signInWindow() : null;
      const r = await apply({ kind: "mcp", source: plan.source, servers: plan.servers });
      onDone();
      const names = (r.servers ?? []).map((s) => s.name);
      toast(t("pst.serversAdded", { names: names.join("、") }));
      if (signIn) {
        const s = await settle(signIn.name);
        if (s?.status?.state === "login" && s.status.loginUrl) {
          if (win?.(s.status.loginUrl)) toast(t("mk2.signInOpened", { name: signIn.name }));
        } else win?.(null);
        onDone();
      }
      onClose();
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <div className="scrim" onClick={onClose} />
      <div className="modal pstmodal" role="dialog" aria-modal="true" aria-labelledby="pstTitle">
        <div className="mhd">
          <h3 id="pstTitle">{plan ? t("pst.confirmTitle") : t("pst.title")}</h3>
          <button className="ix" type="button" aria-label={t("common.close")} onClick={onClose}>
            <X size={14} />
          </button>
        </div>
        <div className="mbd">
          {plan ? (
            <div className="pstsrc">
              <span className="small muted">{t("pst.from")}</span>
              <code title={input}>{input.replace(/\s+/g, " ").slice(0, 120)}</code>
              <button className="linkbtn" type="button" onClick={() => setPlan(null)}>
                {t("pst.edit")}
              </button>
            </div>
          ) : (
            <>
          <p className="small" style={{ margin: "0 0 8px" }}>
              {t("pst.desc")}
            </p>
            <textarea
              className="fi mono pstin"
              spellCheck={false}
              autoComplete="off"
              rows={5}
              autoFocus
              value={input}
              placeholder={t("pst.ph")}
              onChange={(e) => {
                setInput(e.target.value);
                setPlan(null);
                setErr("");
              }}
              onPaste={(e) => {
                const text = e.clipboardData.getData("text");
                if (text && !input.trim()) {
                  e.preventDefault();
                  setInput(text);
                  void run(text);
                }
              }}
              onKeyDown={(e) => {
                if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) void run();
              }}
            />
            {!plan && (
              <div className="mft" style={{ marginTop: 10 }}>
                <button className="btn pri" type="button" disabled={busy || !input.trim()} onClick={() => void run()}>
                  {busy ? t("pst.detecting") : t("pst.detect")}
                </button>
              </div>
            )}
            </>
          )}
          {err && <p className="small errt pstErr">{err}</p>}

          {plan && (
            <div className="pstplan">
              <div className="fl">{t(`pst.kind.${plan.kind}` as any)}</div>
              {plan.note && <p className="small warnline">{tx(plan.note)}</p>}

              {plan.kind === "mcp" &&
                plan.servers!.map((s, i) => (
                  <div className="pstsrv" key={i}>
                    <div className="pstrow">
                      <MonoMark id={s.name || "x"} label={s.name || "?"} size={32} />
                      <input
                        className="fi sm mono pstname"
                        value={s.name}
                        aria-label={t("pst.name")}
                        onChange={(e) => editServer(i, (x) => ({ ...x, name: e.target.value.toLowerCase().replace(/[^a-z0-9_-]/g, "-").slice(0, 32), exists: false }))}
                      />
                      <span className="small muted mono pstwhat" title={s.url ?? (s.command ?? []).join(" ")}>
                        {s.transport === "http" ? s.url : (s.command ?? []).join(" ")}
                      </span>
                    </div>
                    {s.exists && <p className="small warnline">{t("pst.exists", { name: s.name })}</p>}
                    {s.oauth && <p className="small muted">{t("pst.oauth")}</p>}
                    {(["env", "headers"] as const).map((f) =>
                      (s[f] ?? []).map((v, k) => (
                        <label className={"pstvar" + (!v.value.trim() ? " need" : "")} key={f + k}>
                          <span className="pstvk">
                            <span className="mono" title={v.key}>
                              {v.key}
                            </span>
                            {v.secret && (
                              <span className="pstvault">
                                <Lock size={11} />
                                {t("pst.toVault")}
                              </span>
                            )}
                          </span>
                          <input
                            className="fi sm mono"
                            type={v.secret ? "password" : "text"}
                            value={v.value}
                            spellCheck={false}
                            autoComplete="off"
                            placeholder={v.missing ? t("pst.fillIn") : ""}
                            onChange={(e) => editVar(i, f, k, e.target.value)}
                          />
                        </label>
                      )),
                    )}
                  </div>
                ))}

              {plan.kind === "skill" &&
                plan.skills!.map((s) => (
                  <label className="ckrow pstskill" key={s.path}>
                    <input
                      type="checkbox"
                      disabled={s.exists}
                      checked={picked.has(s.path)}
                      onChange={(e) =>
                        setPicked((p) => {
                          const n = new Set(p);
                          if (e.target.checked) n.add(s.path);
                          else n.delete(s.path);
                          return n;
                        })
                      }
                    />
                    <span>
                      <b>{s.name}</b>
                      {s.exists && <span className="pill sm">{t("pst.installed")}</span>}
                      {s.description && <span className="small muted"> · {s.description}</span>}
                    </span>
                  </label>
                ))}

              {plan.kind === "plugin" && (
                <p className="small">
                  {t("pst.plugin")} <span className="mono">{plan.plugin}</span>
                </p>
              )}

              {plan.kind === "mcp" && <p className="small muted pstscope">{t("pst.scopeNote")}</p>}
              <div className="mft">
                {missingN > 0 && <span className="small pstneed">{t("pst.needN", { n: missingN })}</span>}
                <button className="btn ghost" type="button" onClick={onClose}>
                  {t("common.cancel")}
                </button>
                <button className="btn pri" type="button" disabled={!ready} onClick={() => void install()}>
                  {busy ? t("pst.installing") : t("pst.install")}
                </button>
              </div>
            </div>
          )}
        </div>
      </div>
    </>
  );
}
