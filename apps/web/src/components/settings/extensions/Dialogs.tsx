// 插件与技能: connecting a server (a key, OAuth, a device code, GitHub), and
// its scope and access settings.

import { Skeleton } from "../../Skeleton";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { Check, Copy, ExternalLink, X } from "lucide-react";
import { API, openExternal, type AgentView, type McpServer } from "../../../api";
import { canUse, connectorsFor, nextConnectors, useConnectors } from "../../AgentConnectors";
import { t } from "../../../i18n";
import { toast, useStore } from "../../../store";
import { errMsg } from "../ExtParts";
import { ItemIcon, catalogAPI, sleep, tx, type CatItem, type SignInView, type Target } from "./shared";

// Brand icons from LobeHub Icons (@lobehub/icons-static-svg, MIT), as in
// ProviderMark, where the package has the mark; the others are the
// companies' own marks from their brand pages or sites (src/assets/brands).
// Anything else gets a letter tile.

export function useEsc(onClose: () => void) {
  useEffect(() => {
    const esc = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", esc);
    return () => window.removeEventListener("keydown", esc);
  }, [onClose]);
}

/** A portalled modal (the page body is a size container, which would
 *  otherwise be the containing block of the fixed scrim and modal). */
export function Modal({ item, title, onClose, children }: { item: CatItem; title: string; onClose: () => void; children: ReactNode }) {
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
export function ConnectDialog({ item, target, servers, onClose, onChanged, onSignIn }: { item: CatItem; target: Target; servers: McpServer[]; onClose: () => void; onChanged: () => void; onSignIn?: (it: CatItem) => void }) {
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
export function GitHubDialog({ item, target, servers, onClose, onChanged }: { item: CatItem; target: Target; servers: McpServer[]; onClose: () => void; onChanged: () => void }) {
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

export function ScopeSelect({ value, onChange, teamsToo }: { value: string; onChange: (v: string) => void; teamsToo: boolean }) {
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
export function ScopeDialog({ server, label, onClose, onSaved }: { server: McpServer; label: string; onClose: () => void; onSaved: () => void }) {
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
export function AccessDialog({ server, label, onClose }: { server: McpServer; label: string; onClose: () => void }) {
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
