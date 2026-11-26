import { useEffect, useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { Brain, ClipboardList, FileText, Flag, MessageSquare, Pencil, Search, StickyNote, Trash2, Users, X } from "lucide-react";
import { API, MemAPI, type AgentView, type LibItem, type MemEntry, type MemList } from "../api";
import { ago } from "../format";
import { t, type Key } from "../i18n";
import { openDesk, openLibrary, openSession, toast, useStore } from "../store";
import { Avatar } from "./Avatar";
import { EmptyState } from "./EmptyState";
import { Skeleton } from "./Skeleton";
import { Head } from "./Settings";

// 记忆: what agents know across conversations, in one place — every
// agent's long-term memory (MEMORY.md), team memory (TEAM.md), working
// memory (task progress, reference notes) and the documents agents saved to
// the 文档库. Search, filter, edit in place, delete, and 「这条不对」.

/** A row: an entry of the memory API, or a document an agent saved. */
type Row = MemEntry | (Omit<MemEntry, "kind"> & { kind: "doc"; doc: LibItem });
type KindFilter = "all" | "memory" | "team" | "working" | "doc";
type Range = "all" | "1" | "7" | "30";

const KIND_ICON: Record<Row["kind"], ReactNode> = {
  memory: <Brain size={14} />,
  team: <Users size={14} />,
  task: <ClipboardList size={14} />,
  note: <StickyNote size={14} />,
  doc: <FileText size={14} />,
};
const KIND_LABEL: Record<Row["kind"], Key> = { memory: "memp.k.memory", team: "memp.k.team", task: "memp.k.task", note: "memp.k.note", doc: "memp.k.doc" };

const group = (k: Row["kind"]): KindFilter => (k === "task" || k === "note" ? "working" : k);

/** the agent the page opens on (the agent panel's 查看全部) */
let preset = "";
export function openMemory(agent?: string) {
  preset = agent ?? "";
  openDesk("memory");
}

/** when an entry was saved: a day, or an exact time */
function savedAt(s?: string): Date | null {
  if (!s) return null;
  const d = /^\d{4}-\d{2}-\d{2}$/.test(s) ? new Date(s + "T00:00:00") : new Date(s);
  return isNaN(d.getTime()) ? null : d;
}
/** "25 分钟前" (ago() alone says "25 分钟") */
const agoLabel = (iso: string) => {
  const a = ago(iso);
  return a === t("time.justNow") ? a : t("memp.ago", { when: a });
};
function whenLabel(s?: string): string {
  const d = savedAt(s);
  if (!d) return "";
  if (s && s.length > 10) return agoLabel(s);
  const today = new Date();
  today.setHours(0, 0, 0, 0);
  const days = Math.round((today.getTime() - d.getTime()) / 86400000);
  if (days <= 0) return t("memp.today");
  if (days === 1) return t("memp.yesterday");
  return t("memp.daysAgo", { n: days });
}

/** text with the search term marked */
function Hl({ text, q }: { text: string; q: string }) {
  const term = q.trim();
  if (!term) return <>{text}</>;
  const parts: ReactNode[] = [];
  const low = text.toLowerCase();
  const tl = term.toLowerCase();
  let i = 0;
  for (let j = low.indexOf(tl); j >= 0 && parts.length < 40; j = low.indexOf(tl, i)) {
    if (j > i) parts.push(text.slice(i, j));
    parts.push(<mark key={j}>{text.slice(j, j + term.length)}</mark>);
    i = j + term.length;
  }
  parts.push(text.slice(i));
  return <>{parts}</>;
}

const matches = (r: Row, q: string) => {
  const s = q.trim().toLowerCase();
  if (!s) return true;
  return [r.text, r.section, r.name, r.by].some((x) => x && x.toLowerCase().includes(s));
};

/** a progress file's first lines without Markdown marks (and its title line) */
const plain = (s: string) =>
  s
    .replace(/^#\s*\S+\s*/, "")
    .replace(/(^|\s)#{1,6}\s/g, "$1")
    .replace(/(^|\s)[-*]\s/g, "$1")
    .replace(/\s+/g, " ")
    .trim();

const errMsg = (e: any) => e?.message ?? String(e);

/** The 记忆 page of the 工作台. */
export function MemoryPage() {
  const team = useStore((s) => s.team);
  const live = useStore((s) => s.agents);
  const [data, setData] = useState<MemList | null>(null);
  const [docs, setDocs] = useState<LibItem[]>([]);
  const [err, setErr] = useState("");
  const [q, setQ] = useState("");
  const [kind, setKind] = useState<KindFilter>("all");
  const [agent, setAgent] = useState(preset);
  const [range, setRange] = useState<Range>("all");
  const seq = useRef(0);
  useEffect(() => {
    preset = "";
  }, []);
  const load = () => {
    const n = ++seq.current;
    MemAPI.list().then(
      (r) => n === seq.current && (setData(r), setErr("")),
      (e) => n === seq.current && setErr(errMsg(e)),
    );
    API.library(team, { source: "agent", limit: 200 }).then(
      (r) => n === seq.current && setDocs(r.items ?? []),
      () => n === seq.current && setDocs([]),
    );
  };
  useEffect(load, [team]);

  const names = useMemo(() => {
    const m: Record<string, string> = {};
    for (const a of data?.agents ?? []) m[a.id] = a.name;
    return m;
  }, [data]);
  const teamName = useMemo(() => {
    const m: Record<string, string> = {};
    for (const x of data?.teams ?? []) m[x.id] = x.name;
    return m;
  }, [data]);

  const rows: Row[] = useMemo(() => {
    const out: Row[] = [...(data?.entries ?? [])];
    for (const d of docs) {
      if (!d.agent) continue;
      out.push({ kind: "doc", doc: d, agent: d.agent, team: d.team, text: d.title || d.name, name: d.name, saved: d.created, session: d.session });
    }
    return out;
  }, [data, docs]);

  const since = range === "all" ? 0 : Date.now() - Number(range) * 86400000;
  const shown = rows.filter((r) => {
    if (kind !== "all" && group(r.kind) !== kind) return false;
    if (agent && r.agent !== agent) return false;
    if (since) {
      const d = savedAt(r.saved);
      if (!d || d.getTime() < since - 86400000 * (r.saved && r.saved.length === 10 ? 1 : 0)) return false;
    }
    return matches(r, q);
  });
  const counts: Record<KindFilter, number> = { all: 0, memory: 0, team: 0, working: 0, doc: 0 };
  for (const r of rows) {
    if (agent && r.agent !== agent) continue;
    counts.all++;
    counts[group(r.kind)]++;
  }

  // grouped by who knows it: each agent, then each team
  const groups = useMemo(() => {
    const m = new Map<string, Row[]>();
    for (const r of shown) {
      const key = r.kind === "team" ? "t:" + (r.team ?? "") : "a:" + (r.agent ?? "");
      if (!m.has(key)) m.set(key, []);
      m.get(key)!.push(r);
    }
    const order = (k: string) => (k.startsWith("a:") ? 0 : 1);
    return [...m.entries()].sort((a, b) => order(a[0]) - order(b[0]) || b[1].length - a[1].length);
  }, [shown]);

  const patch = (r: Row, next: Partial<MemEntry> | null) => {
    if (r.kind === "doc") return;
    setData((d) => {
      if (!d) return d;
      const entries = next === null ? d.entries.filter((x) => x !== r) : d.entries.map((x) => (x === r ? { ...x, ...next } : x));
      return { ...d, entries };
    });
  };

  const KINDS: KindFilter[] = ["all", "memory", "team", "working", "doc"];
  return (
    <>
      <Head title={t("memp.title")} sub={t("memp.sub")} />
      <div className="vb memp">
        <div className="memp-bar">
          <label className="memp-search">
            <Search size={14} aria-hidden />
            <input type="search" value={q} placeholder={t("memp.searchPh")} aria-label={t("memp.searchPh")} onChange={(e) => setQ(e.target.value)} />
            {q && (
              <button type="button" className="ix" aria-label={t("memp.clear")} onClick={() => setQ("")}>
                <X size={13} />
              </button>
            )}
          </label>
          <div className="memp-filters">
            <select className="memp-sel" value={agent} aria-label={t("memp.fAgent")} onChange={(e) => setAgent(e.target.value)}>
              <option value="">{t("memp.allAgents")}</option>
              {(data?.agents ?? []).map((a) => (
                <option key={a.id} value={a.id}>
                  {a.name}
                </option>
              ))}
            </select>
            <select className="memp-sel" value={range} aria-label={t("memp.fDate")} onChange={(e) => setRange(e.target.value as Range)}>
              <option value="all">{t("memp.anyTime")}</option>
              <option value="1">{t("memp.d1")}</option>
              <option value="7">{t("memp.d7")}</option>
              <option value="30">{t("memp.d30")}</option>
            </select>
          </div>
        </div>
        <div className="memp-kinds" role="tablist" aria-label={t("memp.fKind")}>
          {KINDS.map((k) => (
            <button key={k} type="button" role="tab" aria-selected={kind === k} className="memp-kind" onClick={() => setKind(k)}>
              {k === "all" ? t("memp.kAll") : t(`memp.g.${k}` as Key)}
              <span className="n">{counts[k]}</span>
            </button>
          ))}
        </div>

        {!data && !err && <Skeleton kind="rows" />}
        {err && <EmptyState icon={<Brain size={18} />} title={t("memp.loadFailed")} desc={err} action={<button className="btn" type="button" onClick={load}>{t("common.retry")}</button>} />}
        {data && rows.length === 0 && <EmptyState icon={<Brain size={18} />} title={t("memp.empty")} desc={t("memp.emptyDesc")} />}
        {data && rows.length > 0 && shown.length === 0 && <EmptyState compact icon={<Search size={18} />} title={t("memp.noMatch")} desc={t("memp.noMatchDesc")} />}

        {groups.map(([key, list]) => {
          const isTeam = key.startsWith("t:");
          const id = key.slice(2);
          const a: AgentView | undefined = isTeam ? undefined : live[id];
          return (
            <section className="memp-group" key={key}>
              <div className="memp-gh">
                {isTeam ? (
                  <span className="memp-gico">
                    <Users size={15} />
                  </span>
                ) : a ? (
                  <Avatar agent={a} size={24} />
                ) : (
                  <span className="memp-gico">
                    <Brain size={15} />
                  </span>
                )}
                <b>{isTeam ? t("memp.teamOf", { name: teamName[id] || id }) : names[id] || id}</b>
                <span className="memp-gn">{t("memp.n", { n: list.length })}</span>
              </div>
              <div className="memp-list">
                {list.map((r, i) => (
                  <MemRow key={r.kind + (r.line ?? "") + (r.name ?? "") + (r.raw ?? r.text) + i} r={r} q={q} agentName={r.agent ? names[r.agent] || r.agent : ""} onPatch={(n) => patch(r, n)} />
                ))}
              </div>
            </section>
          );
        })}
      </div>
    </>
  );
}

type Mode = "view" | "edit" | "wrong" | "delete";

/** One entry: read it, edit it in place, mark it wrong or delete it — every
 *  step opens right in the row. */
export function MemRow({ r, q = "", agentName, onPatch, compact }: { r: Row; q?: string; agentName: string; onPatch: (next: Partial<MemEntry> | null) => void; compact?: boolean }) {
  const sessions = useStore((s) => s.sessions);
  const [mode, setMode] = useState<Mode>("view");
  const [draft, setDraft] = useState("");
  const [tell, setTell] = useState(true);
  const [busy, setBusy] = useState(false);
  const [open, setOpen] = useState(false);
  const area = useRef<HTMLTextAreaElement>(null);
  useEffect(() => {
    if (mode === "edit" || mode === "wrong") area.current?.focus();
  }, [mode]);

  const editable = r.kind !== "doc";
  const flaggable = r.kind === "memory" || r.kind === "team";
  const startEdit = async () => {
    if (r.kind === "task" || r.kind === "note") {
      // the list carries the first lines; edit the whole file
      try {
        const full = r.kind === "task" ? await API.taskProgress(r.agent!, r.name!) : await API.note(r.agent!, r.name!);
        setDraft(full.text);
      } catch (e) {
        toast(errMsg(e));
        return;
      }
    } else setDraft(r.text);
    setMode("edit");
  };
  const run = async (f: () => Promise<void>) => {
    setBusy(true);
    try {
      await f();
      setMode("view");
    } catch (e) {
      toast(errMsg(e));
    } finally {
      setBusy(false);
    }
  };
  const save = () =>
    run(async () => {
      if (r.kind === "doc") return;
      const res = await MemAPI.update(r, draft);
      const text = r.kind === "task" || r.kind === "note" ? draft.replace(/\s+/g, " ").trim().slice(0, 600) : res.text;
      const raw = r.raw && r.text ? r.raw.replace(r.text, res.text) : r.raw;
      onPatch({ text, raw });
      toast(t("memp.saved"));
    });
  const del = () =>
    run(async () => {
      if (r.kind === "doc") return;
      await MemAPI.remove(r);
      onPatch(null);
      toast(t("memp.deleted"));
    });
  const flag = () =>
    run(async () => {
      if (r.kind === "doc") return;
      const corr = draft.trim();
      const res = await MemAPI.wrong(r, corr, tell && r.kind === "memory");
      if (corr) onPatch({ text: corr.replace(/\s+/g, " "), raw: r.raw?.replace(r.text, corr.replace(/\s+/g, " ")) });
      else onPatch(null);
      toast(res.told ? t("memp.flaggedTold", { name: agentName }) : t("memp.flagged"));
    });
  const keys = (e: KeyboardEvent, ok: () => void) => {
    if (e.key === "Escape") (e.preventDefault(), setMode("view"));
    if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) (e.preventDefault(), ok());
  };

  const when = whenLabel(r.saved);
  const meta = r.session ? sessions[r.session] : undefined;
  const title = meta?.topic || meta?.title;
  const long = r.text.length > 140;
  return (
    <div className={"memrow" + (mode !== "view" ? " on" : "") + (compact ? " compact" : "")} data-kind={r.kind}>
      <span className="memrow-ico" title={t(KIND_LABEL[r.kind])} aria-label={t(KIND_LABEL[r.kind])}>
        {KIND_ICON[r.kind]}
      </span>
      <div className="memrow-main">
        {mode === "edit" ? (
          <div className="memrow-edit">
            <textarea ref={area} className="fi sm" rows={r.kind === "task" || r.kind === "note" ? 8 : 3} value={draft} onChange={(e) => setDraft(e.target.value)} onKeyDown={(e) => keys(e, save)} aria-label={t("memp.edit")} />
            <div className="memrow-acts">
              <span className="small muted">{t("memp.editHint")}</span>
              <button className="btn ghost" type="button" onClick={() => setMode("view")} disabled={busy}>
                {t("common.cancel")}
              </button>
              <button className="btn pri" type="button" onClick={save} disabled={busy || !draft.trim()}>
                {busy ? <span className="spin sm" /> : null}
                {t("common.save")}
              </button>
            </div>
          </div>
        ) : (
          <div
            className={"memrow-text" + (long && !open ? " clamp" : "")}
            onClick={() => long && setOpen(!open)}
            role={long ? "button" : undefined}
            tabIndex={long ? 0 : undefined}
            aria-expanded={long ? open : undefined}
            onKeyDown={(e) => long && (e.key === "Enter" || e.key === " ") && (e.preventDefault(), setOpen(!open))}
          >
            {r.kind === "task" || r.kind === "note" || r.kind === "doc" ? <b className="memrow-name">{r.kind === "task" ? r.section || r.name : r.kind === "doc" ? r.text : r.name}</b> : null}
            {r.kind === "doc" ? null : r.kind === "task" ? <Hl text={plain(r.text)} q={q} /> : <Hl text={r.text} q={q} />}
          </div>
        )}
        {mode === "view" && (
          <div className="memrow-meta">
            {r.section && (r.kind === "memory" || r.kind === "team") && <span className="memrow-sec">{r.section}</span>}
            {compact ? null : <span className="memrow-kind">{t(KIND_LABEL[r.kind])}</span>}
            {when && <span title={r.saved}>{t("memp.savedAt", { when })}</span>}
            {r.used && <span title={r.used}>{t("memp.usedAt", { when: agoLabel(r.used) })}</span>}
            {r.by && <span>{t("memp.by", { name: r.by })}</span>}
            {r.session && (
              <button className="memrow-src" type="button" onClick={() => openSession(r.session!)} title={(title ? title + " · " : "") + t("memp.openSrc")}>
                <MessageSquare size={11} />
                <span>{compact ? t("memp.srcShort") : title || t("memp.srcConv")}</span>
              </button>
            )}
          </div>
        )}
        {mode === "wrong" && (
          <div className="memrow-wrong">
            <div className="memrow-wq">
              <Flag size={13} />
              <b>{t("memp.wrongTitle")}</b>
            </div>
            <textarea ref={area} className="fi sm" rows={2} value={draft} placeholder={t("memp.wrongPh")} onChange={(e) => setDraft(e.target.value)} onKeyDown={(e) => keys(e, flag)} aria-label={t("memp.wrongTitle")} />
            {r.kind === "memory" && (
              <label className="memrow-tell">
                <input type="checkbox" checked={tell} onChange={(e) => setTell(e.target.checked)} />
                <span>{t("memp.tell", { name: agentName })}</span>
              </label>
            )}
            <div className="memrow-acts">
              <button className="btn ghost" type="button" onClick={() => setMode("view")} disabled={busy}>
                {t("common.cancel")}
              </button>
              <button className="btn pri" type="button" onClick={flag} disabled={busy}>
                {busy ? <span className="spin sm" /> : null}
                {draft.trim() ? t("memp.wrongFix") : t("memp.wrongRemove")}
              </button>
            </div>
          </div>
        )}
        {mode === "delete" && (
          <div className="memrow-del" role="alert">
            <span>{r.kind === "task" ? t("memp.delTask") : r.kind === "note" ? t("memp.delNote") : t("memp.delAsk")}</span>
            <button className="btn ghost" type="button" onClick={() => setMode("view")} disabled={busy}>
              {t("common.cancel")}
            </button>
            <button className="btn dngfill" type="button" onClick={del} disabled={busy}>
              {busy ? <span className="spin sm" /> : <Trash2 size={13} />}
              {t("common.delete")}
            </button>
          </div>
        )}
      </div>
      {mode === "view" && (
        <div className="memrow-tools">
          {r.kind === "doc" ? (
            <button className="btn ghost sm" type="button" onClick={() => openLibrary((r as { doc: LibItem }).doc.id)}>
              {t("memp.openDoc")}
            </button>
          ) : (
            <>
              {editable && (
                <button className="ix" type="button" aria-label={t("memp.edit")} data-tip={t("memp.edit")} onClick={() => void startEdit()}>
                  <Pencil size={13} />
                </button>
              )}
              {flaggable && (
                <button
                  className="ix"
                  type="button"
                  aria-label={t("memp.wrong")}
                  data-tip={t("memp.wrong")}
                  onClick={() => {
                    setDraft("");
                    setMode("wrong");
                  }}
                >
                  <Flag size={13} />
                </button>
              )}
              <button className="ix dng" type="button" aria-label={t("common.delete")} data-tip={t("common.delete")} onClick={() => setMode("delete")}>
                <Trash2 size={13} />
              </button>
            </>
          )}
        </div>
      )}
    </div>
  );
}

/** The agent panel's 记忆: its long-term memory, editable in place, and a
 *  way to the 记忆 page for everything else. */
export function AgentMemories({ agent }: { agent: AgentView }) {
  const [list, setList] = useState<MemEntry[] | null>(null);
  const [all, setAll] = useState(false);
  const seq = useRef(0);
  useEffect(() => {
    if (agent.kind === "chat") return;
    const n = ++seq.current;
    MemAPI.list().then(
      (r) => n === seq.current && setList(r.entries.filter((e) => e.agent === agent.id && e.kind === "memory")),
      () => n === seq.current && setList(null),
    );
  }, [agent.id, agent.status]);
  if (agent.kind === "chat" || list === null) return null;
  const SHOW = 4;
  const newest = [...list].reverse();
  const shown = all ? newest : newest.slice(0, SHOW);
  const patch = (r: MemEntry, next: Partial<MemEntry> | null) => setList((l) => (l ? (next === null ? l.filter((x) => x !== r) : l.map((x) => (x === r ? { ...x, ...next } : x))) : l));
  return (
    <>
      <div className="psec apf-sech">
        <span>
          {t("memp.agentSec")} <span className="pn-sech">{list.length ? t("memp.n", { n: list.length }) : ""}</span>
        </span>
        <button className="linkbtn" type="button" onClick={() => openMemory(agent.id)}>
          {t("memp.openAll")}
        </button>
      </div>
      {list.length === 0 ? (
        <div className="small muted memp-none">{t("memp.agentEmpty")}</div>
      ) : (
        <div className="memp-list inpanel">
          {shown.map((r, i) => (
            <MemRow key={(r.raw ?? r.text) + i} r={r} agentName={agent.name} compact onPatch={(n) => patch(r, n)} />
          ))}
          {list.length > SHOW && (
            <button className="memp-more" type="button" aria-expanded={all} onClick={() => setAll(!all)}>
              {all ? t("memp.less") : t("memp.more", { n: list.length - SHOW })}
            </button>
          )}
        </div>
      )}
    </>
  );
}
