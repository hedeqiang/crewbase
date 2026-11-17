// Settings › 电脑操作: the master switch, the system permissions it needs,
// which agent may operate which app, the block list, and how to stop.
// Backed by features/computertools (GET /api/computer); without a computer
// provider (server, web, non-macOS) the API is absent and the page says so.

import { Hand, MonitorOff, ShieldCheck } from "lucide-react";
import { useEffect, useState } from "react";
import { API, ApiError, desktop, type ComputerOverview, type DesktopPermission } from "../../api";
import { t, type Key } from "../../i18n";
import { toast, useStore } from "../../store";
import { Head, Row } from "../Settings";


const fail = (e: any) => toast(e?.message ?? String(e));

export function Computer() {
  const [ov, setOv] = useState<ComputerOverview | null>(null);
  const [none, setNone] = useState(false);
  const live = useStore((s) => s.computer);
  const load = () =>
    API.computer().then(
      (o) => {
        setOv(o);
        setNone(false);
      },
      (e) => {
        if (e instanceof ApiError && e.status === 404) setNone(true);
        else fail(e);
      },
    );
  useEffect(() => {
    void load();
  }, []);
  const run = async (p: Promise<ComputerOverview>) => {
    try {
      setOv(await p);
    } catch (e) {
      fail(e);
    }
  };

  if (none)
    return (
      <>
        <Head title={t("set.computer")} sub={t("cu.sub")} />
        <div className="vb">
          <div className="soon">
            <div className="chico">
              <MonitorOff size={18} />
            </div>
            <b>{t("cu.unavailable")}</b>
            <span className="small">{t("cu.unavailableDesc")}</span>
          </div>
        </div>
      </>
    );
  if (!ov)
    return (
      <>
        <Head title={t("set.computer")} sub={t("cu.sub")} />
        <div className="vb">
          <p className="small">{t("common.loading")}</p>
        </div>
      </>
    );
  const state = live ?? ov.state;
  return (
    <>
      <Head title={t("set.computer")} sub={t("cu.sub")} />
      <div className="vb">
        <div className="sbox">
          <Row title={t("cu.switch")} desc={t(ov.enabled ? "cu.switchOn" : "cu.switchOff")}>
            <button className="tg" type="button" role="switch" aria-checked={ov.enabled} aria-label={t("cu.switch")} onClick={() => void run(API.setComputer(!ov.enabled))} />
          </Row>
          {ov.enabled && <LiveRows state={state} />}
        </div>

        <SysPerms ov={ov} />
        {/* no app table or block list to fill in: each agent is asked the
            first time it uses an app (以后都允许 remembers it, revocable in
            Settings › 审批); a few kinds of app are never operated (safety) */}
        <p className="small muted cufoot">{t("cu.askNote")}</p>

        <div className="vsec">{t("cu.safety")}</div>
        <div className="box cusafe">
          <ShieldCheck size={16} className="muted" />
          <ul>
            <li>{t("cu.safe1")}</li>
            <li>{t("cu.safe2")}</li>
            <li>{t("cu.safe3")}</li>
            <li>{t("cu.safe4")}</li>
            <li>{t("cu.safe5")}</li>
            <li>{t("cu.safe6")}</li>
          </ul>
        </div>
      </div>
    </>
  );
}

/** Who operates what now, and stop / resume. */
function LiveRows({ state }: { state: ComputerOverview["state"] }) {
  const agents = useStore((s) => s.agents);
  const name = (id: string) => agents[id]?.name ?? id;
  const act = (p: Promise<unknown>) => void p.catch(fail);
  const stopped = state.stoppedAll || state.stopped.length > 0;
  return (
    <>
      {state.live.map((l) => (
        <Row
          key={l.agent}
          title={t("cu.liveRow", { agent: name(l.agent), app: l.appName || l.app })}
          desc={l.paused ? t("cu.paused") : l.foreground ? t("cu.foreground") : t("cu.background")}
        >
          <button className="btn sm" type="button" onClick={() => act(API.stopComputer(l.agent))}>
            <Hand size={13} />
            {t("cu.stop")}
          </button>
        </Row>
      ))}
      <Row
        title={stopped ? t("cu.stopped") : t("cu.stopAll")}
        desc={stopped ? (state.stoppedAll ? t("cu.stoppedAllDesc") : t("cu.stoppedDesc", { agents: state.stopped.map(name).join("、") })) : t("cu.stopAllDesc")}
      >
        {stopped ? (
          <button className="btn sm" type="button" onClick={() => act(API.resumeComputer())}>
            {t("cu.resume")}
          </button>
        ) : (
          <button className="btn sm" type="button" onClick={() => act(API.stopComputer())}>
            {t("cu.stopAll")}
          </button>
        )}
      </Row>
    </>
  );
}

/** 辅助功能 / 屏幕录制: the shell's statuses when it reports them, else
 *  what the provider sees. */
function SysPerms({ ov }: { ov: ComputerOverview }) {
  const d = desktop();
  const [rows, setRows] = useState<DesktopPermission[] | null>(null);
  const load = () => d?.permissions?.().then(setRows, () => setRows(null));
  useEffect(() => {
    void load();
    const back = () => document.visibilityState === "visible" && void load();
    window.addEventListener("focus", back);
    return () => window.removeEventListener("focus", back);
  }, []);
  const ids = ["accessibility", "screen-recording"] as const;
  const granted = (id: (typeof ids)[number]) => {
    const r = rows?.find((x) => x.id === id);
    if (r) return r.status === "granted";
    return id === "accessibility" ? ov.permissions.accessibility : ov.permissions.screenRecording;
  };
  const ask = async (id: string) => {
    const r = rows?.find((x) => x.id === id);
    try {
      if (r?.canRequest && d?.requestPermission) setRows(await d.requestPermission(id));
      else d?.openPermissionSettings?.(id);
    } catch (e) {
      fail(e);
    }
  };
  return (
    <>
      <div className="vsec">{t("perm.title")}</div>
      <div className="sbox">
        {ids.map((id) => {
          const ok = granted(id);
          return (
            <Row key={id} title={t(`perm.${id}` as Key)} desc={t(`perm.${id}.desc` as Key)}>
              <span className="permr">
                <span className={"pill sm " + (ok ? "ok" : "red")}>{t(ok ? "perm.st.granted" : "perm.st.denied")}</span>
                {!ok && (d?.requestPermission || d?.openPermissionSettings) && (
                  <button className="btn sm" type="button" onClick={() => void ask(id)}>
                    {t("cu.grant")}
                  </button>
                )}
              </span>
            </Row>
          );
        })}
      </div>
    </>
  );
}

/** One agent at a time: each app with allow / ask / block. */
