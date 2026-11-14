import { Cloud, Copy, Laptop, Loader2, Pencil, RefreshCw, ShieldCheck, Smartphone, Trash2, Wifi, X } from "lucide-react";
import qrcode from "qrcode-generator";
import { useEffect, useMemo, useState } from "react";
import { API, type DeviceView, type PairingCode, type RelayStatus } from "../../api";
import { ago } from "../../format";
import { t } from "../../i18n";
import { toast, useStore } from "../../store";
import { Head } from "../Settings";

/** A QR code drawn as SVG rects (no HTML injection). */
function QR({ text, size = 176 }: { text: string; size?: number }) {
  const cells = useMemo(() => {
    const q = qrcode(0, "M");
    q.addData(text);
    q.make();
    const n = q.getModuleCount();
    const out: [number, number][] = [];
    for (let r = 0; r < n; r++) for (let c = 0; c < n; c++) if (q.isDark(r, c)) out.push([c, r]);
    return { n, out };
  }, [text]);
  const m = 2; // quiet zone, in modules
  return (
    <svg className="qr" width={size} height={size} viewBox={`${-m} ${-m} ${cells.n + 2 * m} ${cells.n + 2 * m}`} role="img" aria-label={t("dev.qr")} shapeRendering="crispEdges">
      <rect x={-m} y={-m} width={cells.n + 2 * m} height={cells.n + 2 * m} fill="#fff" />
      {cells.out.map(([x, y]) => (
        <rect key={x + "," + y} x={x} y={y} width={1} height={1} fill="#111" />
      ))}
    </svg>
  );
}

function useNow(ms = 1000) {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const i = window.setInterval(() => setNow(Date.now()), ms);
    return () => window.clearInterval(i);
  }, [ms]);
  return now;
}

/** 设置 › 主机与设备: pair phones and computers, see the relay link. */
export function Hosts() {
  const [devices, setDevices] = useState<DeviceView[] | null>(null);
  const [relay, setRelay] = useState<RelayStatus | null>(null);
  const [err, setErr] = useState("");
  const [open, setOpen] = useState<{ p: PairingCode; link: string } | null>(null);
  const [busy, setBusy] = useState("");
  const [rename, setRename] = useState<{ id: string; name: string } | null>(null);
  const sys = useStore((s) => s.profile.system);
  const now = useNow();

  const load = async () => {
    try {
      const [d, r] = await Promise.allSettled([API.devices(), API.relay()]);
      if (d.status === "fulfilled") setDevices(d.value.devices);
      else setErr(d.reason?.status === 404 ? t("dev.old") : d.reason?.message ?? String(d.reason));
      setRelay(r.status === "fulfilled" ? r.value : null);
    } catch (e: any) {
      setErr(e?.message ?? String(e));
    }
  };
  useEffect(() => {
    void load();
    const i = window.setInterval(() => void load(), 5000);
    return () => window.clearInterval(i);
  }, []);

  const start = async (tier: "mobile" | "desktop") => {
    setBusy("pair");
    try {
      const p = await API.startPairing(tier);
      // through the relay when one is set up, else the address of this page
      const link = relay?.configured ? (await API.relayPairLink(p.code)).url : `${location.origin}/#/pair?code=${encodeURIComponent(p.code)}`;
      setOpen({ p, link });
    } catch (e: any) {
      toast(e?.message ?? String(e));
    } finally {
      setBusy("");
    }
  };

  const close = async () => {
    if (open) void API.cancelPairing(open.p.code).catch(() => {});
    setOpen(null);
    void load();
  };

  // the dialog closes itself when the code is used or expires
  useEffect(() => {
    if (!open) return;
    if (new Date(open.p.expires).getTime() <= now) setOpen(null);
  }, [now, open]);
  const prevCount = useMemo(() => devices?.length ?? 0, [open]);
  useEffect(() => {
    if (open && devices && devices.length > prevCount) {
      toast(t("dev.pairedToast", { name: devices[0]?.name ?? "" }));
      setOpen(null);
    }
  }, [devices]);

  const revoke = async (d: DeviceView) => {
    if (!window.confirm(t("dev.revokeConfirm", { name: d.name }))) return;
    try {
      await API.revokeDevice(d.id);
      toast(t("dev.revoked", { name: d.name }));
      void load();
    } catch (e: any) {
      toast(e?.message ?? String(e));
    }
  };

  const saveName = async () => {
    if (!rename) return;
    try {
      await API.renameDevice(rename.id, rename.name);
      setRename(null);
      void load();
    } catch (e: any) {
      toast(e?.message ?? String(e));
    }
  };

  const local = location.hostname === "127.0.0.1" || location.hostname === "localhost";
  const left = open ? Math.max(0, Math.round((new Date(open.p.expires).getTime() - now) / 1000)) : 0;

  return (
    <>
      <Head title={t("set.hosts")} />
      <div className="vb">
        <div className="vsec">{t("dev.thisHost")}</div>
        <div className="box hostcard">
          <span className="hosticon">
            <Laptop size={18} />
          </span>
          <span className="g">
            <b>{sys?.hostname || t("dev.thisComputer")}</b>
            <span className="small">
              {sys ? `${sys.os ?? ""} ${sys.arch ?? ""}`.trim() : ""} · {t("dev.zeroInbound")}
            </span>
          </span>
          <span className="pill ok">{t("dev.online")}</span>
        </div>

        <div className="vsec">{t("dev.relay")}</div>
        <div className="box relaybox">
          {relay?.configured && relay.status ? (
            <div className="relayrow">
              <Cloud size={16} />
              <span className="g">
                <b className="mono">{relay.status.relay.replace(/^https?:\/\//, "")}</b>
                <span className="small">
                  {relay.status.online ? t("dev.relayOnline", { n: relay.status.devices }) : relay.status.error ? t("dev.relayErr", { error: relay.status.error }) : t("dev.relayConnecting")}
                </span>
              </span>
              <span className={"pill " + (relay.status.online ? "ok" : "amber")}>{relay.status.online ? t("dev.connected") : t("dev.offline")}</span>
            </div>
          ) : (
            <div className="relayrow">
              <Wifi size={16} />
              <span className="g">
                <b>{t("dev.noRelay")}</b>
                <span className="small">{t("dev.noRelayDesc")}</span>
              </span>
            </div>
          )}
          <p className="small relaynote">
            <ShieldCheck size={13} /> {t("dev.e2eNote")}
          </p>
        </div>

        <div className="vsec devhead">
          <span>{t("dev.devices")}</span>
          <span className="sp" />
          <button className="btn sm" type="button" disabled={busy !== "" || !!err} onClick={() => void start("desktop")}>
            <Laptop size={13} /> {t("dev.pairComputer")}
          </button>
          <button className="btn pri sm" type="button" disabled={busy !== "" || !!err} onClick={() => void start("mobile")}>
            {busy === "pair" ? <Loader2 size={13} className="spin" /> : <Smartphone size={13} />} {t("dev.pairPhone")}
          </button>
        </div>
        {err && <p className="small errt">{err}</p>}
        {devices && devices.length === 0 && !err && <p className="small devempty">{t("dev.none")}</p>}
        {devices && devices.length > 0 && (
          <div className="htable">
            {devices.map((d) => (
              <div key={d.id} className="hrow devrow">
                <span className="hosticon sm">{d.tier === "desktop" ? <Laptop size={15} /> : <Smartphone size={15} />}</span>
                <span className="g">
                  {rename?.id === d.id ? (
                    <input
                      className="fi sm"
                      autoFocus
                      value={rename.name}
                      maxLength={40}
                      onChange={(e) => setRename({ id: d.id, name: e.target.value })}
                      onBlur={() => void saveName()}
                      onKeyDown={(e) => {
                        if (e.key === "Enter") void saveName();
                        if (e.key === "Escape") setRename(null);
                      }}
                    />
                  ) : (
                    <b>{d.name}</b>
                  )}
                  <span>
                    {[d.platform, d.tier === "desktop" ? t("dev.tierDesktop") : t("dev.tierMobile"), d.via === "relay" ? t("dev.viaRelay") : t("dev.viaLan"), d.lastSeen ? t("dev.seen", { t: ago(d.lastSeen) }) : ""]
                      .filter(Boolean)
                      .join(" · ")}
                  </span>
                </span>
                <button className="ix" type="button" title={t("dev.rename")} aria-label={t("dev.rename")} onClick={() => setRename({ id: d.id, name: d.name })}>
                  <Pencil size={13} />
                </button>
                <button className="ix dng" type="button" title={t("dev.revoke")} aria-label={t("dev.revoke")} onClick={() => void revoke(d)}>
                  <Trash2 size={13} />
                </button>
              </div>
            ))}
          </div>
        )}
        <p className="small" style={{ marginTop: 12 }}>
          {t("dev.tierNote")}
        </p>
      </div>

      {open && (
        <>
          <div className="ovscrim show" onMouseDown={() => void close()} />
          <div className="modal pairdlg" role="dialog" aria-modal="true" aria-label={t("dev.pairTitle")}>
            <div className="mhd">
              <h3>{open.p.tier === "desktop" ? t("dev.pairComputer") : t("dev.pairPhone")}</h3>
              <button className="ib" type="button" aria-label={t("common.close")} onClick={() => void close()}>
                <X size={16} />
              </button>
            </div>
            <div className="mbd pairbody">
              <QR text={open.link} />
              <div className="pairinfo">
                <p className="small">{relay?.configured ? t("dev.scanRelay") : local ? t("dev.scanLocal") : t("dev.scanLan")}</p>
                <div className="paircodebig mono">{open.p.code}</div>
                <p className="small">
                  {t("dev.expires", { s: `${Math.floor(left / 60)}:${String(left % 60).padStart(2, "0")}` })}
                </p>
                <button
                  className="btn sm"
                  type="button"
                  onClick={() => {
                    void navigator.clipboard?.writeText(open.link).then(() => toast(t("dev.copied")));
                  }}
                >
                  <Copy size={13} /> {t("dev.copyLink")}
                </button>
                {local && !relay?.configured && <p className="small amt">{t("dev.localWarn")}</p>}
                <p className="small waitpair">
                  <RefreshCw size={12} className="spin" /> {t("dev.waiting")}
                </p>
              </div>
            </div>
          </div>
        </>
      )}
    </>
  );
}
