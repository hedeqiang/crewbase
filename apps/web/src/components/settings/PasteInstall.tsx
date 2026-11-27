import { useEffect, useMemo, useState, type Dispatch, type SetStateAction } from "react";
import { Sheet } from "../Sheet";
import { ChevronDown, ChevronRight, Lock, Search, X } from "lucide-react";
import { MonoMark } from "../ProviderMark";
import { API, api, type PluginView } from "../../api";
import { getLang, t } from "../../i18n";
import { errText } from "../../errors";
import { toast } from "../../store";
import { settle, signInWindow } from "./ExtParts";

type Text = { zh: string; en: string };
interface PlanVar {
  key: string;
  value: string;
  secret?: boolean;
  missing?: boolean;
}
interface PlanServer {
  name: string;
  transport: "stdio" | "http";
  command?: string[];
  url?: string;
  oauth?: boolean;
  env?: PlanVar[];
  headers?: PlanVar[];
  exists?: boolean;
}
interface PlanSkill {
  name: string;
  description?: string;
  path: string;
  exists?: boolean;
}
interface Plan {
  kind: "mcp" | "skill" | "plugin";
  source: string;
  servers?: PlanServer[];
  skills?: PlanSkill[];
  plugin?: string;
  note?: Text;
}

const detect = (input: string) => api<Plan>("POST", "/api/install/detect", { input });
const apply = (b: { kind: string; source: string; servers?: PlanServer[]; skills?: string[]; scope?: string }) =>
  api<{ servers?: { name: string }[]; skills?: { name: string }[] }>("POST", "/api/install/apply", b);

/** more new skills than this: none start ticked, and groups start closed */
const MANY = 20;

const tx = (x?: Text) => (x ? (getLang() === "en" ? x.en : x.zh) || x.zh || x.en : "");

/** 粘贴安装: paste an MCP config, a command, a server URL, a GitHub link,
 *  a folder or an npm package; see what it installs; install it. */
export function PasteInstall({
  initial = "",
  picks,
  onClose,
  onDone,
  onApprove,
}: {
  initial?: string;
  /** skill folders to tick (a library's suggestions); default: every new one */
  picks?: string[];
  onClose: () => void;
  onDone: () => void;
  onApprove: (p: PluginView) => void;
}) {
  const [input, setInput] = useState(initial);
  const [plan, setPlan] = useState<Plan | null>(null);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [picked, setPicked] = useState<Set<string>>(new Set());

  const run = async (text = input) => {
    if (!text.trim()) return;
    setBusy(true);
    setErr("");
    setPlan(null);
    try {
      const p = await detect(text);
      setPlan(p);
      const fresh = (p.skills ?? []).filter((s) => !s.exists);
      const wanted = picks?.length ? fresh.filter((s) => picks.includes(s.path.split("/").pop() ?? s.path) || picks.includes(s.name)) : fresh;
      // a big library starts with nothing ticked: picking 10 of 175 beats unticking 165
      const start = picks?.length && wanted.length ? wanted : fresh.length > MANY ? [] : fresh;
      setPicked(new Set(start.map((s) => s.path)));
    } catch (e) {
      setErr(errText(e));
    } finally {
      setBusy(false);
    }
  };
  // pasted on the page: recognise it straight away
  useEffect(() => {
    if (initial.trim()) void run(initial);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  const editServer = (i: number, f: (s: PlanServer) => PlanServer) => setPlan((p) => (p ? { ...p, servers: p.servers!.map((s, j) => (j === i ? f(s) : s)) } : p));
  const editVar = (i: number, field: "env" | "headers", k: number, value: string) =>
    editServer(i, (s) => ({ ...s, [field]: (s[field] ?? []).map((v, j) => (j === k ? { ...v, value } : v)) }));

  const missing =
    plan?.kind === "mcp" && (plan.servers ?? []).some((s) => !s.name.trim() || [...(s.env ?? []), ...(s.headers ?? [])].some((v) => !v.value.trim()));
  const missingN =
    plan?.kind === "mcp" ? (plan.servers ?? []).reduce((a, x) => a + [...(x.env ?? []), ...(x.headers ?? [])].filter((v) => !v.value.trim()).length, 0) : 0;
  const ready = !!plan && !busy && (plan.kind === "plugin" || (plan.kind === "skill" ? picked.size > 0 : !missing));

  const install = async () => {
    if (!plan) return;
    setBusy(true);
    setErr("");
    try {
      if (plan.kind === "plugin") {
        const v = await API.install({ source: plan.plugin! });
        toast(t("mk.added", { name: v.name }));
        onDone();
        if (!v.approved) onApprove(v);
        return onClose();
      }
      if (plan.kind === "skill") {
        const r = await apply({ kind: "skill", source: plan.source, skills: [...picked] });
        toast(t("pst.skillsAdded", { names: (r.skills ?? []).map((s) => s.name).join("、") }));
        onDone();
        return onClose();
      }
      // a server that signs in opens its page: the window is opened during the click
      const signIn = (plan.servers ?? []).find((s) => s.oauth);
      const win = signIn ? signInWindow() : null;
      const r = await apply({ kind: "mcp", source: plan.source, servers: plan.servers });
      onDone();
      const names = (r.servers ?? []).map((s) => s.name);
      toast(t("pst.serversAdded", { names: names.join("、") }));
      if (signIn) {
        const s = await settle(signIn.name);
        if (s?.status?.state === "login" && s.status.loginUrl) {
          if (win?.(s.status.loginUrl)) toast(t("mk2.signInOpened", { name: signIn.name }));
        } else win?.(null);
        onDone();
      }
      onClose();
    } catch (e) {
      setErr(errText(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Sheet onClose={onClose} className={"pstmodal" + (plan?.kind === "skill" ? " sk" + ((plan.skills?.length ?? 0) > MANY ? " tall" : "") : "")}>
        <div className="mhd">
          <h3 id="pstTitle">{plan ? t("pst.confirmTitle") : t("pst.title")}</h3>
          <button className="ix" type="button" aria-label={t("common.close")} onClick={onClose}>
            <X size={14} />
          </button>
        </div>
        <div className="mbd">
          {plan ? (
            <div className="pstsrc">
              <span className="small muted">{t("pst.from")}</span>
              <code title={input}>{input.replace(/\s+/g, " ").slice(0, 120)}</code>
              <button className="linkbtn" type="button" onClick={() => setPlan(null)}>
                {t("pst.edit")}
              </button>
            </div>
          ) : (
            <>
          <p className="small" style={{ margin: "0 0 8px" }}>
              {t("pst.desc")}
            </p>
            <textarea
              className="fi mono pstin"
              spellCheck={false}
              autoComplete="off"
              rows={5}
              autoFocus
              value={input}
              placeholder={t("pst.ph")}
              onChange={(e) => {
                setInput(e.target.value);
                setPlan(null);
                setErr("");
              }}
              onPaste={(e) => {
                const text = e.clipboardData.getData("text");
                if (text && !input.trim()) {
                  e.preventDefault();
                  setInput(text);
                  void run(text);
                }
              }}
              onKeyDown={(e) => {
                if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) void run();
              }}
            />
            {!plan && (
              <div className="mft" style={{ marginTop: 10 }}>
                <button className="btn pri" type="button" disabled={busy || !input.trim()} onClick={() => void run()}>
                  {busy ? t("pst.detecting") : t("pst.detect")}
                </button>
              </div>
            )}
            </>
          )}
          {err && <p className="small errt pstErr">{err}</p>}

          {plan && (
            <div className="pstplan">
              <div className="fl">{t(`pst.kind.${plan.kind}` as any)}</div>
              {plan.note && <p className="small warnline">{tx(plan.note)}</p>}

              {plan.kind === "mcp" &&
                plan.servers!.map((s, i) => (
                  <div className="pstsrv" key={i}>
                    <div className="pstrow">
                      <MonoMark id={s.name || "x"} label={s.name || "?"} size={32} />
                      <input
                        className="fi sm mono pstname"
                        value={s.name}
                        aria-label={t("pst.name")}
                        onChange={(e) => editServer(i, (x) => ({ ...x, name: e.target.value.toLowerCase().replace(/[^a-z0-9_-]/g, "-").slice(0, 32), exists: false }))}
                      />
                      <span className="small muted mono pstwhat" title={s.url ?? (s.command ?? []).join(" ")}>
                        {s.transport === "http" ? s.url : (s.command ?? []).join(" ")}
                      </span>
                    </div>
                    {s.exists && <p className="small warnline">{t("pst.exists", { name: s.name })}</p>}
                    {s.oauth && <p className="small muted">{t("pst.oauth")}</p>}
                    {(["env", "headers"] as const).map((f) =>
                      (s[f] ?? []).map((v, k) => (
                        <label className={"pstvar" + (!v.value.trim() ? " need" : "")} key={f + k}>
                          <span className="pstvk">
                            <span className="mono" title={v.key}>
                              {v.key}
                            </span>
                            {v.secret && (
                              <span className="pstvault">
                                <Lock size={11} />
                                {t("pst.toVault")}
                              </span>
                            )}
                          </span>
                          <input
                            className="fi sm mono"
                            type={v.secret ? "password" : "text"}
                            value={v.value}
                            spellCheck={false}
                            autoComplete="off"
                            placeholder={v.missing ? t("pst.fillIn") : ""}
                            onChange={(e) => editVar(i, f, k, e.target.value)}
                          />
                        </label>
                      )),
                    )}
                  </div>
                ))}

              {plan.kind === "skill" && <SkillPicker skills={plan.skills ?? []} picked={picked} setPicked={setPicked} />}

              {plan.kind === "plugin" && (
                <p className="small">
                  {t("pst.plugin")} <span className="mono">{plan.plugin}</span>
                </p>
              )}

              {plan.kind === "mcp" && <p className="small muted pstscope">{t("pst.scopeNote")}</p>}
              <div className="mft">
                {missingN > 0 && <span className="small pstneed">{t("pst.needN", { n: missingN })}</span>}
                {plan.kind === "skill" && !picked.size && <span className="small muted pstneed none">{t("pst.noneYet")}</span>}
                <button className="btn ghost" type="button" onClick={onClose}>
                  {t("common.cancel")}
                </button>
                <button className="btn pri" type="button" disabled={!ready} onClick={() => void install()}>
                  {busy ? t("pst.installing") : plan.kind === "skill" ? t("pst.installN", { n: picked.size }) : t("pst.install")}
                </button>
              </div>
            </div>
          )}
        </div>
      </Sheet>
  );
}

/** a description that is only a YAML block marker (">-", "|") says nothing */
const cleanDesc = (d?: string) => {
  const v = (d ?? "").replace(/\s+/g, " ").trim();
  return /^[>|][-+]?\d*$/.test(v) ? "" : v;
};

const OTHER = "\0other";

interface Group {
  key: string;
  label: string;
  skills: PlanSkill[];
}

/** the folder holding a skill, without a trailing "skills" ("plugin/skills/x" → "plugin") */
const folderOf = (p: string) => {
  const seg = p.split("/").slice(0, -1);
  if (seg[seg.length - 1] === "skills") seg.pop();
  return seg.join("/");
};

/** groups skills by their folder; one group, or one skill per group, is no
 *  grouping at all (null: show the flat list) */
function groupSkills(skills: PlanSkill[]): Group[] | null {
  const by = new Map<string, PlanSkill[]>();
  for (const s of skills) {
    const k = folderOf(s.path);
    by.set(k, [...(by.get(k) ?? []), s]);
  }
  if (by.size < 2 || by.size === skills.length) return null;
  // a leading folder every group shares ("plugins/") is not worth repeating
  let common: string[] | null = null;
  for (const k of by.keys()) {
    const seg = k ? k.split("/") : [];
    if (!common) common = seg.slice(0, -1);
    let n = 0;
    while (n < common.length && n < seg.length - 1 && seg[n] === common[n]) n++;
    common = common.slice(0, n);
  }
  const cut = common?.length ? common.join("/") + "/" : "";
  const out: Group[] = [];
  const single: PlanSkill[] = [];
  for (const [key, list] of by) {
    if (list.length === 1 && key) single.push(list[0]);
    else out.push({ key, label: key ? key.slice(cut.length) : t("pst.rootGroup"), skills: list });
  }
  // plugins with a single skill: one "other" group instead of a header per skill
  if (single.length > 1) out.push({ key: OTHER, label: t("pst.otherGroup"), skills: single });
  else for (const x of single) out.push({ key: folderOf(x.path), label: folderOf(x.path).slice(cut.length), skills: [x] });
  return out;
}

function SkillPicker({
  skills,
  picked,
  setPicked,
}: {
  skills: PlanSkill[];
  picked: Set<string>;
  setPicked: Dispatch<SetStateAction<Set<string>>>;
}) {
  const [q, setQ] = useState("");
  const fresh = useMemo(() => skills.filter((s) => !s.exists), [skills]);
  const groups = useMemo(() => groupSkills(skills), [skills]);
  const many = fresh.length > MANY;
  // groups closed while browsing (a big library starts closed), and groups
  // closed by hand while searching (a search opens every group it matches)
  const [closed, setClosed] = useState<Set<string>>(() => new Set(many ? (groups ?? []).map((g) => g.key) : []));
  const [closedQ, setClosedQ] = useState<Set<string>>(new Set());
  const [open, setOpen] = useState<string | null>(null);

  const needle = q.trim().toLocaleLowerCase();
  const hit = (s: PlanSkill, g: Group) =>
    !needle || [s.name, cleanDesc(s.description), s.path, g.key === OTHER ? "" : g.label].some((x) => x.toLocaleLowerCase().includes(needle));
  const shownGroups = (groups ?? [{ key: "", label: "", skills }])
    .map((g) => ({ ...g, shown: g.skills.filter((s) => hit(s, g)) }))
    .filter((g) => g.shown.length);
  const shown = shownGroups.flatMap((g) => g.shown);
  const shownFresh = shown.filter((s) => !s.exists);
  const shownSet = new Set(shownFresh.map((s) => s.path));
  const hidden = needle ? [...picked].filter((p) => !shownSet.has(p)).length : 0;

  const set = (paths: string[], on: boolean) =>
    setPicked((p) => {
      const n = new Set(p);
      for (const x of paths) {
        if (on) n.add(x);
        else n.delete(x);
      }
      return n;
    });
  const isOpen = (k: string) => (needle ? !closedQ.has(k) : !closed.has(k));
  const toggle = (k: string) => {
    const f = (p: Set<string>) => {
      const n = new Set(p);
      if (n.has(k)) n.delete(k);
      else n.add(k);
      return n;
    };
    if (needle) setClosedQ(f);
    else setClosed(f);
  };

  const row = (s: PlanSkill, g: Group) => {
    const d = cleanDesc(s.description);
    const isOpenRow = open === s.path;
    const folder = g.key === OTHER ? folderOf(s.path).split("/").pop() ?? "" : "";
    return (
      <label className={"pstskill" + (isOpenRow ? " open" : "") + (s.exists ? " has" : "")} key={s.path}>
        <input type="checkbox" disabled={s.exists} checked={!s.exists && picked.has(s.path)} onChange={(e) => set([s.path], e.target.checked)} />
        <b title={s.path || s.name}>{s.name}</b>
        {folder && folder !== s.name && <span className="pstfold mono">{folder}</span>}
        {s.exists && <span className="pill sm">{t("pst.installed")}</span>}
        <span className="pstdesc" title={isOpenRow ? undefined : d}>
          {d}
        </span>
        {d.length > 48 && (
          <button
            className="ix pstmore"
            type="button"
            aria-label={isOpenRow ? t("pst.less") : t("pst.more")}
            aria-expanded={isOpenRow}
            title={isOpenRow ? t("pst.less") : t("pst.more")}
            onClick={(e) => {
              e.preventDefault();
              setOpen(isOpenRow ? null : s.path);
            }}
          >
            <ChevronDown size={13} />
          </button>
        )}
      </label>
    );
  };

  return (
    <div className="pstpick">
      {skills.length > 6 && (
        <label className="search pstq">
          <Search size={14} />
          <input
            type="search"
            value={q}
            placeholder={t("pst.search")}
            aria-label={t("pst.search")}
            spellCheck={false}
            autoComplete="off"
            onChange={(e) => {
              setQ(e.target.value);
              setClosedQ(new Set());
            }}
            onKeyDown={(e) => {
              if (e.key === "Escape" && q) {
                e.stopPropagation();
                setQ("");
              }
            }}
          />
          {q && (
            <button className="ix" type="button" aria-label={t("pst.clearSearch")} onClick={() => setQ("")}>
              <X size={12} />
            </button>
          )}
        </label>
      )}
      <div className="pstall small">
        <span className="pstcount">
          <b>{t("pst.picked", { n: picked.size, total: fresh.length })}</b>
          {needle && <span> · {t("pst.matchN", { n: shown.length })}</span>}
          {hidden > 0 && <span> · {t("pst.hiddenPicked", { n: hidden })}</span>}
          {!needle && fresh.length < skills.length && <span> · {t("pst.installedN", { n: skills.length - fresh.length })}</span>}
        </span>
        <button className="linkbtn" type="button" disabled={!shownFresh.some((s) => !picked.has(s.path))} onClick={() => set([...shownSet], true)}>
          {needle ? t("pst.pickShown") : t("pst.pickAll")}
        </button>
        <button className="linkbtn" type="button" disabled={!shownFresh.some((s) => picked.has(s.path))} onClick={() => set([...shownSet], false)}>
          {needle ? t("pst.unpickShown") : t("pst.pickNone")}
        </button>
      </div>
      {many && !picked.size && !needle && <p className="small muted psthint">{t("pst.manyHint", { n: fresh.length })}</p>}
      <div className="pstlist" role="group" aria-label={t("pst.kind.skill")}>
        {!shown.length && <p className="small muted pstnone">{t("pst.noMatch", { q: q.trim() })}</p>}
        {groups
          ? shownGroups.map((g) => {
              const gf = g.shown.filter((s) => !s.exists);
              const on = gf.filter((s) => picked.has(s.path)).length;
              const o = isOpen(g.key);
              return (
                <div className={"pstgrp" + (o ? " open" : "")} key={g.key}>
                  <div className="pstgh">
                    <input
                      type="checkbox"
                      aria-label={t("pst.groupPick", { name: g.label })}
                      disabled={!gf.length}
                      checked={!!gf.length && on === gf.length}
                      ref={(el) => {
                        if (el) el.indeterminate = on > 0 && on < gf.length;
                      }}
                      onChange={(e) => set(gf.map((s) => s.path), e.target.checked)}
                    />
                    <button className="pstgt" type="button" aria-expanded={o} onClick={() => toggle(g.key)}>
                      {o ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
                      <span className="pstgl mono" title={g.key === OTHER ? undefined : g.key}>
                        {g.label}
                      </span>
                      <span className={"pstgn" + (on ? " on" : "")}>{t("pst.groupN", { n: on, total: gf.length })}</span>
                    </button>
                  </div>
                  {o && <div className="pstgb">{g.shown.map((s) => row(s, g))}</div>}
                </div>
              );
            })
          : shownGroups.flatMap((g) => g.shown.map((s) => row(s, g)))}
      </div>
    </div>
  );
}
