import { useEffect, useRef, useState } from "react";
import { Bot, Check, ChevronDown, ChevronRight, CircleDashed, X } from "lucide-react";
import type { SubRun } from "../projection";
import { t } from "../i18n";
import { openSubRun, useStore } from "../store";
import { Avatar } from "./Avatar";

/* 等待回复: above the composer while the agent waits for work it handed
 * out (delegate, in the foreground or background): one row per helper,
 * ticked off as each answers. It stays a moment after the last one, then
 * folds away. */

const STAY_MS = 4000;

function elapsed(from: string, to?: string) {
  const s = Math.max(0, Math.round(((to ? new Date(to).getTime() : Date.now()) - new Date(from).getTime()) / 1000));
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m${String(s % 60).padStart(2, "0")}s`;
}

export function WaitingFor({ runs }: { runs: SubRun[] }) {
  const agents = useStore((s) => s.agents);
  const [open, setOpen] = useState(true);
  const [, tick] = useState(0);
  const [hideAfter, setHideAfter] = useState<number | null>(null);
  const running = runs.filter((r) => !r.finished);
  // the current wave: every run started while another one was still going
  // (a new wave starts once all of the last one have answered)
  const waveIds = useRef<{ ids: Set<string>; closed: boolean }>({ ids: new Set(), closed: true });
  if (running.length) {
    if (waveIds.current.closed) waveIds.current = { ids: new Set(), closed: false };
    for (const r of running) waveIds.current.ids.add(r.session);
  } else waveIds.current.closed = true;
  const wave = runs.filter((r) => waveIds.current.ids.has(r.session));
  const done = wave.filter((r) => r.finished).length;

  useEffect(() => {
    if (!running.length) return;
    const id = setInterval(() => tick((n) => n + 1), 1000);
    return () => clearInterval(id);
  }, [running.length]);
  // when the last one answers: stay a moment, then go
  useEffect(() => {
    if (running.length || !wave.length) return;
    const last = Math.max(...wave.map((r) => new Date(r.finished!).getTime()));
    setHideAfter(last + STAY_MS);
    const id = setTimeout(() => tick((n) => n + 1), Math.max(0, last + STAY_MS - Date.now()) + 50);
    return () => clearTimeout(id);
  }, [running.length, wave.length]);
  if (!wave.length || (!running.length && hideAfter !== null && Date.now() > hideAfter)) return null;

  return (
    <div className={"waitfor" + (running.length ? "" : " done")} role="status">
      <button type="button" className="wfh" aria-expanded={open} onClick={() => setOpen(!open)}>
        {running.length ? <CircleDashed size={14} className="wfspin" /> : <Check size={14} className="wfok" />}
        <b>{running.length ? t("wf.waiting") : t("wf.allBack")}</b>
        <span className="sp" />
        <span className="wfn">
          {done}/{wave.length}
        </span>
        {open ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
      </button>
      {open && (
        <div className="wfl">
          {wave.map((r) => {
            const a = agents[r.child];
            const failed = !!r.finished && !!r.stop && r.stop !== "completed";
            return (
              <button key={r.session} type="button" className={"wfr" + (r.finished ? " fin" : "")} onClick={() => openSubRun(r.session)} title={r.task}>
                {a && !a.ephemeral ? <Avatar agent={a} size={18} /> : <span className="wfdot"><Bot size={12} /></span>}
                <span className="wfname">{a && !a.ephemeral ? a.name : t("wf.helper")}</span>
                <span className="wftask">{r.task}</span>
                <span className="wfst">
                  {!r.finished ? (
                    <>
                      {t("wf.working")} · {elapsed(r.started)} <i className="wfring" />
                    </>
                  ) : failed ? (
                    <>
                      <X size={12} className="wfbad" /> {t("wf.stopped")}
                    </>
                  ) : (
                    <>
                      <Check size={12} className="wfok" /> {elapsed(r.started, r.finished)}
                    </>
                  )}
                </span>
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}
