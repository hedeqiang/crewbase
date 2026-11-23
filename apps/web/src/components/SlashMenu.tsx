// The composer's / menu: type / at the start of a message to pick a skill
// (its instructions go with the message), an MCP prompt template (filled
// in with arguments), a family of tools to use, or a command (new chat,
// switch model, …). Picks show as chips above the box and are sent as
// `use`; what they stand for is filled in when the turn starts
// (agentloop.ResolveUse). "/name" typed in IM picks a skill the same way.

import { useEffect, useRef, useState, type ReactNode } from "react";
import { BookOpen, MessageSquareQuote, Plug, Wrench, X } from "lucide-react";
import { API, type PromptInfo, type SlashPick } from "../api";
import { t } from "../i18n";

export interface SlashItem {
  key: string;
  kind: "skill" | "prompt" | "tools" | "cmd";
  /** what is typed after / to find it */
  name: string;
  /** sent as the pick's name when it differs from name ("server/prompt") */
  ref?: string;
  desc?: string;
  icon?: ReactNode;
  hint?: string;
  run?: () => void;
  args?: PromptInfo["args"];
}

/** The query when the message starts with "/" and the caret is still in
 *  that first word ("/rel" → "rel"); null otherwise. */
export function slashAt(text: string, caret: number): string | null {
  const m = /^\/([^\s/]*)$/u.exec(text.slice(0, caret));
  return m ? m[1] : null;
}

type Group = { name: string; count: number; summary: string; installed?: boolean };
type Avail = { skills: { name: string; description?: string }[]; prompts: PromptInfo[]; tools: Group[] };
const cache = new Map<string, { at: number; v: Avail }>();

/** What this agent can be told to use: its skills, the prompt templates
 *  of its MCP servers and its tool families (fetched when the menu opens,
 *  kept for a little while). */
export function useSlashSources(agent: string, open: boolean): Avail | null {
  const [v, setV] = useState<Avail | null>(() => cache.get(agent)?.v ?? null);
  useEffect(() => {
    if (!open || !agent) return;
    const c = cache.get(agent);
    setV(c?.v ?? null);
    if (c && Date.now() - c.at < 20_000) return;
    let gone = false;
    void Promise.allSettled([API.agentSkills(agent), API.agentToolGroups(agent), API.agentPrompts(agent)]).then(([s, g, p]) => {
      const next: Avail = {
        skills: s.status === "fulfilled" ? s.value.skills ?? [] : [],
        prompts: p.status === "fulfilled" ? p.value.prompts ?? [] : [],
        // what was added (MCP servers, installed plugins) and the families
        // held back until asked for (the browser…): the always-there
        // built-in ones need no pick
        tools: g.status === "fulfilled" ? (g.value.groups ?? []).filter((x, i, all) => (x.deferred || x.installed) && x.name && x.count > 0 && all.findIndex((y) => y.name === x.name) === i) : [],
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
    if (n.split(/[-_.:\s]/).some((w) => w.startsWith(s))) return 2;
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
export function promptItems(av: Avail | null): SlashItem[] {
  return (av?.prompts ?? []).map((p) => ({
    key: "prompt:" + p.server + "/" + p.name,
    kind: "prompt",
    name: p.name,
    ref: p.server + "/" + p.name,
    desc: p.title || p.description,
    hint: p.server,
    args: p.args,
    icon: <MessageSquareQuote size={14} />,
  }));
}
export function toolItems(av: Avail | null): SlashItem[] {
  return (av?.tools ?? []).map((g) => ({
    key: "tools:" + g.name,
    kind: "tools",
    name: g.name,
    desc: g.summary,
    hint: t("slash.toolsN", { n: g.count }),
    icon: g.installed ? <Plug size={14} /> : <Wrench size={14} />,
  }));
}

/** The pick an item makes (commands make none). */
export function pickOf(it: SlashItem): SlashPick | null {
  if (it.kind === "cmd") return null;
  return { kind: it.kind, name: it.ref ?? it.name, args: it.kind === "prompt" && it.args?.length ? {} : undefined };
}

const GROUPS: { kind: SlashItem["kind"]; label: "slash.skills" | "slash.prompts" | "slash.tools" | "slash.cmds" }[] = [
  { kind: "skill", label: "slash.skills" },
  { kind: "prompt", label: "slash.prompts" },
  { kind: "cmd", label: "slash.cmds" },
  { kind: "tools", label: "slash.tools" },
];

/** The items in the order they are shown (for ↑↓ and Enter). */
export function shownOrder(items: SlashItem[]): SlashItem[] {
  return GROUPS.flatMap((g) => items.filter((x) => x.kind === g.kind));
}

export function SlashMenu({ items, sel, loading, onPick, onHover, empty, forWho }: { items: SlashItem[]; sel: number; loading: boolean; onPick: (it: SlashItem) => void; onHover: (i: number) => void; empty: ReactNode; forWho?: string }) {
  const list = useRef<HTMLDivElement>(null);
  useEffect(() => {
    list.current?.querySelector<HTMLElement>('[aria-selected="true"]')?.scrollIntoView({ block: "nearest" });
  }, [sel, items]);
  const ordered = shownOrder(items);
  let i = -1;
  return (
    <div className="mention slash" role="listbox" aria-label={t("slash.title")} ref={list} onMouseDown={(e) => e.preventDefault()}>
      {forWho && <div className="slashfor small muted">{t("slash.for", { name: forWho })}</div>}
      {ordered.length === 0 ? (
        <div className="slashempty small muted">{loading ? t("common.loading") : empty}</div>
      ) : (
        GROUPS.map((g) => {
          const rows = ordered.filter((x) => x.kind === g.kind);
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

const pickIcon = (kind: string, size: number) => (kind === "skill" ? <BookOpen size={size} /> : kind === "prompt" ? <MessageSquareQuote size={size} /> : <Wrench size={size} />);
const pickLabel = (p: { kind: string; name: string }) => (p.kind === "prompt" ? p.name.slice(p.name.indexOf("/") + 1) : p.name);

/** The picks for the next message, above the box; a prompt template
 *  asks for its arguments here. */
export function PickChips({ picks, argsOf, onRemove, onArgs, missing }: { picks: SlashPick[]; argsOf: (p: SlashPick) => PromptInfo["args"]; onRemove: (p: SlashPick) => void; onArgs: (p: SlashPick, args: Record<string, string>) => void; missing?: string | null }) {
  if (!picks.length) return null;
  const tip = (p: SlashPick) => (p.kind === "skill" ? t("slash.skillTip", { name: p.name }) : p.kind === "prompt" ? t("slash.promptTip", { name: p.name }) : t("slash.toolsTip", { name: p.name }));
  return (
    <div className="pickchips">
      {picks.map((p) => {
        const args = p.kind === "prompt" ? argsOf(p) ?? [] : [];
        return (
          <span key={p.kind + p.name} className={"pickchip " + p.kind + (args.length ? " withargs" : "")} title={tip(p)}>
            <span className="pkh">
              {pickIcon(p.kind, 12)}
              <span>{pickLabel(p)}</span>
              <button type="button" aria-label={t("slash.remove", { name: pickLabel(p) })} onClick={() => onRemove(p)}>
                <X size={11} />
              </button>
            </span>
            {args.map((a) => (
              <label key={a.name} className={"pkarg" + (missing === p.name + "/" + a.name ? " bad" : "")} title={a.description}>
                <span>
                  {a.name}
                  {a.required ? " *" : ""}
                </span>
                <input
                  value={p.args?.[a.name] ?? ""}
                  placeholder={a.description || (a.required ? t("slash.argRequired") : t("slash.argOptional"))}
                  onChange={(e) => onArgs(p, { ...(p.args ?? {}), [a.name]: e.target.value })}
                />
              </label>
            ))}
          </span>
        );
      })}
    </div>
  );
}

/** On a sent message: what was picked for it. */
/** The commands a message used (/skill, /tools, a prompt), at the start of
 *  its bubble as they were typed: one token each, no chip outside it. */
export function UsedCmds({ use }: { use?: { kind: string; name: string }[] }) {
  if (!use?.length) return null;
  const tip = (u: { kind: string; name: string }) => (u.kind === "skill" ? t("slash.usedSkill", { name: u.name }) : u.kind === "prompt" ? t("slash.usedPrompt", { name: u.name }) : t("slash.usedTools", { name: u.name }));
  return (
    <>
      {use.map((u) => (
        <span key={u.kind + u.name} className="bubcmd" title={tip(u)}>
          /{pickLabel(u)}
        </span>
      ))}{" "}
    </>
  );
}
