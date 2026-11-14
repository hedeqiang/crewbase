import { Loader2, ShieldCheck, Smartphone } from "lucide-react";
import { useState } from "react";
import { API, setToken } from "../api";
import { t } from "../i18n";
import { saveRelay, useRelay, type RelayHost } from "../tunnel";
import { Logo } from "./Logo";

/** Parses #/pair?host=…&key=…&code=… (relay) or #/pair?code=… (same network). */
export function pairParams(hash = location.hash): { code: string; relay: RelayHost | null } | null {
  const m = /^#\/pair\?(.*)$/.exec(hash);
  if (!m) return null;
  const q = new URLSearchParams(m[1]);
  const host = q.get("host") ?? "";
  const key = q.get("key") ?? "";
  return { code: q.get("code") ?? "", relay: host && key ? { host, key } : null };
}

function platformHint(): string {
  const ua = navigator.userAgent;
  const os = /iPhone/.test(ua) ? "iPhone" : /iPad/.test(ua) ? "iPad" : /Android/.test(ua) ? "Android" : /Mac/.test(ua) ? "Mac" : /Windows/.test(ua) ? "Windows" : /Linux/.test(ua) ? "Linux" : "";
  const br = /Edg\//.test(ua) ? "Edge" : /Chrome\//.test(ua) ? "Chrome" : /Firefox\//.test(ua) ? "Firefox" : /Safari\//.test(ua) ? "Safari" : "";
  return [os, br].filter(Boolean).join(" · ");
}

/** 配对这台设备: name it, redeem the one-time code, keep the device token. */
export function PairScreen({ code: initial, relay, onPaired }: { code: string; relay: RelayHost | null; onPaired: () => void }) {
  const [code, setCode] = useState(initial);
  const [name, setName] = useState(() => platformHint().split(" · ")[0] || "");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const submit = async () => {
    setBusy(true);
    setErr("");
    try {
      if (relay) useRelay(relay);
      const r = await API.pair({ code: code.trim(), name: name.trim(), platform: platformHint() });
      setToken(r.token);
      if (relay) saveRelay(relay);
      // the code was single-use; drop it from the address bar and history
      history.replaceState(null, "", location.pathname + location.search);
      onPaired();
    } catch (e: any) {
      setErr(e?.status === 401 ? t("pair.badCode") : e?.status === 429 ? t("pair.slowDown") : e?.message ?? String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="tokscreen">
      <form
        className="tokcard"
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
      >
        <Logo size={48} title="Crewbase" />
        <h1>{t("pair.title")}</h1>
        <p>{relay ? t("pair.descRelay") : t("pair.desc")}</p>
        {relay && (
          <p className="pairsec">
            <ShieldCheck size={14} /> {t("pair.e2e")}
          </p>
        )}
        <label className="fl">
          <span>{t("pair.code")}</span>
          <input className="fi mono paircode" value={code} placeholder="XXXX-XXXX" autoComplete="one-time-code" spellCheck={false} onChange={(e) => setCode(e.target.value.toUpperCase())} />
        </label>
        <label className="fl">
          <span>{t("pair.name")}</span>
          <span className="iwrap fi">
            <Smartphone size={15} />
            <input value={name} maxLength={40} placeholder={t("pair.namePh")} onChange={(e) => setName(e.target.value)} />
          </span>
        </label>
        {err && <div className="ferr">{err}</div>}
        <button className="btn pri big" type="submit" disabled={busy || code.replace(/[^A-Za-z0-9]/g, "").length !== 8}>
          {busy && <Loader2 size={15} className="spin" />}
          {t("pair.submit")}
        </button>
      </form>
    </div>
  );
}
