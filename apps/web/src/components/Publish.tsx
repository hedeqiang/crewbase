// 发布到文档库: a workspace file published to the team library, and the two
// kept apart until someone asks (features/library/links.go). A published
// file shows how it stands against its document: the same, the workspace
// newer (同步 makes it a new version of the document), the library newer
// (取回 writes it back to the workspace), or both changed (pick a side).

import { useEffect, useState, useSyncExternalStore } from "react";
import { ArrowDownToLine, ArrowUpFromLine, BookCheck, BookUp, ChevronDown, GitCompareArrows, Library } from "lucide-react";
import { API, teamOf, type LibFileLink, type LibLink } from "../api";
import { t } from "../i18n";
import { getState, openLibrary, toast, useStore } from "../store";
import { confirmDialog } from "./Confirm";
import { Popover, useMenu } from "./Popover";

// per agent: its published files (one request for a whole panel)
type Entry = { at: number; list: LibFileLink[]; inflight?: Promise<void> };
const cache = new Map<string, Entry>();
const subs = new Set<() => void>();
let version = 0;
const bump = () => {
  version++;
  subs.forEach((f) => f());
};

function load(team: string, agent: string): Promise<void> {
  const e = cache.get(agent);
  if (e?.inflight) return e.inflight;
  const p = API.libraryLinks(team, agent)
    .then((r) => void cache.set(agent, { at: Date.now(), list: r.links }))
    .catch(() => void cache.set(agent, { at: Date.now(), list: e?.list ?? [] }))
    .finally(bump);
  cache.set(agent, { at: e?.at ?? 0, list: e?.list ?? [], inflight: p });
  return p;
}

/** look again at an agent's published files (a file changed, a sync) */
export function refreshPublished(agent: string) {
  const e = cache.get(agent);
  if (e) e.at = 0;
  bump();
}

/** An agent's published files by workspace path, fresh within `fresh` ms
 *  (the files change under it, so a panel asks again when it shows). */
export function usePublished(agent: string, fresh = 15_000): Map<string, LibFileLink> {
  const team = useStore((s) => teamOf(s.agents[agent]));
  useSyncExternalStore(
    (f) => (subs.add(f), () => subs.delete(f)),
    () => version,
  );
  const e = cache.get(agent);
  useEffect(() => {
    if (!e || (!e.inflight && Date.now() - e.at > fresh)) void load(team, agent);
  }, [agent, team, e, e?.at, fresh]);
  const m = new Map<string, LibFileLink>();
  for (const l of e?.list ?? []) if (!m.has(l.path)) m.set(l.path, l);
  return m;
}

/** Publishes a file (or syncs it when it was published before). */
export async function publishFile(agent: string, path: string, from?: { session?: string; seq?: number }): Promise<LibLink | null> {
  const team = teamOf(getState().agents[agent]);
  try {
    const r = await API.publishToLibrary(team, { agent, path, session: from?.session, seq: from?.seq });
    refreshPublished(agent);
    return r.link;
  } catch (e: any) {
    toast(e?.message ?? String(e));
    return null;
  }
}

/** 同步: the workspace file becomes a new version of its document. */
export async function sync(team: string, l: LibLink) {
  try {
    await API.syncLibrary(team, l.item);
    toast(t("pub.synced"));
  } catch (e: any) {
    toast(e?.message ?? String(e));
  }
  refreshPublished(l.agent);
}

/** 取回: the document overwrites the workspace file (asks first). */
export async function pull(team: string, l: LibLink, name: string): Promise<boolean> {
  if (!(await confirmDialog({ text: t("pub.pullQ", { path: l.path, name }), ok: t("pub.pull"), danger: l.state === "both" || l.state === "file" }))) return false;
  try {
    await API.pullLibrary(team, l.item);
    toast(t("pub.pulled"));
    refreshPublished(l.agent);
    return true;
  } catch (e: any) {
    toast(e?.message ?? String(e));
    refreshPublished(l.agent);
    return false;
  }
}

/** The publish / sync control of one workspace file. `compact` is an icon
 *  (a row in a list); otherwise a labelled button (a pane's header).
 *  onPulled: the workspace file was rewritten (reload what shows it). */
export function PublishBtn({ agent, path, from, compact, onPulled }: { agent: string; path: string; from?: { session?: string; seq?: number }; compact?: boolean; onPulled?: () => void }) {
  const team = useStore((s) => teamOf(s.agents[agent]));
  const links = usePublished(agent);
  const l = links.get(path);
  const m = useMenu();
  const [busy, setBusy] = useState(false);
  const run = async (f: () => Promise<unknown>) => {
    m.close();
    setBusy(true);
    try {
      await f();
    } finally {
      setBusy(false);
    }
  };
  const doPull = () => run(async () => (l && (await pull(team, l, l.title || l.name)) ? onPulled?.() : undefined));

  // not published yet
  if (!l) {
    const label = t("pub.publish");
    return (
      <button
        type="button"
        className={compact ? "ix pubb" : "btn sm ghost pubb"}
        disabled={busy}
        aria-label={label}
        title={t("pub.publishTitle")}
        onClick={(e) => {
          e.stopPropagation();
          void run(async () => {
            const r = await publishFile(agent, path, from);
            if (r) toast(t("pub.published"));
          });
        }}
      >
        <BookUp size={compact ? 13 : 12} />
        {!compact && label}
      </button>
    );
  }

  const st = l.state;
  const icon =
    st === "file" ? <ArrowUpFromLine size={compact ? 13 : 12} /> : st === "doc" ? <ArrowDownToLine size={compact ? 13 : 12} /> : st === "both" ? <GitCompareArrows size={compact ? 13 : 12} /> : <BookCheck size={compact ? 13 : 12} />;
  const label = st === "file" ? t("pub.stFile") : st === "doc" ? t("pub.stDoc") : st === "both" ? t("pub.stBoth") : t("pub.stSame");
  return (
    <>
      <button
        type="button"
        ref={m.ref}
        className={(compact ? "ix pubb" : "btn sm ghost pubb") + " pub-" + (st || "same") + (m.open ? " on" : "")}
        disabled={busy}
        aria-label={label}
        title={label}
        onClick={(e) => {
          e.stopPropagation();
          m.toggle();
        }}
      >
        {icon}
        {!compact && (
          <>
            {label}
            <ChevronDown size={11} />
          </>
        )}
      </button>
      <Popover anchor={m.anchor} open={m.open} onClose={m.close} align="end" width={300} className="pubpop">
        <div className="pubh">
          <b title={l.title || l.name}>{l.title || l.name}</b>
          <span className="small muted">{t("pub.since", { at: new Date(l.at).toLocaleString() })}</span>
        </div>
        <p className={"pubst small pub-" + (st || "same")}>{t(`pub.hint.${st || "same"}` as any)}</p>
        {(st === "file" || st === "both") && (
          <button type="button" className="mi" onClick={() => void run(() => sync(team, l))}>
            <ArrowUpFromLine size={14} />
            <span className="g">
              {t("pub.sync")}
              <span>{t("pub.syncSub")}</span>
            </span>
          </button>
        )}
        {(st === "doc" || st === "both") && (
          <button type="button" className="mi" onClick={() => void doPull()}>
            <ArrowDownToLine size={14} />
            <span className="g">
              {t("pub.pull")}
              <span>{t("pub.pullSub")}</span>
            </span>
          </button>
        )}
        <button type="button" className="mi" onClick={() => (m.close(), openLibrary(l.item))}>
          <Library size={14} />
          <span className="g">{t("pub.open")}</span>
        </button>
      </Popover>
    </>
  );
}
