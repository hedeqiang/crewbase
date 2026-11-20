// Open Agent Docs (features/agentdocs): a self-hosted document server where
// people and agents write pages together. Connected once with its address
// and an admin agent token; every agent then gets its own identity there,
// and comments that @mention one become its tasks.

import { useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { Check, ExternalLink, FileText, RefreshCw, X } from "lucide-react";
import { API, openExternal, type AgentDocsStatus } from "../../api";
import { t } from "../../i18n";
import { toast, useStore } from "../../store";
import { confirmDialog } from "../Confirm";

const REPO = "https://github.com/kadaliao/open-agent-docs";

export function useAgentDocs() {
  const [st, setSt] = useState<AgentDocsStatus | null>(null);
  const load = () => API.agentDocs().then(setSt, () => setSt(null));
  useEffect(() => {
    void load();
  }, []);
  return { st, setSt, load };
}

export function AgentDocsIcon({ size = 36 }: { size?: number }) {
  return (
    <span className="adicon" style={{ width: size, height: size }}>
      <FileText size={Math.round(size * 0.5)} />
    </span>
  );
}

/** The card's dialog: connect, or the connection's state. */
export function AgentDocsDialog({ st, onClose, onChanged }: { st: AgentDocsStatus | null; onClose: () => void; onChanged: (s: AgentDocsStatus) => void }) {
  const agents = useStore((s) => s.agents);
  const [url, setUrl] = useState(st?.settings.url || "http://127.0.0.1:8848");
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    const k = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", k);
    return () => window.removeEventListener("keydown", k);
  }, [onClose]);
  const connected = !!st?.connected;
  const run = async (fn: () => Promise<AgentDocsStatus>, ok?: string) => {
    setBusy(true);
    setErr(null);
    try {
      const s = await fn();
      onChanged(s);
      if (ok) toast(ok);
    } catch (e: any) {
      setErr(e?.message ?? String(e));
    } finally {
      setBusy(false);
    }
  };
  const nameOf = (id: string) => agents[id]?.name ?? id;
  const docsURL = st?.settings.url;

  return createPortal(
    <>
      <div className="scrim" onClick={onClose} />
      <div className="modal mkdlg addlg" role="dialog" aria-modal="true" aria-labelledby="adTitle">
        <div className="mhd">
          <AgentDocsIcon size={32} />
          <h3 id="adTitle">Open Agent Docs</h3>
          <button className="ix" type="button" aria-label={t("common.close")} onClick={onClose}>
            <X size={14} />
          </button>
        </div>
        <div className="mbd">
          <p className="small addesc">{t("ad.desc")}</p>
          {!connected ? (
            <>
              <ol className="adsteps small">
                <li>
                  {t("ad.step1")}{" "}
                  <button className="linkbtn" type="button" onClick={() => openExternal(REPO + "#run-it")}>
                    {t("ad.howToRun")}
                  </button>
                </li>
                <li>{t("ad.step2")}</li>
                <li>{t("ad.step3")}</li>
              </ol>
              <label className="fl">{t("ad.url")}</label>
              <input className="fi sm mono" value={url} onChange={(e) => setUrl(e.target.value)} placeholder="http://127.0.0.1:8848" />
              <label className="fl">{t("ad.token")}</label>
              <input className="fi sm mono" type="password" value={token} onChange={(e) => setToken(e.target.value)} placeholder="oad_…" autoComplete="off" />
              <p className="small muted">{t("ad.tokenNote")}</p>
              {err && <div className="ferr">{err}</div>}
              <div className="acts">
                <button className="btn pri" type="button" disabled={busy || !url.trim() || !token.trim()} onClick={() => run(() => API.connectAgentDocs({ url, token }), t("ad.connected"))}>
                  {busy ? t("ad.connecting") : t("mk2.connect")}
                </button>
                <button className="btn ghost" type="button" onClick={onClose}>
                  {t("common.cancel")}
                </button>
              </div>
            </>
          ) : (
            <>
              <div className="adconn">
                <span className="pill ok">
                  <Check size={11} />
                  {t("ad.connectedTo", { ws: st!.settings.workspaceName || st!.settings.workspace })}
                </span>
                <span className="mono small muted">{docsURL}</span>
                {docsURL && (
                  <button className="ix" type="button" title={t("ad.open")} aria-label={t("ad.open")} onClick={() => openExternal(docsURL)}>
                    <ExternalLink size={13} />
                  </button>
                )}
              </div>
              {st!.error && <div className="ferr">{st!.error}</div>}
              <div className="psec2 psec2r">
                <span>{t("ad.identities", { n: st!.identities.length })}</span>
                <button className="ix" type="button" title={t("ad.sync")} aria-label={t("ad.sync")} disabled={busy} onClick={() => run(() => API.syncAgentDocs())}>
                  <RefreshCw size={13} />
                </button>
              </div>
              <p className="small muted" style={{ marginTop: 0 }}>
                {t("ad.identitiesDesc")}
              </p>
              <div className="adids">
                {st!.identities.map((x) => (
                  <div key={x.agent} className="adid">
                    <b>{nameOf(x.agent)}</b>
                    <span className="small muted">
                      {t("ad.signsAs")} <code>@{x.name}</code>
                    </span>
                  </div>
                ))}
                {st!.identities.length === 0 && <p className="small muted">{t("ad.noIdentities")}</p>}
              </div>
              <label className="adwake">
                <input type="checkbox" checked={st!.settings.wake} disabled={busy} onChange={(e) => run(() => API.patchAgentDocs({ wake: e.target.checked }))} />
                <span>
                  <b>{t("ad.wake")}</b>
                  <span className="small muted">{t("ad.wakeDesc")}</span>
                </span>
              </label>
              {st!.woken > 0 && <p className="small muted">{t("ad.woken", { n: st!.woken })}</p>}
              {err && <div className="ferr">{err}</div>}
              <div className="acts">
                <button
                  className="btn dng sm"
                  type="button"
                  disabled={busy}
                  onClick={async () => {
                    if (!(await confirmDialog({ text: t("ad.disconnectConfirm"), danger: true, ok: t("mk2.disconnect") }))) return;
                    void run(() => API.disconnectAgentDocs(), t("ad.disconnected"));
                  }}
                >
                  {t("mk2.disconnect")}
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
