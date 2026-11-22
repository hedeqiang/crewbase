import { Skeleton } from "./Skeleton";
import { useEffect, useState, useSyncExternalStore } from "react";
import { BookUp, ChevronDown, ChevronRight, FileDiff as FileDiffIcon, Undo2 } from "lucide-react";
import { API, type FileDiff, type TurnChanges, type TurnSummary } from "../api";
import { t } from "../i18n";
import { toast } from "../store";
import { confirmDialog } from "./Confirm";
import { Popover, useMenu } from "./Popover";
import { UnifiedDiff } from "./Files";
import { PublishBtn, publishFile, usePublished } from "./Publish";

/* 撤销这一轮的改动: in the actions of a turn's last reply, the files the
 * turn changed in the agent's workspace (features/console/snapshots.go),
 * each with its diff, and 撤销这一轮 (the files back as they were before
 * it). The Files tab's 改动 shows the workspace's changes as a whole. */

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

/** 撤销这一轮的改动: in a reply's actions, when its turn changed files.
 *  Opens the files it changed (each with its diff) and 撤销这一轮. */
export function UndoTurnBtn({ agent, turn, ended, session, seq }: { agent: string; turn: string; ended: string; session?: string; seq?: number }) {
  const sum = useSummary(agent, turn, ended);
  const published = usePublished(agent, 60_000);
  const m = useMenu();
  const [tc, setTc] = useState<TurnChanges | null>(null);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (!m.open || tc) return;
    API.turnChanges(agent, turn).then(setTc, (e) => setErr(e?.message ?? String(e)));
  }, [m.open, tc, agent, turn]);
  if (!sum || sum.files === 0) return null;
  const n = sum.files;

  const undo = async () => {
    const cur = tc ?? (await API.turnChanges(agent, turn).catch(() => null));
    if (!cur) return;
    const text = cur.later?.length ? t("tf.undoLater", { n: cur.files.length, later: cur.later.length }) : t("tf.undoQ", { n: cur.files.length });
    m.close();
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

  // the files of the turn that are still there and not published yet
  const from = { session, seq };
  const unpublished = (tc?.files ?? []).filter((f) => f.status !== "deleted" && !published.has(f.path));
  const publishAll = async () => {
    setBusy(true);
    let n = 0;
    for (const f of unpublished) if (await publishFile(agent, f.path, from)) n++;
    setBusy(false);
    if (n) toast(t("pub.publishedN", { n }));
  };

  const title = sum.undone ? t("tf.undoneTitle", { n }) : t("tf.btnTitle", { n });
  return (
    <>
      <button type="button" className={"ix mact" + (m.open ? " on" : "")} ref={m.ref} aria-label={title} title={title} disabled={busy} onClick={m.toggle}>
        <FileDiffIcon size={14} />
      </button>
      <Popover anchor={m.anchor} open={m.open} onClose={m.close} placement="top" width={420} className="tfpop">
        <div className="tfph">
          <FileDiffIcon size={13} />
          <b>{t("tf.changed", { n })}</b>
          {(sum.added > 0 || sum.removed > 0) && (
            <span className="fxic">
              <span className="add">+{sum.added}</span>
              <span className="del">−{sum.removed}</span>
            </span>
          )}
          {sum.undone && <span className="tfundone">{t("tf.undoneTag")}</span>}
        </div>
        <div className="tfl">
          {err ? (
            <div className="fxmsg small errt">{err}</div>
          ) : !tc ? (
            <div className="fxmsg"><Skeleton kind="code" /></div>
          ) : tc.files.length === 0 ? (
            <div className="fxmsg small muted">{t("tf.none")}</div>
          ) : (
            tc.files.map((f) => <TurnFile key={f.path} agent={agent} turn={turn} f={f} from={from} />)
          )}
        </div>
        {!sum.undone && unpublished.length > 1 && (
          <div className="tfpf">
            <span className="small muted">{t("pub.publishTitle")}</span>
            <button type="button" className="btn sm ghost" disabled={busy} onClick={() => void publishAll()}>
              <BookUp size={12} />
              {t("pub.publishAll")}
            </button>
          </div>
        )}
        {!sum.undone && (
          <div className="tfpf">
            <span className="small muted">{t("tf.undoTitle")}</span>
            <button type="button" className="btn sm dng" disabled={busy || (tc ? !tc.restorable : false)} onClick={undo} title={tc && !tc.restorable ? t("tf.cannot") : undefined}>
              <Undo2 size={12} />
              {t("tf.undo")}
            </button>
          </div>
        )}
      </Popover>
    </>
  );
}

function TurnFile({ agent, turn, f, from }: { agent: string; turn: string; f: TurnChanges["files"][number]; from: { session?: string; seq?: number } }) {
  const [open, setOpen] = useState(false);
  const [d, setD] = useState<FileDiff | null>(null);
  const [err, setErr] = useState("");
  useEffect(() => {
    if (!open || d || f.binary || f.large) return;
    API.turnDiff(agent, turn, f.path).then(setD, (e) => setErr(e?.message ?? String(e)));
  }, [open, d, agent, turn, f]);
  return (
    <div className="tff">
      <div className="tffrow">
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
      {f.status !== "deleted" && <PublishBtn agent={agent} path={f.path} from={from} compact />}
      </div>
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
