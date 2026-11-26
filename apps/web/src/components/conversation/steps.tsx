import { excerpt } from "../MsgExtras";
import { useEffect, useState, type ReactNode } from "react";
import { Check, ChevronDown, ChevronRight, CircleX, CircleMinus, Hand, ShieldCheck, ShieldX, ChevronUp } from "lucide-react";
import { useRawURL } from "../../api";
import { t, type Key } from "../../i18n";
import { argsFull, argsPreview, type Decision, type Step, type TurnPart } from "../../projection";
import { openSubRun } from "../../store";

/** How a finished step went: a declined approval or a command's non-zero
 *  exit (grep finding nothing, cat of a missing file) is not a failure of
 *  the tool, and is not shown as one. */
export type Outcome = "ok" | "declined" | "exit" | "error";
export function outcome(s: Step): Outcome {
  const r = s.result;
  if (!r || !r.isError) return "ok";
  if (/^not approved \(/.test(r.content)) return "declined";
  if (/^Exit code: [1-9]\d*\n/.test(r.content)) return "exit";
  return "error";
}

/** What an agent said about the pictures it made: one line, the rest a
 *  click away (the pictures are the answer). */
export function FoldedWords({ text, children }: { text: string; children: ReactNode }) {
  const [open, setOpen] = useState(false);
  if (open)
    return (
      <div className="foldw on">
        {children}
        <button type="button" className="foldbtn" onClick={() => setOpen(false)}>
          <ChevronUp size={13} />
          {t("media.lessWords")}
        </button>
      </div>
    );
  return (
    <button type="button" className="foldw" onClick={() => setOpen(true)} title={t("media.moreWords")} aria-expanded={false}>
      <span className="foldl">{excerpt(text, 120)}</span>
      <ChevronDown size={13} className="foldc" />
    </button>
  );
}

/** The image calls whose pictures are shown in the turn need no step line
 *  of their own (a failed one keeps its line: it says why). */
export function withoutMediaSteps(parts: TurnPart[]): TurnPart[] {
  const shown = new Set(parts.flatMap((p) => (p.k === "media" ? [...(p.m.calls ?? []), ...(p.m.call ? [p.m.call] : [])] : [])));
  if (!shown.size) return parts;
  return parts.flatMap((p): TurnPart[] => {
    if (p.k !== "steps") return [p];
    const steps = p.steps.filter((st) => !(shown.has(st.call.id) && st.result && !st.result.isError));
    return steps.length ? [{ ...p, steps }] : [];
  });
}

/** What a run of steps did, in words: 写了 3 个文件 · 跑了 2 条命令 · 搜了 1 次
 *  (the kinds that happened, most first; the step count when nothing fits). */
const WORK_KINDS: { key: Key; tools: string[] }[] = [
  { key: "work.wrote", tools: ["write_file", "edit_file", "library_save", "memory_add"] },
  { key: "work.ran", tools: ["bash", "computer_script", "computer_shortcut"] },
  { key: "work.read", tools: ["read_file", "list_dir", "library_read", "note_read"] },
  { key: "work.searched", tools: ["web_search", "library_search", "search_tools", "tool_search"] },
  { key: "work.opened", tools: ["web_fetch", "browser_open", "browser_navigate"] },
  { key: "work.made", tools: ["show_page", "image_generate", "video_generate", "preview_open"] },
  { key: "work.handed", tools: ["delegate"] },
];
export function workSummary(steps: Step[]): string | null {
  const counts: { key: Key; n: number }[] = [];
  let other = 0;
  for (const st of steps) {
    const k = WORK_KINDS.find((w) => w.tools.includes(st.call.name) || (w.key === "work.opened" && st.call.name.startsWith("browser_")) || (w.key === "work.ran" && st.call.name.startsWith("computer_")));
    if (!k) {
      other++;
      continue;
    }
    const c = counts.find((x) => x.key === k.key);
    if (c) c.n++;
    else counts.push({ key: k.key, n: 1 });
  }
  if (!counts.length) return steps.length ? t("chat.steps", { n: steps.length }) : null;
  counts.sort((a, b) => b.n - a.n);
  const parts = counts.slice(0, 3).map((c) => t(c.key, { n: c.n }));
  if (counts.length > 3 || other > 0) parts.push(t("work.more", { n: steps.length }));
  return parts.join(" · ");
}

/** Rerenders every second while on (a running step's clock). */
export function useTick(on: boolean) {
  const [, set] = useState(0);
  useEffect(() => {
    if (!on) return;
    const id = setInterval(() => set((n) => n + 1), 1000);
    return () => clearInterval(id);
  }, [on]);
}

/** 3 秒 / 1 分 10 秒; nothing under a second. */
export function durLabel(ms: number | undefined): string {
  if (ms === undefined || !(ms >= 1000)) return "";
  const s = Math.round(ms / 1000);
  return s < 60 ? t("dur.s", { n: s }) : t("dur.ms", { m: Math.floor(s / 60), s: s % 60 });
}

/** The first line that says something (for a one-line excerpt). */
function firstLine(s: string): string {
  return (
    s
      .split("\n")
      .map((l) => l.trim())
      .find((l) => l && !/^<\/?\w+>$/.test(l) && !/^Exit code:/.test(l)) ?? ""
  );
}

/** One tool call as one line: how it went, what it is, its main argument,
 *  how long it took (a clock while it runs). Its arguments and result open
 *  in place below it. live: the turn is still going (a step with no result
 *  is running; in a finished turn it was cut off). */
export function StepRow({ s, agent, live = true }: { s: Step; agent?: string; live?: boolean }) {
  const [open, setOpen] = useState(false);
  const running = !s.result && !s.awaiting && live;
  useTick(running);
  const cu = computerLabel(s);
  // a command that starts by going to the agent's own folder: that part says nothing
  const pv = cu ? "" : argsPreview(s.call.args).replace(/^cd\s+\S*\/workspaces\/[^/\s]+\/?\s*&&\s*/, "");
  const shots = (s.computer ?? []).filter((c) => c.screenshot).map((c) => c.screenshot!);
  const oc = s.result ? outcome(s) : null;
  const exit = oc === "exit" ? /^Exit code: (\d+)/.exec(s.result!.content)?.[1] : undefined;
  const t0 = Date.parse(s.time);
  const took = running ? Date.now() - t0 : s.doneAt ? Date.parse(s.doneAt) - t0 : undefined;
  const parked = !!s.result?.content.startsWith(PARKED);
  return (
    <div className={"tlitem step" + (running ? " run" : "") + (oc === "error" ? " bad" : "")}>
      <button className="tlrow stephead" type="button" aria-expanded={open} onClick={() => setOpen(!open)}>
        <span className="tlic">
          {s.awaiting ? (
            <Hand size={13} className="waitc" />
          ) : running ? (
            <span className="spin sm" />
          ) : !s.result ? (
            <CircleMinus size={13} className="muted" />
          ) : oc === "error" ? (
            <CircleX size={13} className="errc" />
          ) : oc === "ok" ? (
            <Check size={13} className="okc" />
          ) : (
            <CircleMinus size={13} className="muted" />
          )}
        </span>
        {cu ? <b className="cul">{cu}</b> : toolLabel(s.call.name) ? <b>{toolLabel(s.call.name)}</b> : <b className="mono">{s.call.name}</b>}
        {cu && (s.computer ?? []).some((c) => c.foreground) && <span className="pill sm">{t("tool.cu.fg")}</span>}
        <span className={"pv" + (PROSE_ARGS.has(s.call.name) ? "" : " mono")}>{pv}</span>
        {running && s.progress && (
          <span className="stepexit prog" title={s.progress}>
            {t("step.progress", { text: s.progress })}
          </span>
        )}
        {s.awaiting && <span className="pill sm amber">{t(s.asking ? "el.waiting" : "appr.waiting")}</span>}
        {exit && <span className="stepexit">{t("step.exit", { n: exit })}</span>}
        {!s.result && !s.awaiting && !live && <span className="stepexit">{t("step.unfinished")}</span>}
        {s.decision && <DecisionTag d={s.decision} />}
        {s.sub && (
          <span
            className="steplink"
            role="link"
            tabIndex={0}
            onClick={(e) => {
              e.stopPropagation();
              openSubRun(s.sub!);
            }}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.stopPropagation();
                openSubRun(s.sub!);
              }
            }}
          >
            {t("sub.view")}
          </span>
        )}
        <ChevronRight size={13} className="ch" />
        <span className="tlmeta">{durLabel(took)}</span>
      </button>
      {/* a failure says why without a click */}
      {oc === "error" && !open && <div className="steperr">{firstLine(s.result!.content)}</div>}
      {shots.length > 0 && agent && (
        <div className="cushots">
          {shots.map((p) => (
            <CuShot key={p} agent={agent} path={p} />
          ))}
        </div>
      )}
      {open && (
        <div className="stepbody">
          {s.call.name === "computer_act_batch" && (s.computer?.length ?? 0) > 0 && (
            <ol className="custeps small">
              {s.computer!.map((c, i) => (
                <li key={i} className={c.ok ? "" : "errc"}>
                  {cuActionLabel(c.action, { target: c.elementName || (c.element ? `[${c.element}]` : ""), text: c.text ?? "", keys: c.keys ?? "", path: (c.menu ?? []).join(" › "), app: c.appName || c.app })}
                  {c.error ? " · " + c.error : ""}
                </li>
              ))}
            </ol>
          )}
          {s.decision?.message && (
            <p className="small stepnote">
              {t("appr.noteFrom", { by: decidedBy(s.decision) })}「{s.decision.message}」
            </p>
          )}
          <ArgsView args={s.call.args} />
          {s.result &&
            (parked ? (
              <p className="small muted">{t("appr.parked")}</p>
            ) : (
              <OutputView content={s.result.content} error={oc === "error"} />
            ))}
        </div>
      )}
    </div>
  );
}

/** A call's arguments, one per line: short values inline, long or
 *  multi-line text (a file's content, a script) as a block of its own. */
function ArgsView({ args }: { args: any }) {
  const a = parsedArgs(args);
  const entries = Object.entries(a);
  if (!entries.length) {
    const raw = argsFull(args);
    return raw && raw !== "{}" ? <pre className="codeblk tlpre">{clip(raw)}</pre> : null;
  }
  return (
    <dl className="targs">
      {entries.map(([k, v]) => {
        const str = typeof v === "string" ? v : JSON.stringify(v, null, typeof v === "object" && v !== null ? 2 : 0);
        const block = str.includes("\n") || str.length > 90;
        return (
          <div key={k} className={block ? "blk" : ""}>
            <dt>{k}</dt>
            <dd>{block ? <pre className="codeblk tlpre">{clip(str)}</pre> : <code>{str}</code>}</dd>
          </div>
        );
      })}
    </dl>
  );
}

const tagged = (s: string, name: string) => new RegExp(`<${name}>\\n?([\\s\\S]*?)\\n?</${name}>`).exec(s)?.[1] ?? null;

/** A result as it reads best: a command's output and its errors apart
 *  (from its <stdout>/<stderr>), anything else as it came. */
function OutputView({ content, error }: { content: string; error: boolean }) {
  const m = /^Exit code: \d+\n([\s\S]*)$/.exec(content);
  if (m) {
    const out = tagged(m[1], "stdout");
    const err = tagged(m[1], "stderr");
    if (out !== null || err !== null)
      return (
        <>
          {out ? (
            <>
              <div className="steplbl">{t("step.result")}</div>
              <pre className="codeblk tlpre">{clip(out)}</pre>
            </>
          ) : null}
          {err ? (
            <>
              <div className="steplbl">{t("step.stderr")}</div>
              <pre className="codeblk tlpre errblk">{clip(err)}</pre>
            </>
          ) : null}
          {!out && !err && <p className="small muted steplbl">{t("step.noOutput")}</p>}
        </>
      );
  }
  if (!content.trim()) return <p className="small muted steplbl">{t("step.noOutput")}</p>;
  return (
    <>
      <div className="steplbl">{t("step.result")}</div>
      <pre className={"codeblk tlpre" + (error ? " errblk" : "")}>{clip(content)}</pre>
    </>
  );
}
const decidedBy = (d: Decision) => (d.by && d.by !== "desktop" ? d.by : t("common.you"));

/** How an approval was answered, as a small tag on its tool step. */
function DecisionTag({ d }: { d: Decision }) {
  const ok = d.outcome === "allowed-once";
  const label = ok
    ? t(d.remember ? "appr.tagAlways" : "appr.tagApproved")
    : t(d.outcome === "rejected" ? "appr.tagRejected" : d.outcome === "cancelled" ? "appr.tagCancelled" : "appr.tagUnavailable");
  const who = decidedBy(d);
  return (
    <span className={"dtag" + (ok ? " ok" : " no")} title={t(ok ? "appr.tagByOk" : "appr.tagByNo", { by: who }) + (d.message ? ` · 「${d.message}」` : "")}>
      {ok ? <ShieldCheck size={11} /> : <ShieldX size={11} />}
      {label}
    </span>
  );
}

/** A saved screenshot of a computer_* step: a thumbnail that opens full size. */
function CuShot({ agent, path }: { agent: string; path: string }) {
  const src = useRawURL(agent, path);
  const name = path.split("/").pop() ?? path;
  return (
    <a href={src} target="_blank" rel="noreferrer" title={t("att.open", { name })}>
      {src ? <img src={src} alt={name} loading="lazy" /> : <span className="thumbph" />}
    </a>
  );
}

function parsedArgs(args: any): Record<string, any> {
  if (typeof args === "string") {
    try {
      return JSON.parse(args) ?? {};
    } catch {
      return {};
    }
  }
  return args && typeof args === "object" ? args : {};
}

type CuVars = { target: string; text: string; keys: string; path: string; app: string };

/** One computer action in words: "点击 新建备忘录", "按 cmd+n". */
function cuActionLabel(action: string, v: CuVars): string {
  switch (action) {
    case "press":
    case "click":
    case "double_click":
    case "right_click":
      return t(v.target ? "tool.cu.click" : "tool.cu.clickApp", v);
    case "set_value":
      return t("tool.cu.set", v);
    case "focus":
      return t("tool.cu.focus", v);
    case "type":
      return t("tool.cu.type", { ...v, text: v.text.length > 40 ? v.text.slice(0, 40) + "…" : v.text });
    case "key":
      return t("tool.cu.key", v);
    case "menu":
      return t("tool.cu.menu", v);
    case "scroll":
      return t("tool.cu.scroll", v);
    case "drag":
      return t("tool.cu.drag", v);
    case "observe":
      return t("tool.cu.observe", v);
    case "open":
      return t("tool.cu.open", v);
  }
  return t("tool.cu.act", v);
}

/** computer_* steps in words ("查看 备忘录", "点击 新建备忘录"); null for other tools. */
function computerLabel(s: Step): string | null {
  const n = s.call.name;
  if (!n.startsWith("computer_")) return null;
  const a = parsedArgs(s.call.args);
  const logs = s.computer ?? [];
  const app = String(logs[0]?.appName || a.app || "");
  switch (n) {
    case "computer_apps":
      return t("tool.cu.apps");
    case "computer_observe":
      return t("tool.cu.observe", { app });
    case "computer_open":
      return t("tool.cu.open", { app });
    case "computer_act_batch":
      return t("tool.cu.batch", { app, n: Array.isArray(a.actions) ? a.actions.length : logs.length });
    case "computer_script":
      return app ? t("tool.cu.scriptApp", { app }) : t("tool.cu.script");
    case "computer_shortcut":
      return t("tool.cu.shortcut", { name: String(a.name ?? "") });
    case "computer_shortcuts":
      return t("tool.cu.shortcuts");
  }
  const l = logs[logs.length - 1];
  const at = a.x != null && a.y != null ? `(${Math.round(a.x)}, ${Math.round(a.y)})` : "";
  return cuActionLabel(String(a.action ?? ""), {
    target: l?.elementName || (a.element ? `[${a.element}]` : at),
    text: String(a.text ?? ""),
    keys: String(a.keys ?? ""),
    path: Array.isArray(a.menu) ? a.menu.join(" › ") : "",
    app,
  });
}

/** Readable names of common tools (others show their tool name). */
const TOOL_LABELS: Record<string, Key> = {
  delegate: "tool.delegate",
  bash: "tool.bash",
  show_page: "tool.page",
  library_save: "tool.libSave",
  library_search: "tool.libSearch",
  library_read: "tool.libRead",
  react: "tool.react",
  search_tools: "tool.findTools",
  tool_search: "tool.findTools",
  read_file: "tool.read",
  write_file: "tool.write",
  edit_file: "tool.edit",
  list_dir: "tool.list",
  web_fetch: "tool.fetch",
  web_search: "tool.search",
  browser_open: "tool.browse",
  image_generate: "tool.image",
  video_generate: "tool.video",
  memory_add: "tool.remember",
  followup_set: "tool.followup",
  routine_create: "tool.routineCreate",
  routine_update: "tool.routineUpdate",
  task_progress: "tool.progress",
  note_read: "tool.note",
  computer_apps: "tool.cuApps",
  computer_observe: "tool.cuObserve",
  computer_act: "tool.cuAct",
  computer_act_batch: "tool.cuBatch",
  computer_open: "tool.cuOpen",
  computer_script: "tool.cuScript",
  computer_shortcut: "tool.cuShortcut",
  computer_shortcuts: "tool.cuShortcuts",
};
export const toolLabel = (name: string) => (TOOL_LABELS[name] ? t(TOOL_LABELS[name]) : "");
/** tools whose main argument is prose (a task, a prompt), not code */
const PROSE_ARGS = new Set(["delegate", "image_generate", "video_generate", "memory_add", "followup_set", "web_search"]);

/** Start of tools.ParkedResult: the call stopped waiting for its approval. */
const PARKED = "Waiting for the user's approval";

function clip(s: string, n = 6000) {
  return s.length > n ? s.slice(0, n) + `\n… (${s.length - n} more)` : s;
}
