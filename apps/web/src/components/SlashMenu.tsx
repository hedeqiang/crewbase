// The composer's / menu: type / at the start of a message to pick a skill
// (its instructions go with the message), a family of tools to use, or a
// command (new chat, switch model, …). Picks show as chips above the box
// and are sent as `use` (features/skills loads a skill when its turn
// starts; "/name" typed in IM does the same).

import { useEffect, useRef, useState, type ReactNode } from "react";
import { BookOpen, Wrench, X } from "lucide-react";
import { API, type SlashPick } from "../api";
import { t } from "../i18n";

export interface SlashItem {
  key: string;
  kind: "skill" | "tools" | "cmd";
  name: string;
  desc?: string;
  icon?: ReactNode;
  hint?: string;
  run?: () => void;
}

/** The query when the message starts with "/" and the caret is still in
 *  that first word ("/rel" → "rel"); null otherwise. */
export function slashAt(text: string, caret: number): string | null {
  const m = /^\/([^\s/]*)$/u.exec(text.slice(0, caret));
  return m ? m[1] : null;
}

type Avail = { skills: { name: string; description?: string }[]; tools: { name: string; count: number; summary: string }[] };
const cache = new Map<string, { at: number; v: Avail }>();

/** What this agent can be told to use: its skills and its tool families
 *  (fetched when the menu opens, kept for a little while). */
export function useSlashSources(agent: string, open: boolean): Avail | null {
  const [v, setV] = useState<Avail | null>(() => cache.get(agent)?.v ?? null);
  useEffect(() => {
    if (!open) return;
    const c = cache.get(agent);
    if (c) setV(c.v);
    if (c && Date.now() - c.at < 20_000) return;
    let gone = false;
    void Promise.allSettled([API.agentSkills(agent), API.agentToolGroups(agent)]).then(([s, g]) => {
      const next: Avail = {
        skills: s.status === "fulfilled" ? s.value.skills ?? [] : [],
        // the families held back until asked for (MCP servers, plugins, the
        // browser…): the built-in ones are always there and need no pick
        tools: g.status === "fulfilled" ? (g.value.groups ?? []).filter((x, i, all) => x.deferred && x.name && x.count > 0 && all.findIndex((y) => y.name === x.name) === i) : [],
      };
      cache.set(agent, { at: Date.now(), v: next });
      if (!gone) setV(next);
    });
    return () => {
      gone = true;
    };
  }, [agent, open]);
  return v;
}

/** Items matching q, best first: names that start with it, then the rest. */
export function filterSlash(items: SlashItem[], q: string): SlashItem[] {
  const s = q.trim().toLowerCase();
  if (!s) return items;
  const score = (it: SlashItem) => {
    const n = it.name.toLowerCase();
    if (n === s) return 0;
    if (n.startsWith(s)) return 1;
    if (n.split(/[-_.\s]/).some((w) => w.startsWith(s))) return 2;
    if (n.includes(s)) return 3;
    if ((it.desc ?? "").toLowerCase().includes(s) || (it.hint ?? "").toLowerCase().includes(s)) return 4;
    return 9;
  };
  return items
    .map((it, i) => ({ it, i, sc: score(it) }))
    .filter((x) => x.sc < 9)
    .sort((a, b) => a.sc - b.sc || a.i - b.i)
    .map((x) => x.it);
}

export function skillItems(av: Avail | null): SlashItem[] {
  return (av?.skills ?? []).map((s) => ({ key: "skill:" + s.name, kind: "skill", name: s.name, desc: s.description, icon: <BookOpen size={14} /> }));
}
export function toolItems(av: Avail | null): SlashItem[] {
  return (av?.tools ?? []).map((g) => ({ key: "tools:" + g.name, kind: "tools", name: g.name, desc: g.summary, hint: t("slash.toolsN", { n: g.count }), icon: <Wrench size={14} /> }));
}

const GROUPS: { kind: SlashItem["kind"]; label: "slash.skills" | "slash.tools" | "slash.cmds" }[] = [
  { kind: "skill", label: "slash.skills" },
  { kind: "cmd", label: "slash.cmds" },
  { kind: "tools", label: "slash.tools" },
];

export function SlashMenu({ items, sel, loading, onPick, onHover, empty }: { items: SlashItem[]; sel: number; loading: boolean; onPick: (it: SlashItem) => void; onHover: (i: number) => void; empty: ReactNode }) {
  const list = useRef<HTMLDivElement>(null);
  useEffect(() => {
    list.current?.querySelector<HTMLElement>('[aria-selected="true"]')?.scrollIntoView({ block: "nearest" });
  }, [sel, items]);
  let i = -1;
  return (
    <div className="mention slash" role="listbox" aria-label={t("slash.title")} ref={list} onMouseDown={(e) => e.preventDefault()}>
      {items.length === 0 ? (
        <div className="slashempty small muted">{loading ? t("common.loading") : empty}</div>
      ) : (
        GROUPS.map((g) => {
          const rows = items.filter((x) => x.kind === g.kind);
          if (!rows.length) return null;
          return (
            <div key={g.kind} className="slashg">
              <div className="mlbl">{t(g.label)}</div>
              {rows.map((it) => {
                const k = ++i;
                return (
                  <button key={it.key} type="button" role="option" aria-selected={k === sel} className={"mi " + it.kind} onMouseEnter={() => onHover(k)} onClick={() => onPick(it)}>
                    <span className="slashic">{it.icon}</span>
                    <span className="g">
                      <b>
                        <span className="slashn">/{it.name}</span>
                        {it.hint && <span className="slashh">{it.hint}</span>}
                      </b>
                      {it.desc && <span>{it.desc}</span>}
                    </span>
                  </button>
                );
              })}
            </div>
          );
        })
      )}
      <div className="slashfoot small muted">{t("slash.keys")}</div>
    </div>
  );
}

/** The picks for the next message, above the box. */
export function PickChips({ picks, onRemove }: { picks: SlashPick[]; onRemove: (p: SlashPick) => void }) {
  if (!picks.length) return null;
  return (
    <div className="pickchips">
      {picks.map((p) => (
        <span key={p.kind + p.name} className={"pickchip " + p.kind} title={p.kind === "skill" ? t("slash.skillTip", { name: p.name }) : t("slash.toolsTip", { name: p.name })}>
          {p.kind === "skill" ? <BookOpen size={12} /> : <Wrench size={12} />}
          <span>{p.name}</span>
          <button type="button" aria-label={t("slash.remove", { name: p.name })} onClick={() => onRemove(p)}>
            <X size={11} />
          </button>
        </span>
      ))}
    </div>
  );
}

/** On a sent message: what was picked for it. */
export function UsedChips({ use }: { use?: { kind: string; name: string }[] }) {
  if (!use?.length) return null;
  return (
    <div className="usedchips">
      {use.map((u) => (
        <span key={u.kind + u.name} className={"pickchip sm " + u.kind} title={u.kind === "skill" ? t("slash.usedSkill", { name: u.name }) : t("slash.usedTools", { name: u.name })}>
          {u.kind === "skill" ? <BookOpen size={11} /> : <Wrench size={11} />}
          <span>{u.name}</span>
        </span>
      ))}
    </div>
  );
}
