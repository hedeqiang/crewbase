// 设置 › 网络代理 (features/proxy): an HTTP, HTTPS or SOCKS5 proxy for every
// outbound connection Crewbase makes, local addresses direct.
//
//   the switch     applies at once (with what the form shows)
//   发现           macOS's system proxy, the environment's, and common local
//                  proxy ports (Clash, Surge, v2rayN…): a click fills the form
//   the form       type, address, an optional login (the password goes to
//                  the vault), what goes direct; 测试连接 tries it unsaved
// Saving with the proxy on tests it straight away.

import { useEffect, useMemo, useState } from "react";
import { AlertCircle, Check, CircleCheck, Globe, KeyRound, LoaderCircle, Plus, RefreshCw, RotateCcw, X } from "lucide-react";
import { API, type ProxyCheck, type ProxyFound, type ProxyInput, type ProxySettings, type ProxyType } from "../../api";
import { t, type Key } from "../../i18n";
import { toast } from "../../store";
import { Head } from "../Settings";
import { Skeleton } from "../Skeleton";

const TYPES: ProxyType[] = ["http", "https", "socks5"];
const TYPE_LABEL: Record<ProxyType, string> = { http: "HTTP", https: "HTTPS", socks5: "SOCKS5" };
const DEFAULT_BYPASS = ["localhost", "127.0.0.1", "::1", ".local", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"];
const errOf = (e: any) => e?.message ?? String(e);

type Draft = ProxySettings & { password: string; clearPassword: boolean; bypassText: string };

const draftOf = (s: ProxySettings): Draft => ({ ...s, username: s.username ?? "", password: "", clearPassword: false, bypassText: s.bypass.join("\n") });

const bypassOf = (text: string) =>
  text
    .split(/[\s,;]+/)
    .map((x) => x.trim())
    .filter(Boolean);

const inputOf = (d: Draft, enabled = d.enabled): ProxyInput => ({
  enabled,
  type: d.type,
  host: d.host.trim(),
  port: Number(d.port) || 0,
  username: d.username?.trim() ?? "",
  hasPassword: d.hasPassword,
  bypass: bypassOf(d.bypassText),
  password: d.password || undefined,
  clearPassword: d.clearPassword || undefined,
});

/** "socks5://user@host:1080" pasted into the address: taken apart */
function parsePasted(raw: string): Partial<Draft> | null {
  const m = /^(https?|socks5h?|socks):\/\/(?:([^:@/]*)(?::[^@/]*)?@)?(\[[^\]]+\]|[^:/]+):(\d+)/i.exec(raw.trim());
  if (!m) return null;
  const scheme = m[1].toLowerCase();
  const type: ProxyType = scheme.startsWith("socks") ? "socks5" : (scheme as ProxyType);
  return { type, host: m[3].replace(/^\[|\]$/g, ""), port: Number(m[4]), ...(m[2] ? { username: m[2] } : {}) };
}

export function ProxyPage() {
  const [saved, setSaved] = useState<ProxySettings | null>(null);
  const [env, setEnv] = useState<string | undefined>();
  const [d, setD] = useState<Draft | null>(null);
  const [busy, setBusy] = useState<"" | "save" | "test" | "toggle">("");
  const [check, setCheck] = useState<ProxyCheck | null>(null);
  const [err, setErr] = useState("");
  const [login, setLogin] = useState(false);

  useEffect(() => {
    API.proxySettings().then(
      (v) => {
        setSaved(v.settings);
        setEnv(v.env);
        setD(draftOf(v.settings));
        setLogin(!!v.settings.username || v.settings.hasPassword);
      },
      (e) => toast(errOf(e)),
    );
  }, []);

  const dirty = useMemo(() => {
    if (!d || !saved) return false;
    const a = inputOf(d);
    return (
      a.type !== saved.type ||
      a.host !== saved.host ||
      a.port !== saved.port ||
      (a.username ?? "") !== (saved.username ?? "") ||
      !!a.password ||
      !!a.clearPassword ||
      a.bypass.join(",") !== saved.bypass.join(",")
    );
  }, [d, saved]);

  if (!d || !saved)
    return (
      <>
        <Head title={t("set.proxy")} sub={t("sub.proxy")} />
        <div className="vb">
          <Skeleton kind="rows" />
        </div>
      </>
    );

  const set = (x: Partial<Draft>) => {
    setD({ ...d, ...x });
    setErr("");
    setCheck(null);
  };

  const test = async (input: ProxyInput) => {
    setBusy("test");
    setCheck(null);
    try {
      setCheck(await API.testProxy(input));
    } catch (e) {
      setErr(errOf(e));
    } finally {
      setBusy("");
    }
  };

  const save = async (enabled: boolean, why: "save" | "toggle") => {
    setBusy(why);
    setErr("");
    try {
      const v = await API.setProxy(inputOf(d, enabled));
      setSaved(v.settings);
      setEnv(v.env);
      setD(draftOf(v.settings));
      toast(why === "toggle" ? (enabled ? t("px.turnedOn") : t("px.turnedOff")) : t("px.saved"));
      setBusy("");
      // on: see that it works
      if (v.settings.enabled) await test(inputOf(draftOf(v.settings), true));
      else setCheck(null);
    } catch (e) {
      setErr(errOf(e));
      setBusy("");
    }
  };

  const status = saved.enabled
    ? t("px.statusOn", { type: TYPE_LABEL[saved.type], addr: `${saved.host}:${saved.port}` })
    : env
      ? t("px.statusEnv", { env })
      : t("px.statusOff");

  return (
    <>
      <Head title={t("set.proxy")} sub={t("sub.proxy")} />
      <div className="vb px">
        <section className={"pxhero" + (saved.enabled ? " on" : "")}>
          <span className="pxic">
            <Globe size={20} />
          </span>
          <div className="g">
            <b>{t("px.use")}</b>
            <span>{status}</span>
          </div>
          {busy === "toggle" ? (
            <LoaderCircle size={18} className="pxspin" />
          ) : (
            <button
              className="tg"
              type="button"
              role="switch"
              aria-checked={saved.enabled}
              aria-label={t("px.use")}
              disabled={!!busy}
              onClick={() => void save(!saved.enabled, "toggle")}
            />
          )}
        </section>

        <Found
          onPick={(f) => {
            set({ type: f.type, host: f.host, port: f.port });
          }}
          current={d}
        />

        <section className="pxcard">
          <h4>{t("px.server")}</h4>
          <div className="fl">
            {t("px.type")}
            <span className="seg pxtypes" role="radiogroup" aria-label={t("px.type")}>
              {TYPES.map((x) => (
                <button key={x} type="button" role="radio" aria-checked={d.type === x} aria-pressed={d.type === x} onClick={() => set({ type: x })}>
                  {TYPE_LABEL[x]}
                </button>
              ))}
            </span>
            <span className="small muted">{t(`px.typeHint.${d.type}` as Key)}</span>
          </div>
          <div className="pxaddr">
            <label className="fl">
              {t("px.host")}
              <input
                className="fi mono"
                value={d.host}
                placeholder="127.0.0.1"
                spellCheck={false}
                autoCapitalize="off"
                onChange={(e) => {
                  const p = parsePasted(e.target.value);
                  if (p) {
                    set(p);
                    if (p.username) setLogin(true);
                  } else set({ host: e.target.value });
                }}
              />
            </label>
            <span className="pxcolon">:</span>
            <label className="fl pxport">
              {t("px.port")}
              <input className="fi mono" inputMode="numeric" value={d.port || ""} placeholder="7890" onChange={(e) => set({ port: Number(e.target.value.replace(/\D/g, "").slice(0, 5)) || 0 })} />
            </label>
          </div>
          <span className="small muted pxpaste">{t("px.pasteHint")}</span>

          {login ? (
            <div className="pxlogin">
              <div className="pxloginh">
                <KeyRound size={14} />
                {t("px.login")}
                <span className="sp" />
                {!saved.username && !saved.hasPassword && (
                  <button type="button" className="ix" aria-label={t("px.noLogin")} title={t("px.noLogin")} onClick={() => (setLogin(false), set({ username: "", password: "" }))}>
                    <X size={14} />
                  </button>
                )}
              </div>
              <div className="pxaddr">
                <label className="fl">
                  {t("px.username")}
                  <input className="fi" value={d.username ?? ""} autoComplete="off" spellCheck={false} autoCapitalize="off" onChange={(e) => set({ username: e.target.value })} />
                </label>
                <label className="fl">
                  {t("px.password")}
                  <input
                    className="fi"
                    type="password"
                    autoComplete="new-password"
                    value={d.password}
                    disabled={d.clearPassword}
                    placeholder={d.hasPassword && !d.clearPassword ? t("px.passwordKeep") : ""}
                    onChange={(e) => set({ password: e.target.value })}
                  />
                </label>
              </div>
              <span className="small muted">
                {t("px.passwordHint")}
                {d.hasPassword && (
                  <>
                    {" "}
                    <button type="button" className="linkbtn" onClick={() => set({ clearPassword: !d.clearPassword, password: "" })}>
                      {d.clearPassword ? t("px.keepPassword") : t("px.clearPassword")}
                    </button>
                  </>
                )}
              </span>
            </div>
          ) : (
            <button type="button" className="linkbtn small pxaddlogin" onClick={() => setLogin(true)}>
              <Plus size={13} />
              {t("px.addLogin")}
            </button>
          )}
        </section>

        <section className="pxcard">
          <h4>
            {t("px.bypass")}
            {d.bypassText.trim() !== DEFAULT_BYPASS.join("\n") && (
              <button type="button" className="linkbtn small" onClick={() => set({ bypassText: DEFAULT_BYPASS.join("\n") })}>
                <RotateCcw size={12} />
                {t("px.bypassReset")}
              </button>
            )}
          </h4>
          <textarea className="fi mono pxbypass" rows={4} spellCheck={false} value={d.bypassText} onChange={(e) => set({ bypassText: e.target.value })} />
          <span className="small muted">{t("px.bypassHint")}</span>
        </section>

        {check && <Result c={check} type={d.type} />}
        {err && (
          <p className="pxerr">
            <AlertCircle size={15} />
            {err}
          </p>
        )}

        <div className="pxfoot">
          <button type="button" className="btn" disabled={!!busy || !d.host.trim() || !d.port} onClick={() => void test(inputOf(d, true))}>
            {busy === "test" ? <LoaderCircle size={14} className="pxspin" /> : <RefreshCw size={14} />}
            {t("px.test")}
          </button>
          <span className="sp" />
          {dirty && (
            <button type="button" className="btn ghost" disabled={!!busy} onClick={() => (setD(draftOf(saved)), setCheck(null), setErr(""))}>
              {t("common.cancel")}
            </button>
          )}
          <button type="button" className="btn pri" disabled={!dirty || !!busy} onClick={() => void save(saved.enabled, "save")}>
            {busy === "save" ? <LoaderCircle size={14} className="pxspin" /> : null}
            {t("common.save")}
          </button>
        </div>

        <section className="pxscope">
          <b>{t("px.scope")}</b>
          <ul>
            {(["models", "downloads", "github", "previews", "commands"] as const).map((k) => (
              <li key={k}>
                <Check size={13} />
                {t(`px.scope.${k}` as Key)}
              </li>
            ))}
          </ul>
          <span className="small muted">{t("px.scopeLocal")}</span>
        </section>
      </div>
    </>
  );
}

/** proxies already on this machine; a click fills the form */
function Found({ onPick, current }: { onPick: (f: ProxyFound) => void; current: Draft }) {
  const [found, setFound] = useState<ProxyFound[] | null>(null);
  const [busy, setBusy] = useState(false);
  const run = () => {
    setBusy(true);
    API.detectProxy().then(
      (r) => (setFound(r.found), setBusy(false)),
      () => (setFound([]), setBusy(false)),
    );
  };
  useEffect(run, []);
  return (
    <section className="pxfound" aria-busy={busy}>
      <span className="small muted pxfoundh">{busy && !found ? t("px.detecting") : found?.length ? t("px.found") : t("px.foundNone")}</span>
      {found?.map((f) => {
        const on = current.type === f.type && current.host === f.host && current.port === f.port;
        return (
          <button key={`${f.source}${f.type}${f.host}${f.port}`} type="button" className={"pxchip" + (on ? " on" : "")} onClick={() => onPick(f)} title={t("px.useThis")}>
            {on ? <CircleCheck size={13} /> : null}
            <span className="src">{t(`px.src.${f.source}` as Key)}</span>
            <b>{TYPE_LABEL[f.type]}</b>
            <span className="mono">
              {f.host}:{f.port}
            </span>
            {f.also && <span className="also">+{TYPE_LABEL[f.also as ProxyType] ?? f.also}</span>}
          </button>
        );
      })}
      <button type="button" className="ix pxre" aria-label={t("px.detectAgain")} title={t("px.detectAgain")} disabled={busy} onClick={run}>
        <RefreshCw size={13} className={busy ? "pxspin" : ""} />
      </button>
    </section>
  );
}

function Result({ c, type }: { c: ProxyCheck; type: ProxyType }) {
  if (c.ok)
    return (
      <p className="pxres ok">
        <CircleCheck size={16} />
        <span>{t("px.ok", { target: c.target, ms: c.ms })}</span>
      </p>
    );
  const other = type === "socks5" ? "HTTP" : "SOCKS5";
  const msg =
    c.code === "status"
      ? t("px.err.status", { target: c.target, status: c.status ?? 0 })
      : c.code === "type"
        ? t("px.err.type", { other })
        : c.code === "timeout"
          ? t("px.err.timeout", { target: c.target })
          : c.code && c.code !== "other"
            ? t(`px.err.${c.code}` as Key)
            : t("px.err.other");
  return (
    <div className="pxres bad">
      <AlertCircle size={16} />
      <div>
        <span>{msg}</span>
        {c.error && <code>{c.error}</code>}
      </div>
    </div>
  );
}
