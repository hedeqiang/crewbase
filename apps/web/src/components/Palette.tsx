import { keyLabel } from "../shortcuts";
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Bot, Hash, ListChecks, Monitor, PanelRight, Search, Shield, Square, SunMoon, MessageSquare } from "lucide-react";
import { API } from "../api";
import { t, type Key } from "../i18n";
import { modeLabel } from "../format";
import { newChat, openNewAgent, openSession, openSettings, sessionAgent, setState, setTheme, toast, useStore } from "../store";
import { Avatar } from "./Avatar";
import { SlotIcon, runCommand, useSlot } from "./UISlots";
import { SECTIONS, SECTION_ICONS } from "./Settings";

interface Item {
  group: string;
  icon: ReactNode;
  title: string;
  sub?: string;
  run: () => void;
}

export function Palette() {
  const open = useStore((s) => s.palette);
  if (!open) return null;
  return <PaletteInner />;
}

function PaletteInner() {
  const agents = useStore((s) => s.agents);
  const sessions = useStore((s) => s.sessions);
  const approvals = useStore((s) => s.approvals);
  const current = useStore((s) => s.current);
  const theme = useStore((s) => s.theme);
  const [q, setQ] = useState("");
  const [sel, setSel] = useState(0);
  const inp = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const close = () => setState({ palette: false });
  const paletteCmds = useSlot("palette.command");

  useEffect(() => inp.current?.focus(), []);

  const items = useMemo(() => {
    const it: Item[] = [];
    const cur = sessionAgent(current);
    for (const a of Object.values(agents))
      it.push({ group: t("pal.agents"), icon: <Avatar agent={a} size={20} />, title: a.name, sub: [a.role, a.folder].filter(Boolean).join(" · "), run: () => openSession(a.session) });
    for (const m of Object.values(sessions))
      if (m.kind === "channel") it.push({ group: t("pal.channels"), icon: <Hash size={16} />, title: "# " + (m.title || m.id), sub: m.folder, run: () => openSession(m.id) });
    for (const r of approvals)
      it.push({
        group: t("pal.pending"),
        icon: <ListChecks size={16} />,
        title: t("appr.wants", { agent: agents[r.agent]?.name ?? r.agent, summary: r.summary || r.tool }),
        sub: r.reason,
        run: () => openSession(r.session),
      });
    for (const s of SECTIONS) it.push({ group: t("pal.settings"), icon: SECTION_ICONS[s], title: t(`set.${s}` as Key), run: () => openSettings(s) });
    it.push({ group: t("pal.commands"), icon: <MessageSquare size={16} />, title: t("list.newChat"), sub: keyLabel("newChat"), run: () => void newChat() });
    it.push({ group: t("pal.commands"), icon: <Bot size={16} />, title: t("pal.cmd.newAgent"), sub: keyLabel("newAgent"), run: openNewAgent });
    const nextTheme = theme === "system" ? "light" : theme === "light" ? "dark" : "system";
    it.push({ group: t("pal.commands"), icon: <SunMoon size={16} />, title: t("pal.cmd.theme", { theme: t(`theme.${nextTheme}`) }), run: () => setTheme(nextTheme) });
    it.push({ group: t("pal.commands"), icon: <PanelRight size={16} />, title: t("pal.cmd.panel"), run: () => setState((s) => ({ panelOpen: !s.panelOpen, view: "chat" })) });
    if (cur) {
      for (const m of ["read-only", "sandbox", "host"] as const)
        if (m !== cur.effectiveMode)
          it.push({
            group: t("pal.commands"),
            icon: m === "host" ? <Monitor size={16} /> : <Shield size={16} />,
            title: t("pal.cmd.mode", { mode: modeLabel(m) }),
            sub: cur.name,
            run: () => {
              if (m === "host") setState({ hostConfirm: cur.id, view: "chat" });
              else API.patchAgent(cur.id, { mode: m }).then(() => toast(t("comp.modeSet", { name: cur.name, mode: modeLabel(m) })), (e) => toast(e.message));
            },
          });
      if (cur.status === "running" || cur.status === "waiting")
        it.push({ group: t("pal.commands"), icon: <Square size={16} />, title: t("pal.cmd.stop", { name: cur.name }), run: () => void API.action(cur.id, "stop") });
    }
    for (const c of paletteCmds)
      it.push({ group: t("ui.pluginCmd"), icon: <SlotIcon name={c.icon} size={16} />, title: c.title || c.key, sub: c.desc || c.owner, run: () => void runCommand(c, current ?? "") });
    const ql = q.trim().toLowerCase();
    if (!ql) return it;
    return it.filter((x) => x.title.toLowerCase().includes(ql) || x.sub?.toLowerCase().includes(ql) || x.group.toLowerCase().includes(ql));
  }, [agents, sessions, approvals, current, theme, q, paletteCmds]);

  useEffect(() => setSel(0), [q]);
  useEffect(() => {
    listRef.current?.querySelector('[aria-selected="true"]')?.scrollIntoView({ block: "nearest" });
  }, [sel]);

  const runAt = (i: number) => {
    const x = items[i];
    if (!x) return;
    close();
    x.run();
  };

  let lastGroup = "";
  return (
    <>
      <div className="scrim" onClick={close} />
      <div className="palette" role="dialog" aria-label={t("keys.palette")}>
        <label className="pin">
          <Search size={16} />
          <input
            ref={inp}
            value={q}
            placeholder={t("pal.placeholder")}
            autoComplete="off"
            onChange={(e) => setQ(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "ArrowDown") {
                e.preventDefault();
                setSel((s) => Math.min(items.length - 1, s + 1));
              } else if (e.key === "ArrowUp") {
                e.preventDefault();
                setSel((s) => Math.max(0, s - 1));
              } else if (e.key === "Enter") {
                e.preventDefault();
                runAt(sel);
              } else if (e.key === "Escape") {
                e.preventDefault();
                close();
              }
            }}
          />
        </label>
        <div className="plist" ref={listRef}>
          {items.length === 0 && <p className="small" style={{ padding: "10px 12px", margin: 0 }}>{t("pal.empty")}</p>}
          {items.map((x, i) => {
            const head = x.group !== lastGroup ? <div className="pg">{x.group}</div> : null;
            lastGroup = x.group;
            return (
              <div key={i}>
                {head}
                <button className="pi" type="button" aria-selected={i === sel} onMouseMove={() => setSel(i)} onClick={() => runAt(i)}>
                  {x.icon}
                  <span className="g">
                    {x.title}
                    {x.sub && <span>{x.sub}</span>}
                  </span>
                </button>
              </div>
            );
          })}
        </div>
        <div className="pfoot">
          <span>{t("pal.select")}</span>
          <span>{t("pal.open")}</span>
          <span>{t("pal.esc")}</span>
        </div>
      </div>
    </>
  );
}
