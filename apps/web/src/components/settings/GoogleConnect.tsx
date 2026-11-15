// The built-in Google connector (features/google): one Google sign-in for
// Gmail, Calendar, Drive, Docs, Sheets and Slides. Extensions.tsx calls
// connectBuiltin for catalog rows with kind "builtin" and shows
// <GoogleAccount /> on its 已安装 tab.
import { useEffect, useRef, useState } from "react";
import { LogIn } from "lucide-react";
import { api, openExternal } from "../../api";
import { t } from "../../i18n";
import { toast, useStore } from "../../store";
import { errMsg } from "./ExtParts";

import google from "@lobehub/icons-static-svg/icons/google-color.svg?raw";
import { confirmDialog } from "../Confirm";

export interface GoogleProduct {
  id: string;
  name: string;
  catalogId: string;
  connected: boolean;
  scope?: string;
  scopes: string[];
  tools: string[];
}
export interface GoogleView {
  configured: boolean;
  source?: string;
  missing?: string;
  connected: boolean;
  email?: string;
  needsLogin?: boolean;
  scopes?: string[];
  products: GoogleProduct[];
  pending?: string;
  lastError?: string;
  redirectUri?: string;
}

export const googleAPI = {
  status: () => api<GoogleView>("GET", "/api/google"),
  connect: (product: string, scope?: string) => api<{ authUrl: string }>("POST", "/api/google/connect", { product, scope }),
  finish: (url: string) => api<GoogleView>("POST", "/api/google/finish", { url }),
  disconnect: (product?: string) => api<GoogleView>("POST", "/api/google/disconnect", { product }),
};

/** A catalog row backed by a builtin connector (the parts Extensions needs). */
export interface BuiltinItem {
  id: string;
  name: string;
  builtin?: string;
  product?: string;
  connector?: { configured: boolean; needsLogin?: boolean; account?: string; note?: { zh: string; en: string } };
}

/** 连接: opens Google's sign-in page for the item's product. */
export async function connectBuiltin(it: BuiltinItem, scope?: string) {
  if (it.builtin !== "google" || !it.product) return;
  if (it.connector && !it.connector.configured) {
    toast(t("gws.notSetUp"));
    return;
  }
  try {
    const r = await googleAPI.connect(it.product, scope);
    openExternal(r.authUrl);
    toast(t("gws.continueInBrowser", { name: it.name }));
  } catch (e) {
    toast(t("mk.addFailed", { error: errMsg(e) }));
  }
}

/** The row's second line for a builtin item: the account or what is wrong. */
export function builtinNote(it: BuiltinItem): string | null {
  const c = it.connector;
  if (!c) return null;
  if (!c.configured) return t("gws.notSetUpShort");
  if (c.needsLogin) return t("gws.needsLogin");
  return null;
}

/** 已安装 › Google: the signed-in account, its products, sign out. */
export function GoogleAccount({ onChange }: { onChange: () => void }) {
  const tick = useStore((s) => s.mcpTick);
  const [v, setV] = useState<GoogleView | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [paste, setPaste] = useState("");
  const seq = useRef(0);
  const load = () => {
    const n = ++seq.current; // only the newest answer counts
    return googleAPI.status().then(
      (r) => n === seq.current && setV(r),
      () => n === seq.current && setV((x) => x),
    );
  };
  useEffect(() => {
    void load();
  }, [tick]);
  if (!v || (!v.connected && !v.pending)) return null;
  const run = async (key: string, f: () => Promise<unknown>, ok?: string) => {
    setBusy(key);
    try {
      await f();
      if (ok) toast(ok);
    } catch (e) {
      toast(errMsg(e));
    } finally {
      setBusy(null);
      void load();
      onChange();
    }
  };
  const on = v.products.filter((p) => p.connected);
  return (
    <section className="mksec">
      <div className="mksh">
        <h3>Google</h3>
      </div>
      <div className="mklist">
        <div className="mkirow">
          <div className="mkrow">
            <span className="mkic brand" aria-hidden dangerouslySetInnerHTML={{ __html: google }} />
            <div className="g">
              <b>
                {v.email || t("gws.account")}
                {v.needsLogin ? (
                  <span className="pill sm red">{t("gws.needsLoginShort")}</span>
                ) : v.connected ? (
                  <span className="pill sm ok">{t("gws.connected")}</span>
                ) : (
                  <span className="pill sm amber">{t("gws.waiting")}</span>
                )}
              </b>
              <span>{on.length ? on.map((p) => p.name).join(" · ") : t("gws.waitingDesc")}</span>
            </div>
            {v.needsLogin && on[0] && (
              <button className="btn pri sm" type="button" disabled={!!busy} onClick={() => void connectBuiltin({ id: on[0].catalogId, name: on[0].name, builtin: "google", product: on[0].id, connector: { configured: v.configured } }, on[0].scope)}>
                <LogIn size={12} /> {t("gws.signInAgain")}
              </button>
            )}
            {v.connected && (
              <button
                className="btn sm"
                type="button"
                disabled={!!busy}
                onClick={async () => {
                  if (await confirmDialog(t("gws.signOutConfirm", { email: v.email ?? "" }))) void run("out", () => googleAPI.disconnect(), t("gws.signedOut"));
                }}
              >
                {t("gws.signOut")}
              </button>
            )}
          </div>
          {v.lastError && <div className="small errt mkerr">{v.lastError}</div>}
          {on.map((p) => (
            <div className="mkrow" key={p.id}>
              <div className="g">
                <b>{p.name}</b>
                <span className="mono">{t("ext.toolsN", { n: p.tools.length })} · {p.tools.join(", ")}</span>
              </div>
              <button
                className="btn ghost sm"
                type="button"
                disabled={busy === p.id}
                onClick={() => void run(p.id, () => googleAPI.disconnect(p.id), t("mk.removed", { name: p.name }))}
              >
                {t("gws.disconnect")}
              </button>
            </div>
          ))}
          {v.pending && (
            <form
              className="runbar mkpath"
              onSubmit={(e) => {
                e.preventDefault();
                if (paste.trim()) void run("paste", () => googleAPI.finish(paste.trim()).then(() => setPaste("")), t("gws.connectedToast"));
              }}
            >
              <input className="fi sm mono" value={paste} placeholder={t("gws.pastePh")} aria-label={t("gws.pasteLabel")} onChange={(e) => setPaste(e.target.value)} />
              <button className="btn" type="submit" disabled={!paste.trim() || busy === "paste"}>
                {t("gws.pasteBtn")}
              </button>
            </form>
          )}
          {v.pending && <p className="small">{t("gws.pasteHint")}</p>}
        </div>
      </div>
    </section>
  );
}
