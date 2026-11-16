// Settings › 电脑操作: the master switch, the system permissions it needs,
// which agent may operate which app, the block list, and how to stop.
// Backed by features/computertools (GET /api/computer); without a computer
// provider (server, web, non-macOS) the API is absent and the page says so.

import { Hand, MonitorOff, ShieldCheck } from "lucide-react";
import { Fragment, useEffect, useMemo, useState } from "react";
import { API, ApiError, desktop, type ComputerOverview, type ComputerPerm, type DesktopPermission } from "../../api";
import { t, type Key } from "../../i18n";
import { toast, useStore } from "../../store";
import { Head, Row } from "../Settings";

const PERMS: ComputerPerm[] = ["allow", "ask", "block"];
const GROUPS = ["passwords", "terminal", "system", "crewbase", "finance"] as const;

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
        <AppTable ov={ov} run={run} />
        <BlockTable ov={ov} run={run} />

        <div className="vsec">{t("cu.safety")}</div>
        <div className="box cusafe">
          <ShieldCheck size={16} className="muted" />
          <ul>
            <li>{t("cu.safe1")}</li>
            <li>{t("cu.safe2")}</li>
            <li>{t("cu.safe3")}</li>
            <li>{t("cu.safe4")}</li>
            <li>{t("cu.safe5")}</li>
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
function AppTable({ ov, run }: { ov: ComputerOverview; run: (p: Promise<ComputerOverview>) => Promise<void> }) {
  const agents = useStore((s) => s.agents);
  const list = useMemo(() => Object.values(agents).sort((a, b) => a.name.localeCompare(b.name)), [agents]);
  const [agent, setAgent] = useState("");
  const cur = agent && agents[agent] ? agent : (list[0]?.id ?? "");
  const apps = ov.apps.filter((a) => !a.blocked && !a.browser);
  const browsers = ov.apps.filter((a) => a.browser && a.running);
  return (
    <>
      <div className="vsec">{t("cu.apps")}</div>
      <div className="sbox">
        <Row title={t("cu.appsFor")} desc={t("cu.appsDesc")}>
          <span className="ssel">
            <select value={cur} aria-label={t("cu.appsFor")} onChange={(e) => setAgent(e.target.value)}>
              {list.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.name}
                </option>
              ))}
            </select>
          </span>
        </Row>
        {apps.length === 0 && (
          <div className="srow">
            <span className="small">{ov.appsError ? t("cu.appsError", { error: ov.appsError }) : t("cu.appsNone")}</span>
          </div>
        )}
        {cur &&
          apps.map((a) => {
            const p = a.perms[cur] ?? "ask";
            return (
              <Row key={a.id} title={a.name} desc={a.id + (a.running ? "" : " · " + t("cu.notRunning"))}>
                <div className="seg" role="group" aria-label={a.name}>
                  {PERMS.map((x) => (
                    <button key={x} type="button" aria-pressed={p === x} onClick={() => p !== x && void run(API.setComputerPermission(cur, a.id, x))}>
                      {t(`cu.perm.${x}` as Key)}
                    </button>
                  ))}
                </div>
                <button className="linkbtn" type="button" title={t("cu.blockAllDesc")} onClick={() => void run(API.setComputerBlocked(a.id, true))}>
                  {t("cu.blockAll")}
                </button>
              </Row>
            );
          })}
      </div>
      {browsers.length > 0 && <p className="small cufoot">{t("cu.browsers", { apps: browsers.map((b) => b.name).join("、") })}</p>}
    </>
  );
}

/** The default block list (by group) and apps the person blocked. */
function BlockTable({ ov, run }: { ov: ComputerOverview; run: (p: Promise<ComputerOverview>) => Promise<void> }) {
  const [open, setOpen] = useState(false);
  const user = ov.blocks.user;
  return (
    <>
      <div className="vsec">{t("cu.blocked")}</div>
      <div className="sbox">
        {user.map((b) => (
          <Row key={b.key} title={b.name} desc={b.id + " · " + t("cu.blockedByYou")}>
            <button className="btn sm" type="button" onClick={() => void run(API.setComputerBlocked(b.key, false))}>
              {t("cu.unblock")}
            </button>
          </Row>
        ))}
        {GROUPS.map((g) => {
          const rows = ov.blocks.default.filter((b) => b.group === g);
          const on = rows.filter((b) => b.blocked);
          // collapsed: one line for what stays blocked, a row for each app let through
          const summary = !open && on.length > 0 && (
            <Row title={t(`cu.group.${g}` as Key)} desc={on.map((b) => b.name).join("、")}>
              <span className="pill sm">{t("cu.blockedDefault")}</span>
            </Row>
          );
          return (
            <Fragment key={g}>
              {summary}
              {(open ? rows : rows.filter((b) => !b.blocked)).map((b) => (
                <Row key={b.key} title={b.name} desc={t(`cu.group.${g}` as Key) + (b.id ? " · " + b.id : "")} dim={!b.blocked}>
                  <button
                    className="tg"
                    type="button"
                    role="switch"
                    aria-checked={b.blocked}
                    aria-label={t("cu.blockedToggle", { app: b.name })}
                    onClick={() => void run(API.setComputerBlocked(b.key, !b.blocked))}
                  />
                </Row>
              ))}
            </Fragment>
          );
        })}
      </div>
      <p className="small cufoot">
        {t("cu.blockedNote")}{" "}
        <button className="linkbtn" type="button" onClick={() => setOpen(!open)}>
          {open ? t("cu.blockedLess") : t("cu.blockedEdit")}
        </button>
      </p>
    </>
  );
}
