import { useEffect, useMemo, useState, type ReactNode } from "react";
import {
  AlertTriangle,
  ArrowUpRight,
  ChevronDown,
  ChevronRight,
  CircleCheck,
  FileWarning,
  GitBranch,
  GitCommitHorizontal,
  GitMerge,
  GitPullRequest,
  MessageSquare,
  Moon,
  RefreshCw,
  ShieldCheck,
  ShieldQuestion,
  Sparkles,
  Upload,
  Webhook,
  XCircle,
} from "lucide-react";
import { API, type AgentView, type NsChange, type NsConfig, type NsEvent, type NsHandled, type NsProblem, type NsReport, type NsWaiting, type NightShiftView } from "../api";
import { t, type Key } from "../i18n";
import { hhmm } from "../format";
import { openSession, refreshNightShift, setState, toast, useStore } from "../store";
import { Avatar } from "./Avatar";
import { EmptyState } from "./EmptyState";
import { Skeleton } from "./Skeleton";
import { AgentSelect } from "./settings/Automation";
import { confirmDialog } from "./Confirm";

/* 夜间值班: an agent on duty while the person is away (features/nightshift).
 * The set-up lives in 设置 › 自动化; while on duty a moon marks the agent in
 * the list and a quiet bar sits on top of its conversation; when the hours
 * are over the report takes that place until it is read. Everything here
 * comes from what happened: there is no preview. */

function errMsg(e: any) {
  return e?.message ?? String(e);
}

/** The time zone the person's hours are read in (this browser's). */
function browserTZ(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "";
  } catch {
    return "";
  }
}

/** "11月26日 22:00" or "今天 22:00"-less: the date and time of iso. */
function when(iso: string): string {
  const d = new Date(iso);
  return t("time.date", { m: d.getMonth() + 1, d: d.getDate(), time: hhmm(iso) });
}

/** The span of a report: 11月26日 22:00 – 08:00. */
export function shiftSpan(from: string, to: string): string {
  const a = new Date(from);
  const b = new Date(to);
  return a.toDateString() === b.toDateString() || b.getTime() - a.getTime() < 86400000 ? `${when(from)} – ${hhmm(to)}` : `${when(from)} – ${when(to)}`;
}

/** The shift on duty for an agent: until when (null when off duty). */
export function dutyOf(ns: NightShiftView | null, agentId: string | undefined): { until: string } | null {
  if (!agentId || !ns?.status.onDuty || ns.status.agent !== agentId || !ns.status.until) return null;
  return { until: ns.status.until };
}

export function useOnDuty(agentId: string | undefined): { until: string } | null {
  return dutyOf(useStore((s) => s.nightShift), agentId);
}

/** 值班中 · 到 08:00 */
export function dutyLine(until: string) {
  return t("ns.onDutyUntil", { time: hhmm(until) });
}

/** The moon on an agent's row in the list while it is on duty. */
export function NightMoon({ agent }: { agent: AgentView }) {
  const duty = useOnDuty(agent.id);
  if (!duty) return null;
  return (
    <span className="nsmoon" title={dutyLine(duty.until)} aria-label={dutyLine(duty.until)}>
      <Moon size={11} strokeWidth={2.4} />
    </span>
  );
}

async function endShift() {
  if (!(await confirmDialog(t("ns.endConfirm")))) return;
  try {
    setState({ nightShift: await API.endNightShift() });
    toast(t("ns.ended"));
  } catch (e) {
    toast(errMsg(e));
  }
}

// ---------------------------------------------------------------- report

const KIND_ICON: Record<string, ReactNode> = {
  ci: <XCircle size={14} />,
  issue: <FileWarning size={14} />,
  comment: <MessageSquare size={14} />,
  review: <MessageSquare size={14} />,
  pr: <GitPullRequest size={14} />,
  custom: <Sparkles size={14} />,
  event: <Webhook size={14} />,
};

/** What came in, in one line: CI 失败 · #12 Faster cache */
function eventLine(e: NsEvent): string {
  const k = `ns.kind.${e.kind}` as Key;
  const kind = t(k) === k ? t("ns.kind.event") : t(k);
  const num = e.num ? `#${e.num} ` : "";
  return `${kind} · ${num}${e.title}`;
}

function eventMeta(e: NsEvent): string {
  const bits: string[] = [];
  if (e.detail) bits.push(e.detail);
  if (e.who) bits.push(e.who);
  bits.push(t(e.source === "webhook" ? "ns.via.webhook" : e.source === "start" ? "ns.via.start" : "ns.via.poll"));
  bits.push(hhmm(e.at));
  return bits.join(" · ");
}

const RUN_TONE: Record<string, string> = { running: "blue", quiet: "", attention: "ok", failed: "red" };

function RunState({ h, work }: { h: NsHandled; work: string }) {
  // a run still open while its conversation asks the person: it waits for them
  const asks = useStore((s) => !!work && s.approvals.some((a) => a.session === work));
  if (!h.status) return <span className="pill sm">{t("ns.run.none")}</span>;
  if (h.status === "running" && asks) return <span className="pill sm amber">{t("ns.run.open")}</span>;
  const k = `ns.run.${h.status}` as Key;
  return <span className={"pill sm " + (RUN_TONE[h.status] ?? "")}>{t(k) === k ? h.status : t(k)}</span>;
}

function HandledRow({ h, work }: { h: NsHandled; work: string }) {
  const [open, setOpen] = useState(false);
  const evs = h.events ?? [];
  const first = evs[0];
  return (
    <li className="nsr-item">
      <span className={"nsr-ic k-" + (first?.kind ?? "run")}>{first ? KIND_ICON[first.kind] ?? KIND_ICON.event : <RefreshCw size={14} />}</span>
      <div className="nsr-g">
        {evs.length === 0 && <b>{t("ns.run.manual")}</b>}
        {evs.map((e, i) => (
          <div key={i} className="nsr-ev">
            <b>
              {e.url ? (
                <a href={e.url} target="_blank" rel="noreferrer">
                  {eventLine(e)}
                  <ArrowUpRight size={12} className="nsr-out" />
                </a>
              ) : (
                eventLine(e)
              )}
            </b>
            <span className="nsr-meta">{eventMeta(e)}</span>
          </div>
        ))}
        {h.summary && (
          <button type="button" className={"nsr-sum" + (open ? " open" : "")} aria-expanded={open} onClick={() => setOpen(!open)}>
            {h.summary}
          </button>
        )}
      </div>
      <div className="nsr-c">
        <RunState h={h} work={work} />
        {work && (
          <button type="button" className="btn sm ghost" onClick={() => openSession(work)}>
            {t("appr.open")}
          </button>
        )}
      </div>
    </li>
  );
}

const CHANGE_ICON: Record<string, ReactNode> = {
  commit: <GitCommitHorizontal size={14} />,
  push: <Upload size={14} />,
  pr: <GitPullRequest size={14} />,
  merge: <GitMerge size={14} />,
  branch: <GitBranch size={14} />,
};

function changeLine(c: NsChange): string {
  const k = `ns.change.${c.kind}` as Key;
  const label = t(k) === k ? c.kind : t(k);
  return [label, c.ref, c.kind === "push" ? "" : c.title].filter(Boolean).join(" · ");
}

function ChangeRow({ c, work }: { c: NsChange; work: string }) {
  const sid = c.session || work;
  return (
    <li className={"nsr-item" + (c.failed ? " bad" : "")}>
      <span className="nsr-ic">{CHANGE_ICON[c.kind] ?? <GitCommitHorizontal size={14} />}</span>
      <div className="nsr-g">
        <b className="mono-ish">{c.url ? <a href={c.url} target="_blank" rel="noreferrer">{changeLine(c)}<ArrowUpRight size={12} className="nsr-out" /></a> : changeLine(c)}</b>
        <span className="nsr-meta">{[c.kind === "push" ? c.title : c.branch, c.failed ? t("ns.change.failed") : "", hhmm(c.at)].filter(Boolean).join(" · ")}</span>
      </div>
      <div className="nsr-c">
        {sid && (
          <button type="button" className="btn sm ghost" onClick={() => openSession(sid)}>
            {t("appr.open")}
          </button>
        )}
      </div>
    </li>
  );
}

function ProblemRow({ p, work }: { p: NsProblem; work: string }) {
  const k = `ns.fail.${p.what}` as Key;
  const sid = p.session || (p.what === "run" ? work : "");
  return (
    <li className="nsr-item bad">
      <span className="nsr-ic">
        <AlertTriangle size={14} />
      </span>
      <div className="nsr-g">
        <b>
          {t(k) === k ? p.what : t(k)}
          {p.count && p.count > 1 ? ` ×${p.count}` : ""}
        </b>
        {p.detail && <span className="nsr-meta wrap">{p.detail}</span>}
      </div>
      <div className="nsr-c">
        {sid && (
          <button type="button" className="btn sm ghost" onClick={() => openSession(sid)}>
            {t("appr.open")}
          </button>
        )}
      </div>
    </li>
  );
}

function WaitingRow({ w }: { w: NsWaiting }) {
  const still = useStore((s) => s.approvals.some((a) => a.callId === w.callId));
  return (
    <li className={"nsr-item" + (still ? " wait" : " done")}>
      <span className="nsr-ic">{still ? <ShieldQuestion size={14} /> : <CircleCheck size={14} />}</span>
      <div className="nsr-g">
        <b>{w.summary || w.tool}</b>
        <span className="nsr-meta">{[t(`cat.${w.category}` as Key) === `cat.${w.category}` ? w.category : t(`cat.${w.category}` as Key), when(w.asked)].join(" · ")}</span>
      </div>
      <div className="nsr-c">
        {still ? (
          <button type="button" className="btn sm" onClick={() => openSession(w.session)}>
            {t("ns.decide")}
          </button>
        ) : (
          <span className="pill sm">{t("ns.decided")}</span>
        )}
      </div>
    </li>
  );
}

function Section({ title, n, children }: { title: string; n: number; children: ReactNode }) {
  if (n === 0) return null;
  return (
    <section className="nsr-sec">
      <h4>
        {title}
        <span className="nsr-n">{n}</span>
      </h4>
      <ul>{children}</ul>
    </section>
  );
}

/** The report itself: what it did, what it changed, what failed, what waits. */
export function NightReportBody({ r }: { r: NsReport }) {
  const empty = r.handled.length + r.changes.length + r.failed.length + r.waiting.length === 0;
  return (
    <div className="nsr">
      {empty && (
        <p className="nsr-quiet">
          <Moon size={14} />
          {r.polls > 0 ? t("ns.quietPolled", { n: r.polls }) : t("ns.quiet")}
        </p>
      )}
      <Section title={t("ns.sec.waiting")} n={r.waiting.length}>
        {r.waiting.map((w) => (
          <WaitingRow key={w.callId} w={w} />
        ))}
      </Section>
      <Section title={t("ns.sec.handled")} n={r.handled.length}>
        {r.handled.map((h, i) => (
          <HandledRow key={h.run + i} h={h} work={r.work} />
        ))}
      </Section>
      <Section title={t("ns.sec.changes")} n={r.changes.length}>
        {r.changes.map((c, i) => (
          <ChangeRow key={i} c={c} work={r.work} />
        ))}
      </Section>
      <Section title={t("ns.sec.failed")} n={r.failed.length}>
        {r.failed.map((p, i) => (
          <ProblemRow key={i} p={p} work={r.work} />
        ))}
      </Section>
    </div>
  );
}

/** The four counts of a report, as quiet chips. */
function Counts({ r }: { r: NsReport }) {
  const pending = useStore((s) => r.waiting.filter((w) => s.approvals.some((a) => a.callId === w.callId)).length);
  return (
    <span className="nscounts">
      <span>{t("ns.c.handled", { n: r.handled.length })}</span>
      <span>{t("ns.c.changes", { n: r.changes.length })}</span>
      {r.failed.length > 0 && <span className="bad">{t("ns.c.failed", { n: r.failed.length })}</span>}
      {pending > 0 && <span className="nswait">{t("ns.c.waiting", { n: pending })}</span>}
    </span>
  );
}

// ---------------------------------------------------------------- conversation

/** On top of the agent's conversation: while on duty a quiet bar (with 提前结束),
 *  after the shift its report until it is read. */
export function NightShiftBar({ agent }: { agent: AgentView }) {
  const ns = useStore((s) => s.nightShift);
  const [open, setOpen] = useState(false);
  const duty = useOnDuty(agent.id);
  const report = useMemo(() => ns?.reports.find((r) => r.agent === agent.id && !r.seen), [ns, agent.id]);
  if (duty && ns) {
    const evs = ns.shift?.events ?? [];
    return (
      <div className="nsbar on" role="status">
        <button type="button" className="nsbar-h" aria-expanded={open} onClick={() => setOpen(!open)} disabled={evs.length === 0}>
          <span className="nsbar-moon">
            <Moon size={14} />
          </span>
          <b>{dutyLine(duty.until)}</b>
          <span className="nsbar-m">
            {t("ns.c.handled", { n: ns.status.handled })}
            {ns.status.waiting > 0 && <> · <span className="nswait">{t("ns.c.waiting", { n: ns.status.waiting })}</span></>}
          </span>
          {evs.length > 0 && (open ? <ChevronDown size={14} className="nsbar-chv" /> : <ChevronRight size={14} className="nsbar-chv" />)}
        </button>
        {ns.status.session && (
          <button type="button" className="btn sm ghost" onClick={() => openSession(ns.status.session!)}>
            {t("ns.openWork")}
          </button>
        )}
        <button type="button" className="btn sm" onClick={() => void endShift()}>
          {t("ns.end")}
        </button>
        {open && evs.length > 0 && (
          <ul className="nsbar-l">
            {evs
              .slice()
              .reverse()
              .map((e, i) => (
                <li key={i}>
                  <span className={"nsr-ic k-" + e.kind}>{KIND_ICON[e.kind] ?? KIND_ICON.event}</span>
                  <span className="g">{eventLine(e)}</span>
                  <span className="tm">{hhmm(e.at)}</span>
                </li>
              ))}
          </ul>
        )}
      </div>
    );
  }
  if (!report) return null;
  const seen = async () => {
    try {
      await API.seenNightReport(report.id);
    } catch {
      /* the card stays */
    }
    void refreshNightShift();
  };
  return (
    <div className="nsbar report">
      <div className="nsbar-top">
        <span className="nsbar-moon">
          <Moon size={14} />
        </span>
        <div className="nsbar-t">
          <b>{t("ns.reportTitle")}</b>
          <span>
            {shiftSpan(report.start, report.end)}
            {report.early ? ` · ${t("ns.early")}` : ""}
          </span>
        </div>
        <button type="button" className="btn sm ghost" aria-expanded={open} onClick={() => setOpen(!open)}>
          {open ? <ChevronDown size={13} /> : <ChevronRight size={13} />}
          {open ? t("ns.fold") : t("ns.details")}
        </button>
        <button type="button" className="btn sm" onClick={() => void seen()}>
          {t("ns.gotIt")}
        </button>
      </div>
      <Counts r={report} />
      {open && <NightReportBody r={report} />}
    </div>
  );
}

// ---------------------------------------------------------------- settings

const EVERY = [5, 10, 30];
const HOURS: [string, string][] = [
  ["22:00", "08:00"],
  ["23:00", "07:00"],
  ["19:00", "09:00"],
];

function Row({ title, desc, children, dim }: { title: string; desc?: ReactNode; children?: ReactNode; dim?: boolean }) {
  return (
    <div className={"srow" + (dim ? " dim" : "")}>
      <span className="g">
        <b>{title}</b>
        {desc && <span>{desc}</span>}
      </span>
      {children && <span className="c">{children}</span>}
    </div>
  );
}

function sameConfig(a: NsConfig, b: NsConfig) {
  return (
    a.enabled === b.enabled &&
    a.agent === b.agent &&
    a.start === b.start &&
    a.end === b.end &&
    a.every === b.every &&
    a.watch.ci === b.watch.ci &&
    a.watch.github === b.watch.github &&
    a.watch.custom === b.watch.custom &&
    (a.watch.instruction ?? "") === (b.watch.instruction ?? "")
  );
}

/** 设置 › 自动化 › 夜间值班 */
export function NightShiftSettings() {
  const ns = useStore((s) => s.nightShift);
  const agents = useStore((s) => s.agents);
  const [f, setF] = useState<NsConfig | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  useEffect(() => {
    void refreshNightShift();
  }, []);
  useEffect(() => {
    if (ns && !f) setF(ns.config);
  }, [ns, f]);
  if (!ns || !f) return <div className="vb"><Skeleton kind="rows" n={5} /></div>;

  const st = ns.status;
  const a = agents[f.agent];
  const dirty = !sameConfig(f, ns.config);
  const set = (p: Partial<NsConfig>) => setF({ ...f, ...p });
  const watch = (p: Partial<NsConfig["watch"]>) => setF({ ...f, watch: { ...f.watch, ...p } });
  const save = async (next: NsConfig) => {
    setBusy(true);
    setErr(null);
    try {
      const v: NightShiftView = await API.saveNightShift({ ...next, tz: browserTZ() || next.tz });
      setState({ nightShift: v });
      setF(v.config);
      toast(t("panel.saved"));
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  };
  const toggle = () => {
    const next = { ...f, enabled: !f.enabled };
    if (!next.agent) {
      setErr(t("ns.needAgent"));
      return;
    }
    void save(next);
  };
  const tz = browserTZ();
  // where issues and comments come from: a GitHub webhook when there is one
  const hooks = (ns.config.endpoints?.length ?? 0) > 0 && f.agent === ns.config.agent ? st.endpoints ?? [] : st.hooks ?? [];
  const viaHook = hooks.length > 0;
  const ghProblem = a && st.agent === f.agent && (!st.repo ? t("ns.noRepo") : st.gh && st.gh !== "ok" ? t(st.gh === "missing" ? "ns.ghMissing" : "ns.ghLogin") : "");

  let line: ReactNode;
  if (st.onDuty && st.until) line = <>{t("ns.st.on", { name: agents[st.agent ?? ""]?.name ?? "", time: hhmm(st.until) })}</>;
  else if (ns.config.enabled && st.endedEarly && st.next) line = t("ns.st.endedEarly", { time: when(st.next) });
  else if (ns.config.enabled && st.next) line = t("ns.st.next", { name: agents[ns.config.agent]?.name ?? "", time: when(st.next) });
  else line = t("ns.st.off");

  return (
    <div className="vb nsset">
      <div className={"nshero" + (st.onDuty ? " on" : ns.config.enabled ? " set" : "")}>
        <span className="nshero-ic">
          <Moon size={20} />
        </span>
        <div className="nshero-t">
          <b>{t("ns.title")}</b>
          <span>{line}</span>
          {st.onDuty && (
            <span className="nshero-m">
              {t("ns.c.handled", { n: st.handled })} · {t("ns.c.changes", { n: st.changes })}
              {st.waiting > 0 && (
                <>
                  {" · "}
                  <span className="nswait">{t("ns.c.waiting", { n: st.waiting })}</span>
                </>
              )}
            </span>
          )}
        </div>
        <div className="nshero-c">
          {st.onDuty && (
            <>
              {st.session && (
                <button type="button" className="btn sm ghost" onClick={() => openSession(st.session!)}>
                  {t("ns.openWork")}
                </button>
              )}
              <button
                type="button"
                className="btn sm ghost"
                onClick={async () => {
                  try {
                    await API.pollNightShift();
                    toast(t("ns.polling"));
                  } catch (e) {
                    toast(errMsg(e));
                  }
                }}
              >
                <RefreshCw size={12} />
                {t("ns.pollNow")}
              </button>
              <button type="button" className="btn sm" onClick={() => void endShift()}>
                {t("ns.end")}
              </button>
            </>
          )}
          <button type="button" className="tg" role="switch" aria-checked={ns.config.enabled} aria-label={t("ns.enable")} disabled={busy} onClick={toggle} />
        </div>
      </div>
      {st.onDuty && st.problem && (
        <p className="nsnote warn">
          <AlertTriangle size={13} />
          {t(`ns.fail.${st.problem}` as Key)}
        </p>
      )}

      <div className="vsec">{t("ns.sec.when")}</div>
      <div className="sbox">
        <Row title={t("ns.hours")} desc={tz ? t("ns.hoursTZ", { tz }) : t("ns.hoursLocal")}>
          <span className="nshours">
            <input className="fi sm" type="time" aria-label={t("ns.from")} value={f.start} onChange={(e) => set({ start: e.target.value })} />
            <span aria-hidden>–</span>
            <input className="fi sm" type="time" aria-label={t("ns.to")} value={f.end} onChange={(e) => set({ end: e.target.value })} />
          </span>
        </Row>
        <div className="srow nspresets">
          {HOURS.map(([s, e]) => (
            <button key={s} type="button" className="chip" aria-pressed={f.start === s && f.end === e} onClick={() => set({ start: s, end: e })}>
              {s}–{e}
            </button>
          ))}
        </div>
        <Row
          title={t("ns.agent")}
          desc={
            a ? (
              <>
                {st.folder && st.agent === f.agent ? st.folder : a.folder || t("ns.agentHint")}
                {st.repo && st.agent === f.agent ? ` · GitHub ${st.repo}` : ""}
              </>
            ) : (
              t("ns.agentHint")
            )
          }
        >
          {a && <Avatar agent={a} size={22} />}
          <AgentSelect value={f.agent} onChange={(v) => set({ agent: v })} />
        </Row>
      </div>
      {ghProblem && (f.watch.ci || (f.watch.github && !viaHook)) && (
        <p className="nsnote warn">
          <AlertTriangle size={13} />
          {ghProblem}
        </p>
      )}

      <div className="vsec">{t("ns.sec.watch")}</div>
      <div className="sbox">
        <Row title={t("ns.w.ci")} desc={t("ns.w.ciDesc")}>
          <button type="button" className="tg" role="switch" aria-checked={f.watch.ci} aria-label={t("ns.w.ci")} onClick={() => watch({ ci: !f.watch.ci })} />
        </Row>
        <Row
          title={t("ns.w.github")}
          desc={
            viaHook ? (
              <span className="nsvia">
                <Webhook size={12} />
                {t("ns.w.viaHook", { name: hooks.join("、") })}
              </span>
            ) : (
              t("ns.w.viaPoll", { n: f.every })
            )
          }
        >
          <button type="button" className="tg" role="switch" aria-checked={f.watch.github} aria-label={t("ns.w.github")} onClick={() => watch({ github: !f.watch.github })} />
        </Row>
        {(f.watch.ci || (f.watch.github && !viaHook)) && (
          <Row title={t("ns.every")} desc={t("ns.everyDesc")}>
            <div className="seg">
              {EVERY.map((n) => (
                <button key={n} type="button" aria-pressed={f.every === n} onClick={() => set({ every: n })}>
                  {t("ns.min", { n })}
                </button>
              ))}
            </div>
          </Row>
        )}
        <Row title={t("ns.w.custom")} desc={t("ns.w.customDesc")}>
          <button type="button" className="tg" role="switch" aria-checked={f.watch.custom} aria-label={t("ns.w.custom")} onClick={() => watch({ custom: !f.watch.custom })} />
        </Row>
        {f.watch.custom && (
          <div className="srow nscustom">
            <textarea className="fi sm" rows={3} value={f.watch.instruction ?? ""} placeholder={t("ns.w.customPh")} onChange={(e) => watch({ instruction: e.target.value })} />
          </div>
        )}
      </div>
      <p className="nsnote">
        <ShieldCheck size={13} />
        {t("ns.safety")}
      </p>
      {err && <div className="ferr">{err}</div>}
      {(dirty || !ns.config.updated) && (
        <div className="acts nsacts">
          <button type="button" className="btn pri" disabled={busy || !f.agent} onClick={() => void save(ns.config.updated ? f : { ...f, enabled: true })}>
            {ns.config.updated ? t("common.save") : t("ns.start")}
          </button>
          {dirty && ns.config.updated && (
            <button type="button" className="btn ghost" onClick={() => setF(ns.config)}>
              {t("common.cancel")}
            </button>
          )}
        </div>
      )}

      <div className="vsec">{t("ns.history")}</div>
      {ns.reports.length === 0 ? (
        <EmptyState icon={<Moon size={22} />} title={t("ns.noReports")} desc={t("ns.noReportsDesc")} />
      ) : (
        <div className="sbox nshist">
          {ns.reports.map((r) => {
            const on = open === r.id;
            return (
              <div key={r.id} className={"nshrow" + (on ? " open" : "")}>
                <button type="button" className="nshrow-h" aria-expanded={on} onClick={() => setOpen(on ? null : r.id)}>
                  {on ? <ChevronDown size={14} className="chv" /> : <ChevronRight size={14} className="chv" />}
                  <span className="nshrow-t">
                    <b>{shiftSpan(r.start, r.end)}</b>
                    <span className="nshrow-who">
                      {agents[r.agent] && <Avatar agent={agents[r.agent]} size={16} />}
                      {agents[r.agent]?.name ?? r.name}
                      {r.early ? ` · ${t("ns.early")}` : ""}
                    </span>
                  </span>
                  <Counts r={r} />
                </button>
                {on && <NightReportBody r={r} />}
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}
