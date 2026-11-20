import { Skeleton } from "./Skeleton";
import { useEffect, useState, useSyncExternalStore } from "react";
import { ChevronDown, ChevronRight, FileDiff as FileDiffIcon, Undo2 } from "lucide-react";
import { API, type FileDiff, type TurnChanges, type TurnSummary } from "../api";
import { t } from "../i18n";
import { toast } from "../store";
import { confirmDialog } from "./Confirm";
import { UnifiedDiff } from "./Files";

/* 这一轮改了哪些文件: under a turn's last reply, the files the turn changed
 * in the agent's workspace (features/console/snapshots.go), each with its
 * diff, and 撤销这一轮 (the files back as they were before it). */

// per agent: the turns that changed files (one request for a conversation)
type Entry = { at: number; list: TurnSummary[]; inflight?: Promise<void> };
const cache = new Map<string, Entry>();
const subs = new Set<() => void>();
let version = 0;
const bump = () => {
  version++;
  subs.forEach((f) => f());
};

function load(agent: string): Promise<void> {
  const e = cache.get(agent);
  if (e?.inflight) return e.inflight;
  const p = API.turnSummaries(agent)
    .then((list) => void cache.set(agent, { at: Date.now(), list }))
    .catch(() => void cache.set(agent, { at: Date.now(), list: e?.list ?? [] }))
    .finally(bump);
  cache.set(agent, { at: e?.at ?? 0, list: e?.list ?? [], inflight: p });
  return p;
}

/** forget what an agent's turns changed (after an undo) */
export function refreshTurnFiles(agent: string) {
  cache.delete(agent);
  bump();
}

function useSummary(agent: string, turn: string, ended: string): TurnSummary | undefined {
  useSyncExternalStore(
    (f) => (subs.add(f), () => subs.delete(f)),
    () => version,
  );
  const e = cache.get(agent);
  const s = e?.list.find((x) => x.turn === turn);
  useEffect(() => {
    if (s) return;
    const endedAt = new Date(ended).getTime();
    // never asked, or asked before this turn ended
    if (!e || (!e.inflight && e.at < endedAt + 500)) {
      // the snapshot's end is written a moment after the turn's record
      const wait = Date.now() - endedAt < 3000 ? 1200 : 0;
      const id = setTimeout(() => void load(agent), wait);
      return () => clearTimeout(id);
    }
  }, [agent, turn, ended, s, e]);
  return s;
}

export function TurnFiles({ agent, turn, ended }: { agent: string; turn: string; ended: string }) {
  const sum = useSummary(agent, turn, ended);
  const [open, setOpen] = useState(false);
  const [tc, setTc] = useState<TurnChanges | null>(null);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (!open || tc) return;
    API.turnChanges(agent, turn).then(setTc, (e) => setErr(e?.message ?? String(e)));
  }, [open, tc, agent, turn]);
  if (!sum) return null;

  const undo = async () => {
    const cur = tc ?? (await API.turnChanges(agent, turn).catch(() => null));
    if (!cur) return;
    const text = cur.later?.length ? t("tf.undoLater", { n: cur.files.length, later: cur.later.length }) : t("tf.undoQ", { n: cur.files.length });
    if (!(await confirmDialog({ text, ok: t("tf.undo"), danger: true }))) return;
    setBusy(true);
    try {
      const r = await API.restoreTurn(agent, turn);
      toast(t("tf.undone", { n: r.restored }));
      setTc(null);
      refreshTurnFiles(agent);
    } catch (e: any) {
      toast(e?.message ?? String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className={"turnfiles" + (open ? " on" : "")}>
      <div className="tfh">
        <button type="button" className="tft" aria-expanded={open} onClick={() => setOpen(!open)}>
          <FileDiffIcon size={13} />
          <span>{t("tf.changed", { n: sum.files })}</span>
          {(sum.added > 0 || sum.removed > 0) && (
            <span className="fxic">
              <span className="add">+{sum.added}</span>
              <span className="del">−{sum.removed}</span>
            </span>
          )}
          {sum.undone && <span className="tfundone">{t("tf.undoneTag")}</span>}
          {open ? <ChevronDown size={13} /> : <ChevronRight size={13} />}
        </button>
        <span className="sp" />
        {open && !sum.undone && (
          <button type="button" className="btn sm ghost fxrevert" disabled={busy || (tc ? !tc.restorable : false)} onClick={undo} title={tc && !tc.restorable ? t("tf.cannot") : t("tf.undoTitle")}>
            <Undo2 size={12} />
            {t("tf.undo")}
          </button>
        )}
      </div>
      {open && (
        <div className="tfl">
          {err ? (
            <div className="fxmsg small errt">{err}</div>
          ) : !tc ? (
            <div className="fxmsg"><Skeleton kind="code" /></div>
          ) : tc.files.length === 0 ? (
            <div className="fxmsg small muted">{t("tf.none")}</div>
          ) : (
            tc.files.map((f) => <TurnFile key={f.path} agent={agent} turn={turn} f={f} />)
          )}
        </div>
      )}
    </div>
  );
}

function TurnFile({ agent, turn, f }: { agent: string; turn: string; f: TurnChanges["files"][number] }) {
  const [open, setOpen] = useState(false);
  const [d, setD] = useState<FileDiff | null>(null);
  const [err, setErr] = useState("");
  useEffect(() => {
    if (!open || d || f.binary || f.large) return;
    API.turnDiff(agent, turn, f.path).then(setD, (e) => setErr(e?.message ?? String(e)));
  }, [open, d, agent, turn, f]);
  return (
    <div className="tff">
      <button type="button" className="fxitem" aria-expanded={open} onClick={() => setOpen(!open)}>
        {open ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
        <span className="fxip" title={f.path}>
          {f.path}
        </span>
        <span className={"fxtag st-" + f.status}>{t(`fx.st.${f.status}` as any)}</span>
        {!f.binary && !f.large && (
          <span className="fxic">
            <span className="add">+{f.added}</span>
            <span className="del">−{f.removed}</span>
          </span>
        )}
      </button>
      {open &&
        (f.binary ? (
          <div className="fxmsg small muted">{t("fx.binary")}</div>
        ) : f.large ? (
          <div className="fxmsg small muted">{t("fx.large")}</div>
        ) : err ? (
          <div className="fxmsg small errt">{err}</div>
        ) : !d ? (
          <div className="fxmsg"><Skeleton kind="code" /></div>
        ) : d.hunks.length === 0 ? (
          <div className="fxmsg small muted">{f.status === "deleted" ? t("fx.emptyDeleted") : t("fx.emptyFile")}</div>
        ) : (
          <UnifiedDiff hunks={d.hunks} path={f.path} />
        ))}
    </div>
  );
}
