import { MessagesSquare, Moon } from "lucide-react";
import { useEffect, useState } from "react";
import { API, type ImGateway, type ImPlatform, type NotifyPrefs, type NotifyState, type QuietHours } from "../../api";
import { hhmm, nextMorning } from "../../format";
import { t, type Key } from "../../i18n";
import { dndActive, patchProfile, setState, toast, useStore } from "../../store";
import { Head, Row } from "../Settings";
import { imGatewayName } from "./Im";

const DEFAULT_QUIET: QuietHours = { on: false, start: "22:00", end: "08:00" };

const FIXED: Record<string, string> = { telegram: "Telegram", slack: "Slack", discord: "Discord", lark: "Lark" };

type ImInfo = { platforms: ImPlatform[]; gateways: ImGateway[] };

/** A column's name: 桌面, or the chat app of an IM gateway (with its group
 *  when that app has more than one). */
function channelName(ch: string, im?: ImInfo) {
  if (ch === "desktop") return t("nt.ch.desktop");
  const g = im?.gateways.find((x) => x.id === ch);
  if (!g) return ch === "feishu" ? t("imx.feishu") : (FIXED[ch] ?? ch); // gateways from the config file
  const name = imGatewayName(g, im!.platforms);
  return im!.gateways.filter((x) => imGatewayName(x, im!.platforms) === name).length > 1 ? `${name} · ${g.channel}` : name;
}

/** Settings › 通知: what to tell you, where, and when to stay quiet. */
export function Notify() {
  const [st, setSt] = useState<NotifyState | null>(null);
  const [prefs, setPrefs] = useState<NotifyPrefs>({ matrix: {}, quiet: DEFAULT_QUIET });
  const [testing, setTesting] = useState("");
  const [im, setIm] = useState<ImInfo>();
  const profile = useStore((s) => s.profile);
  const dnd = dndActive(profile);

  useEffect(() => {
    Promise.all([API.notify(), API.settings()])
      .then(([n, s]) => {
        setSt(n);
        setPrefs({ matrix: s.notify?.matrix ?? {}, quiet: s.notify?.quiet?.start ? s.notify.quiet : DEFAULT_QUIET });
      })
      .catch((e) => toast(t("nt.failed", { error: e?.message ?? String(e) })));
    API.imConfig().then(setIm, () => {});
  }, []);

  const save = async (p: NotifyPrefs) => {
    setPrefs(p);
    try {
      await API.patchSettings({ notify: p });
      setSt(await API.notify());
    } catch (e: any) {
      toast(t("nt.failed", { error: e?.message ?? String(e) }));
      setSt(await API.notify().catch(() => st));
    }
  };

  const toggle = (kind: string, ch: string) => {
    if (!st) return;
    const on = !st.matrix[kind]?.[ch];
    setSt({ ...st, matrix: { ...st.matrix, [kind]: { ...st.matrix[kind], [ch]: on } } });
    void save({ ...prefs, matrix: { ...prefs.matrix, [kind]: { ...(prefs.matrix?.[kind] ?? {}), [ch]: on } } });
  };

  const setQuiet = (q: Partial<QuietHours>) => void save({ ...prefs, quiet: { ...prefs.quiet, ...q } });

  const test = async (ch: string) => {
    setTesting(ch);
    try {
      await API.testNotify(ch);
      toast(t("nt.testSent", { channel: channelName(ch, im) }));
    } catch (e: any) {
      toast(t("nt.failed", { error: e?.message ?? String(e) }));
    } finally {
      setTesting("");
    }
  };

  const channels = st?.channels ?? [];

  return (
    <>
      <Head title={t("set.notify")} sub={t("nt.sub")} />
      <div className="vb">
        {(st?.quietNow || dnd) && (
          <div className="ntnow">
            <Moon size={15} />
            <span className="g">{dnd ? t("nt.dndNow", { time: hhmm(profile.dndUntil) }) : t("nt.quietNow", { end: prefs.quiet.end })}</span>
            {dnd && (
              <button className="btn sm" type="button" onClick={() => void patchProfile({ dndUntil: "" })}>
                {t("nt.dndEnd")}
              </button>
            )}
          </div>
        )}

        <div className="vsec">{t("nt.what")}</div>
        {!st ? (
          <div className="sbox">
            <div className="srow">
              <span className="small">{t("common.loading")}</span>
            </div>
          </div>
        ) : (
          <div className="sbox ntbl" style={{ ["--n" as any]: channels.length }}>
            <div className="ntr nth">
              <span className="g" />
              {channels.map((ch) => (
                <span key={ch} className="ntc">
                  <b>{channelName(ch, im)}</b>
                  <button type="button" className="linkbtn" disabled={testing !== ""} onClick={() => void test(ch)}>
                    {testing === ch ? t("nt.testing") : t("nt.test")}
                  </button>
                </span>
              ))}
            </div>
            {st.kinds.map((k) => (
              <div key={k} className="ntr">
                <span className="g">
                  <b>
                    {t(`nt.k.${k}` as Key)}
                    {k === "needs-you" && <span className="pill amber">{t("nt.urgent")}</span>}
                  </b>
                  <span>{t(`nt.d.${k}` as Key)}</span>
                </span>
                {channels.map((ch) => {
                  const on = !!st.matrix[k]?.[ch];
                  return (
                    <span key={ch} className="ntc">
                      <button
                        type="button"
                        className="tg"
                        role="switch"
                        aria-checked={on}
                        aria-label={`${t(`nt.k.${k}` as Key)} · ${channelName(ch, im)}`}
                        onClick={() => toggle(k, ch)}
                      />
                    </span>
                  );
                })}
              </div>
            ))}
          </div>
        )}
        <button type="button" className="ntim" onClick={() => setState({ settings: "im" })}>
          <MessagesSquare size={15} />
          <span className="g">
            <b>{t(im?.gateways.length ? "imx.ntHave" : "imx.ntNone")}</b>
            <span>{t("imx.ntDesc")}</span>
          </span>
          <span className="linkbtn">{t("imx.ntGo")}</span>
        </button>

        <div className="vsec">{t("nt.when")}</div>
        <div className="sbox">
          <Row title={t("nt.quiet")} desc={t("nt.quietDesc")}>
            <span className={"ntq" + (prefs.quiet.on ? "" : " off")}>
              <input type="time" className="fi sm" value={prefs.quiet.start} aria-label={t("nt.from")} onChange={(e) => e.target.value && setQuiet({ start: e.target.value })} />
              <span className="muted">–</span>
              <input type="time" className="fi sm" value={prefs.quiet.end} aria-label={t("nt.to")} onChange={(e) => e.target.value && setQuiet({ end: e.target.value })} />
            </span>
            <button type="button" className="tg" role="switch" aria-checked={prefs.quiet.on} aria-label={t("nt.quiet")} onClick={() => setQuiet({ on: !prefs.quiet.on })} />
          </Row>
          <Row title={t("nt.dnd")} desc={dnd ? t("nt.dndNow", { time: hhmm(profile.dndUntil) }) : t("nt.dndDesc")}>
            {dnd ? (
              <button className="btn sm" type="button" onClick={() => void patchProfile({ dndUntil: "" })}>
                {t("nt.dndEnd")}
              </button>
            ) : (
              <>
                <button className="btn sm" type="button" onClick={() => void patchProfile({ dndUntil: new Date(Date.now() + 3600_000).toISOString() })}>
                  {t("me.dnd1h")}
                </button>
                <button className="btn sm" type="button" onClick={() => void patchProfile({ dndUntil: nextMorning().toISOString() })}>
                  {t("me.dndMorning")}
                </button>
              </>
            )}
          </Row>
        </div>
        <p className="small ntfoot">{t("nt.foot")}</p>
      </div>
    </>
  );
}
