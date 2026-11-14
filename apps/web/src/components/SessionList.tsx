import { useMemo, useState } from "react";
import { Bot, ChevronDown, Workflow, FolderPlus, Hash, LogOut, Plus, Search, Settings, User, Keyboard } from "lucide-react";
import type { AgentView, SessionMeta } from "../api";
import { setToken } from "../api";
import { t } from "../i18n";
import { ago, detailLabel, statusLabel, pillTone } from "../format";
import { takeoverReason } from "./Browser";
import { lastSnippet } from "../projection";
import { disconnectEvents, openNewAgent, openNewChannel, openSession, openSettings, setState, setTheme, useStore, type Theme } from "../store";
import { Avatar, MeAvatar } from "./Avatar";
import { Popover, useMenu } from "./Popover";

export function SessionList() {
  const agents = useStore((s) => s.agents);
  const sessions = useStore((s) => s.sessions);
  const approvals = useStore((s) => s.approvals);
  const events = useStore((s) => s.events);
  const current = useStore((s) => s.current);
  const view = useStore((s) => s.view);
  const connected = useStore((s) => s.connected);
  const [q, setQ] = useState("");
  const [folded, setFolded] = useState<Record<string, boolean>>({});
  const plus = useMenu();
  const me = useMenu();

  const summaries = useStore((s) => s.summaries);
  const channels = useStore((s) => s.channels);
  const groups = useMemo(() => {
    const ql = q.trim().toLowerCase();
    const match = (...xs: (string | undefined)[]) => !ql || xs.some((x) => x?.toLowerCase().includes(ql));
    const g = new Map<string, { agents: AgentView[]; channels: SessionMeta[] }>();
    const get = (f: string) => {
      if (!g.has(f)) g.set(f, { agents: [], channels: [] });
      return g.get(f)!;
    };
    // Most recently active first; fall back to creation order.
    const upd = (sid: string, created: string) => summaries[sid]?.updated ?? created;
    const list = Object.values(agents).sort((a, b) => upd(b.session, b.created).localeCompare(upd(a.session, a.created)));
    for (const a of list) if (match(a.name, a.role, a.folder, summaries[a.session]?.topic)) get(a.folder ?? "").agents.push(a);
    const chs = Object.values(sessions)
      .filter((m) => m.kind === "channel")
      .sort((a, b) => {
        const ar = channels[a.id]?.state === "archived" ? 1 : 0;
        const br = channels[b.id]?.state === "archived" ? 1 : 0;
        return ar - br || upd(b.id, b.created).localeCompare(upd(a.id, a.created));
      });
    for (const m of chs) if (match(m.title, m.folder, summaries[m.id]?.topic)) get(m.folder ?? "").channels.push(m);
    // ungrouped first, then folders alphabetically
    return [...g.entries()].sort(([a], [b]) => (a === "" ? -1 : b === "" ? 1 : a.localeCompare(b)));
  }, [agents, sessions, summaries, channels, q]);

  const snippet = (sid: string): { text: string; time: string; agent?: string } | null => {
    const sm = summaries[sid];
    const local = lastSnippet(events[sid]);
    if (sm?.last && (!local || sm.last.time >= local.time)) return sm.last;
    return local ?? (sm ? { text: "", time: sm.updated } : null);
  };

  const takeovers = useMemo(() => Object.values(agents).filter((a) => !!takeoverReason(a)), [agents]);
  const pendingBy = useMemo(() => {
    const m: Record<string, number> = {};
    for (const r of approvals) m[r.agent] = (m[r.agent] ?? 0) + 1;
    return m;
  }, [approvals]);

  const empty = Object.keys(agents).length === 0;
  const noMatch = !empty && groups.length === 0;

  return (
    <aside className="list">
      <div className="lhead">
        <label className="search">
          <Search size={15} />
          <input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("list.search")} />
        </label>
        <button className="ib" type="button" ref={plus.ref} onClick={plus.toggle} aria-label={t("list.new")} title={t("list.new")} aria-haspopup="menu">
          <Plus size={16} />
        </button>
      </div>
      <div className="lscroll">
        {approvals.length + takeovers.length > 0 && !q && (
          <>
            <div className="lsec">
              {t("list.needsYou")}
              <span className="cnt amber">{approvals.length + takeovers.length}</span>
            </div>
            {takeovers.map((a) => (
              <button key={"tk" + a.id} className="ci" type="button" aria-current={view === "chat" && current === a.session} onClick={() => openSession(a.session)}>
                <Avatar agent={a} size={36} status />
                <span style={{ minWidth: 0 }}>
                  <span className="ttl">
                    <b>{a.name}</b>
                    <span className="pill amber">{t("list.waitTakeover")}</span>
                  </span>
                  <span className="snip">{takeoverReason(a)}</span>
                </span>
                <span className="meta" />
              </button>
            ))}
            {approvals.map((r) => {
              const a = agents[r.agent];
              return (
                <button key={r.callId} className="ci" type="button" aria-current={view === "chat" && current === r.session} onClick={() => openSession(r.session)}>
                  {a ? <Avatar agent={a} size={36} status /> : <span className="chav"><Workflow size={18} /></span>}
                  <span style={{ minWidth: 0 }}>
                    <span className="ttl">
                      <b>{a?.name ?? (r.agent || t("appr.workflow"))}</b>
                      <span className="pill amber">{t("list.waitApprove")}</span>
                    </span>
                    <span className="snip">{r.summary || r.tool}</span>
                  </span>
                  <span className="meta">{ago(r.asked)}</span>
                </button>
              );
            })}
          </>
        )}
        {groups.map(([folder, g]) => (
          <div key={folder || "_"}>
            {folder && (
              <button className="lsec grp" type="button" onClick={() => setFolded((f) => ({ ...f, [folder]: !f[folder] }))}>
                {folder}
                <span className="fold" style={{ transform: folded[folder] ? "rotate(-90deg)" : undefined }}>
                  <ChevronDown size={14} />
                </span>
              </button>
            )}
            {!folder && g.channels.length > 0 && g.agents.length > 0 && <div className="lsec">{t("list.channels")}</div>}
            {!folded[folder] &&
              g.channels.map((m) => {
                const ch = channels[m.id];
                const owner = ch ? agents[ch.owner] : undefined;
                const snip = snippet(m.id);
                const who = snip?.agent ? agents[snip.agent]?.name : undefined;
                const topic = summaries[m.id]?.topic;
                return (
                  <button
                    key={m.id}
                    className={"ci" + (ch?.state === "archived" ? " arch" : "")}
                    type="button"
                    aria-current={view === "chat" && current === m.id}
                    onClick={() => openSession(m.id)}
                  >
                    <span className="chav">
                      <Hash size={18} />
                      {owner && (
                        <span className="chown">
                          <Avatar agent={owner} size={16} status />
                        </span>
                      )}
                    </span>
                    <span style={{ minWidth: 0 }}>
                      <span className="ttl">
                        <b>{m.title || m.id}</b>
                        {ch?.state === "paused" && <span className="pill">{t("ch.paused")}</span>}
                        {ch?.state === "archived" && <span className="pill">{t("ch.archived")}</span>}
                      </span>
                      {topic && <span className="topic">{topic}</span>}
                      <span className="snip">
                        {who ? <span className="who">{who}：</span> : null}
                        {snip?.text || (owner ? t("ch.ownerIs", { name: owner.name }) : t("list.channels"))}
                      </span>
                    </span>
                    <span className="meta">{snip?.time && <span>{ago(snip.time)}</span>}</span>
                  </button>
                );
              })}
            {!folder && g.channels.length > 0 && g.agents.length > 0 && <div className="lsec">{t("list.agents")}</div>}
            {!folded[folder] &&
              g.agents.map((a) => {
                const snip = snippet(a.session);
                const topic = summaries[a.session]?.topic;
                const pend = pendingBy[a.id] ?? 0;
                return (
                  <button key={a.id} className="ci" type="button" aria-current={view === "chat" && current === a.session} onClick={() => openSession(a.session)}>
                    <Avatar agent={a} size={36} status />
                    <span style={{ minWidth: 0 }}>
                      <span className="ttl">
                        <b>{a.name}</b>
                        {a.role && <span className="tag">{a.role}</span>}
                      </span>
                      {topic && <span className="topic">{topic}</span>}
                      <span className="snip">
                        {a.status !== "idle" ? <span className={"stx " + pillTone(a.status)}>{detailLabel(a.detail) || statusLabel(a.status)} · </span> : null}
                        {snip?.text || a.role || ""}
                      </span>
                    </span>
                    <span className="meta">
                      {snip && <span>{ago(snip.time)}</span>}
                      {pend > 0 && <span className="ub amber">{pend}</span>}
                    </span>
                  </button>
                );
              })}
          </div>
        ))}
        {empty && (
          <div className="lempty">
            <p className="small">{t("list.empty")}</p>
            <button className="btn pri" type="button" onClick={openNewAgent}>
              <Plus size={14} />
              {t("list.newAgent")}
            </button>
          </div>
        )}
        {noMatch && <p className="small lempty">{t("list.noMatch")}</p>}
      </div>
      <div className="lfoot">
        <button className="mebtn" type="button" ref={me.ref} onClick={me.toggle} aria-haspopup="menu">
          <MeAvatar size={26} label={t("common.you")} />
          <span className="g">
            <b>{t("common.you")}</b>
            <span>
              <i className={"hdot" + (connected ? " on" : "")} />
              {connected ? t("list.meSub") : t("list.meSubOffline")}
            </span>
          </span>
        </button>
        <button
          className="ib"
          type="button"
          aria-label={t("list.settings")}
          title={t("list.settings")}
          aria-pressed={view === "settings"}
          onClick={() => (view === "settings" ? setState({ view: "chat" }) : openSettings("general"))}
        >
          <Settings size={16} />
        </button>
      </div>

      <Popover anchor={plus.anchor} open={plus.open} onClose={plus.close} align="end" width={240}>
        <button
          className="mi"
          type="button"
          onClick={() => {
            plus.close();
            openNewAgent();
          }}
        >
          <Bot size={16} />
          <span className="g">{t("list.newAgent")}</span>
          <kbd>⌘N</kbd>
        </button>
        <button
          className="mi"
          type="button"
          disabled={Object.keys(agents).length === 0}
          onClick={() => {
            plus.close();
            openNewChannel();
          }}
        >
          <Hash size={16} />
          <span className="g">
            {t("list.newChannel")}
            <span>{t("list.newChannelDesc")}</span>
          </span>
        </button>
        <button className="mi" type="button" disabled>
          <FolderPlus size={16} />
          <span className="g">
            {t("list.newFolder")}
            <span>{t("common.soon")}</span>
          </span>
        </button>
      </Popover>

      <Popover anchor={me.anchor} open={me.open} onClose={me.close} placement="top" width={260}>
        <MeMenu close={me.close} />
      </Popover>
    </aside>
  );
}

function MeMenu({ close }: { close: () => void }) {
  const theme = useStore((s) => s.theme);
  return (
    <>
      <div className="mehead">
        <MeAvatar size={32} label={t("common.you")} />
        <div>
          <b>{t("common.you")}</b>
          <span className="small">{t("list.meSub")}</span>
        </div>
      </div>
      <div className="mlbl">{t("me.appearance")}</div>
      <ThemeSeg theme={theme} />
      <hr />
      <button
        className="mi"
        type="button"
        onClick={() => {
          close();
          openSettings("profile");
        }}
      >
        <User size={16} />
        <span className="g">{t("me.profile")}</span>
      </button>
      <button
        className="mi"
        type="button"
        onClick={() => {
          close();
          openSettings("keys");
        }}
      >
        <Keyboard size={16} />
        <span className="g">{t("me.shortcuts")}</span>
      </button>
      <button
        className="mi"
        type="button"
        onClick={() => {
          close();
          openSettings("general");
        }}
      >
        <Settings size={16} />
        <span className="g">{t("me.allSettings")}</span>
      </button>
      <hr />
      <button
        className="mi"
        type="button"
        onClick={() => {
          close();
          disconnectEvents();
          setToken(null);
          setState({ authed: false, connected: false });
        }}
      >
        <LogOut size={16} />
        <span className="g">{t("me.logout")}</span>
      </button>
    </>
  );
}

export function ThemeSeg({ theme }: { theme: Theme }) {
  return (
    <div className="seg">
      {(["system", "light", "dark"] as const).map((v) => (
        <button key={v} type="button" aria-pressed={theme === v} onClick={() => setTheme(v)}>
          {t(`theme.${v}`)}
        </button>
      ))}
    </div>
  );
}
