import { useEffect, useMemo, useRef, useState, type CSSProperties } from "react";
import { Bot, Check, ChevronDown, ChevronRight, FolderInput, Lock, Users, Workflow, FolderPlus, Hash, LifeBuoy, LogOut, Moon, Plus, Power, Search, Settings, Keyboard, MessageSquare } from "lucide-react";
import type { AgentView, SessionMeta, UsageStats } from "../api";
import { API, desktop, openExternal, setToken, teamOf, type TeamView } from "../api";
import { t } from "../i18n";
import { hhmm, kfmt, listTime, nextMorning, plainPreview, sinceLabel } from "../format";
import { takeoverReason } from "./Browser";
import { lastSnippet } from "../projection";
import {
  disconnectEvents,
  dndActive,
  myName,
  openNewAgent,
  isChat,
  newChat,
  openNewChannel,
  openSession,
  openSettings,
  patchProfile,
  refreshProfile,
  setState,
  setTheme,
  switchTeam,
  refreshTeams,
  toast,
  useStore,
  type Profile,
  type Theme,
} from "../store";
import { Avatar, channelColor, MeAvatar } from "./Avatar";
import { ListToggle } from "./Header";
import { RowMenuButton, RowMenuHost, openAddToFolder, openNewFolder, openRowMenu } from "./RowMenu";
import { Popover, useMenu } from "./Popover";
import { keyLabel } from "../shortcuts";
import { UpdatePill } from "./Update";

const ISSUES_URL = "https://github.com/hedeqiang/crewbase/issues";

export function SessionList() {
  const agents = useStore((s) => s.agents);
  const sessions = useStore((s) => s.sessions);
  const approvals = useStore((s) => s.approvals);
  const events = useStore((s) => s.events);
  const current = useStore((s) => s.current);
  const view = useStore((s) => s.view);
  const manage = useStore((s) => s.tier === "desktop");
  const connected = useStore((s) => s.connected);
  const profile = useStore((s) => s.profile);
  const [q, setQ] = useState("");
  const [folded, setFolded] = useState<Record<string, boolean>>({});
  const plus = useMenu();
  const me = useMenu();

  const summaries = useStore((s) => s.summaries);
  const channels = useStore((s) => s.channels);
  const savedFolders = useStore((s) => s.savedFolders);
  const team = useStore((s) => s.team);
  const teams = useStore((s) => s.teams);
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
    // sub-agents live in their parent's task panel, not in the list
    for (const a of list) if (!a.ephemeral && teamOf(a) === team && match(a.name, a.role, a.folder, summaries[a.session]?.topic)) get(a.folder ?? "").agents.push(a);
    const chs = Object.values(sessions)
      .filter((m) => m.kind === "channel" && teamOf(m) === team)
      .sort((a, b) => {
        const ar = channels[a.id]?.state === "archived" ? 1 : 0;
        const br = channels[b.id]?.state === "archived" ? 1 : 0;
        return ar - br || upd(b.id, b.created).localeCompare(upd(a.id, a.created));
      });
    for (const m of chs) if (match(m.title, m.folder, summaries[m.id]?.topic)) get(m.folder ?? "").channels.push(m);
    // empty folders the person created stay visible (not while searching)
    if (!ql) for (const f of savedFolders) get(f);
    // ungrouped first, then folders alphabetically
    return [...g.entries()].sort(([a], [b]) => (a === "" ? -1 : b === "" ? 1 : a.localeCompare(b)));
  }, [agents, sessions, summaries, channels, savedFolders, q, team]);

  const snippet = (sid: string): { text: string; time: string; agent?: string } | null => {
    const sm = summaries[sid];
    const local = lastSnippet(events[sid]);
    const s = sm?.last && (!local || sm.last.time >= local.time) ? sm.last : local ?? (sm ? { text: "", time: sm.updated } : null);
    return s && { ...s, text: plainPreview(s.text) };
  };

  const takeovers = useMemo(() => Object.values(agents).filter((a) => !!takeoverReason(a)), [agents]);
  const pendingBy = useMemo(() => {
    const m: Record<string, number> = {};
    for (const r of approvals) m[r.agent] = (m[r.agent] ?? 0) + 1;
    return m;
  }, [approvals]);

  const empty = Object.keys(agents).length === 0;
  const noMatch = !empty && groups.length === 0;
  const needs = approvals.length + takeovers.length;
  const sel = (sid: string) => view === "chat" && current === sid;
  const dnd = dndActive(profile);
  const host = profile.system?.hostname;

  const agentRow = (a: AgentView) => {
    const snip = snippet(a.session);
    const topic = summaries[a.session]?.topic;
    const waiting = (pendingBy[a.id] ?? 0) > 0 || !!takeoverReason(a);
    return (
      <button key={a.id} className="ci" data-session={a.session} type="button" aria-current={sel(a.session)} onClick={() => openSession(a.session)} onContextMenu={(e) => openRowMenu(e, { kind: "agent", agent: a })}>
        <Avatar agent={a} size={32} status={!isChat(a)} />
        <span className="ct">
          <span className="l1">
            <b>{isChat(a) ? topic || a.name : a.name}</b>
            {waiting && <span className="wchip">{t("list.wait")}</span>}
            <span className="tm">{snip?.time ? listTime(snip.time) : ""}</span>
          </span>
          <span className="l2">{(isChat(a) ? snip?.text : topic || snip?.text || a.role) || " "}</span>
        </span>
        <RowMenuButton target={{ kind: "agent", agent: a }} />
      </button>
    );
  };

  const channelRow = (m: SessionMeta) => {
    const ch = channels[m.id];
    const owner = ch ? agents[ch.owner] : undefined;
    const snip = snippet(m.id);
    const who = snip?.agent ? agents[snip.agent]?.name : undefined;
    const topic = summaries[m.id]?.topic;
    return (
      <button key={m.id} className={"ci" + (ch?.state === "archived" ? " arch" : "")} data-session={m.id} type="button" aria-current={sel(m.id)} onClick={() => openSession(m.id)} onContextMenu={(e) => openRowMenu(e, { kind: "channel", meta: m })}>
        <span className="chav tint" style={{ ["--c" as string]: channelColor(m.id) } as CSSProperties}>
          <Hash size={16} strokeWidth={2.4} />
          {owner && (
            <span className="chown">
              <Avatar agent={owner} size={14} />
            </span>
          )}
        </span>
        <span className="ct">
          <span className="l1">
            <b>{m.title || m.id}</b>
            {ch?.state === "paused" && <span className="pill sm">{t("ch.paused")}</span>}
            {ch?.state === "archived" && <span className="pill sm">{t("ch.archived")}</span>}
            <span className="tm">{snip?.time ? listTime(snip.time) : ""}</span>
          </span>
          <span className="l2">
            {topic ? (
              topic
            ) : (
              <>
                {who && snip?.text ? <span className="who">{who}：</span> : null}
                {snip?.text || (owner ? t("ch.ownerIs", { name: owner.name }) : " ")}
              </>
            )}
          </span>
        </span>
        <RowMenuButton target={{ kind: "channel", meta: m }} />
      </button>
    );
  };

  return (
    <aside className={"list" + (desktop() ? "" : " web")}>
      <RowMenuHost />
      {/* desktop: a bar under the window controls; web: the toggle is in the
          conversation header, mirroring the right-panel toggle */}
      {desktop() && (
        <div className="lbar">
          <ListToggle />
        </div>
      )}
      {teams.length > 1 && <TeamSwitcher teams={teams} current={team} />}
      <div className="lhead">
        <label className="search">
          <Search size={14} />
          <input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("list.search")} />
        </label>
        {manage && (
          <button className="ib bordered" type="button" ref={plus.ref} onClick={plus.toggle} aria-label={t("list.new")} title={t("list.new")} aria-haspopup="menu">
            <Plus size={16} />
          </button>
        )}
      </div>
      <div className="lscroll">
        {needs > 0 && !q && (
          <>
            <div className="lsec">
              <span>{t("list.needsYou")}</span>
              <span className="cnt amber">{needs}</span>
            </div>
            {takeovers.map((a) => (
              <button key={"tk" + a.id} className="ci" type="button" aria-current={false} onClick={() => openSession(a.session)}>
                <Avatar agent={a} size={32} status />
                <span className="ct">
                  <span className="l1">
                    <b>{a.name}</b>
                    <span className="wchip">{t("list.waitTakeover")}</span>
                    <span className="tm" />
                  </span>
                  <span className="l2">{takeoverReason(a)}</span>
                </span>
              </button>
            ))}
            {approvals.map((r) => {
              const a = agents[r.agent];
              return (
                <button key={r.callId} className="ci" type="button" aria-current={false} onClick={() => openSession(r.session)}>
                  {a ? (
                    <Avatar agent={a} size={32} status />
                  ) : (
                    <span className="chav">
                      <Workflow size={16} />
                    </span>
                  )}
                  <span className="ct">
                    <span className="l1">
                      <b>{a?.name ?? (r.agent || t("appr.workflow"))}</b>
                      <span className="wchip">{t("list.waitApprove")}</span>
                      <span className="tm">{listTime(r.asked)}</span>
                    </span>
                    <span className="l2">{r.summary || r.tool}</span>
                  </span>
                </button>
              );
            })}
          </>
        )}
        {groups.map(([folder, g]) => {
          // blank chats are conversations, not team members: their own section
          const crew = g.agents.filter((a) => !isChat(a));
          const chats = g.agents.filter((a) => isChat(a));
          const both = !folder && [g.channels.length, crew.length, chats.length].filter((n) => n > 0).length > 1;
          return (
            <div key={folder || "_"} className="lgrp">
              {folder && (
                <button
                  className="lsec grp"
                  type="button"
                  aria-expanded={!folded[folder]}
                  onClick={() => setFolded((f) => ({ ...f, [folder]: !f[folder] }))}
                  onContextMenu={(e) => openRowMenu(e, { kind: "folder", name: folder })}
                >
                  <ChevronRight size={12} className="fold" />
                  <span>{folder}</span>
                  <span className="cnt">{g.agents.length + g.channels.length}</span>
                  <RowMenuButton target={{ kind: "folder", name: folder }} />
                </button>
              )}
              {both && (
                <div className="lsec">
                  <span>{t("list.channels")}</span>
                  <span className="cnt">{g.channels.length}</span>
                </div>
              )}
              {!folded[folder] && g.channels.map(channelRow)}
              {both && crew.length > 0 && (
                <div className="lsec">
                  <span>{t("list.agents")}</span>
                  <span className="cnt">{crew.length}</span>
                </div>
              )}
              {!folded[folder] && (folder ? g.agents : crew).map(agentRow)}
              {!folder && both && chats.length > 0 && (
                <div className="lsec">
                  <span>{t("list.chats")}</span>
                  <span className="cnt">{chats.length}</span>
                </div>
              )}
              {!folder && !folded[folder] && chats.map(agentRow)}
              {folder && !folded[folder] && g.agents.length + g.channels.length === 0 && (
                <div className="lfempty">
                  <span>{t("list.emptyFolder")}</span>
                  {manage && (
                    <span className="lfacts">
                      <button type="button" className="chipbtn" onClick={() => void newChat(folder)}>
                        <MessageSquare size={12} />
                        {t("list.newChat")}
                      </button>
                      <button type="button" className="chipbtn" onClick={() => openNewAgent(folder)}>
                        <Bot size={12} />
                        {t("list.newAgent")}
                      </button>
                      <button type="button" className="chipbtn" onClick={() => openAddToFolder(folder)}>
                        <FolderInput size={12} />
                        {t("rm.folderAddExisting")}
                      </button>
                    </span>
                  )}
                </div>
              )}
            </div>
          );
        })}
        {empty && (
          <div className="lempty">
            <p className="small">{t("list.empty")}</p>
          </div>
        )}
        {noMatch && <p className="small lempty">{t("list.noMatch")}</p>}
      </div>
      <UpdatePill />
      <div className="lfoot">
        <button className="mebtn" type="button" ref={me.ref} onClick={me.toggle} aria-haspopup="menu" aria-expanded={me.open}>
          <span className="mewrap">
            <MeAvatar size={28} label={Array.from(myName(profile))[0]} />
            {dnd && (
              <span className="moon" title={t("me.dndUntil", { time: hhmm(profile.dndUntil) })}>
                <Moon size={9} />
              </span>
            )}
          </span>
          <span className="g">
            <b>{myName(profile)}</b>
            <span>
              {!connected && <i className="hdot" />}
              {dnd ? t("me.dndUntil", { time: hhmm(profile.dndUntil) }) : host ? t("me.subHost", { host }) : t("me.subLocal")}
            </span>
          </span>
        </button>
        <button
          className="ib"
          type="button"
          aria-label={t("list.settings")}
          title={t("list.settings") + " (" + keyLabel("settings") + ")"}
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
            void newChat();
          }}
        >
          <MessageSquare size={16} />
          <span className="g">
            {t("list.newChat")}
            <span>{t("list.newChatDesc")}</span>
          </span>
          <kbd>{keyLabel("newChat")}</kbd>
        </button>
        <button
          className="mi"
          type="button"
          onClick={() => {
            plus.close();
            openNewAgent();
          }}
        >
          <Bot size={16} />
          <span className="g">
            {t("list.newAgent")}
            <span>{t("list.newAgentDesc")}</span>
          </span>
          <kbd>{keyLabel("newAgent")}</kbd>
        </button>
        <button
          className="mi"
          type="button"
          disabled={!Object.values(agents).some((a) => !isChat(a))}
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
          <kbd>{keyLabel("newChannel")}</kbd>
        </button>
        <button
          className="mi"
          type="button"
          onClick={() => {
            plus.close();
            openNewFolder();
          }}
        >
          <FolderPlus size={16} />
          <span className="g">
            {t("list.newFolder")}
            <span>{t("list.newFolderDesc")}</span>
          </span>
        </button>
      </Popover>

      <Popover anchor={me.anchor} open={me.open} onClose={me.close} placement="top" width={280} className="memenu">
        <MeMenu close={me.close} profile={profile} />
      </Popover>
    </aside>
  );
}

function todayKey() {
  return new Date().toISOString().slice(0, 10);
}

function usd(n: number) {
  return "$" + n.toFixed(n < 10 ? 2 : 0);
}

function MeMenu({ close, profile }: { close: () => void; profile: Profile }) {
  const [usage, setUsage] = useState<UsageStats | null>(null);
  const [editing, setEditing] = useState(false);
  const [name, setName] = useState(profile.displayName);
  const [dndOpen, setDndOpen] = useState(false);
  const input = useRef<HTMLInputElement>(null);
  const shell = desktop();
  const sys = profile.system;
  const dnd = dndActive(profile);

  useEffect(() => {
    void refreshProfile();
    API.usage().then(setUsage, () => {});
  }, []);
  useEffect(() => {
    if (editing) input.current?.select();
  }, [editing]);

  const saveName = () => {
    setEditing(false);
    const v = name.trim();
    if (v !== profile.displayName) void patchProfile({ displayName: v });
  };
  const setDnd = (until: Date | null) => {
    setDndOpen(false);
    void patchProfile({ dndUntil: until ? until.toISOString() : "" });
  };

  const version = sys?.version || shell?.version;
  const sub = [version ? t("me.version", { version }) : "", sys?.startedAt ? t("me.uptime", { time: sinceLabel(sys.startedAt) }) : ""].filter(Boolean).join(" · ");
  const today = usage?.days?.[todayKey()] ?? 0;

  return (
    <>
      <div className="mehead">
        <MeAvatar size={36} label={Array.from(myName(profile))[0]} />
        <div className="g">
          {editing ? (
            <input
              ref={input}
              className="mename"
              value={name}
              maxLength={40}
              placeholder={t("me.namePh")}
              aria-label={t("me.namePh")}
              onChange={(e) => setName(e.target.value)}
              onBlur={saveName}
              onKeyDown={(e) => {
                if (e.key === "Enter") saveName();
                if (e.key === "Escape") {
                  e.stopPropagation();
                  e.preventDefault();
                  setName(profile.displayName);
                  setEditing(false);
                }
              }}
            />
          ) : (
            <button
              className="mename"
              type="button"
              title={t("me.rename")}
              onClick={() => {
                setName(profile.displayName);
                setEditing(true);
              }}
            >
              {myName(profile)}
            </button>
          )}
          <span className="small">{sub || (sys?.hostname ? t("me.subHost", { host: sys.hostname }) : t("me.subLocal"))}</span>
        </div>
      </div>

      <button
        className="mestats"
        type="button"
        onClick={() => {
          close();
          openSettings("profile");
        }}
      >
        <span className="lbl">
          {t("me.today")}
          <ChevronRight size={12} />
        </span>
        <span className="st">
          <b>{usage ? kfmt(today) : "—"}</b>
          <span>{t("me.todayTokens")}</span>
        </span>
        <span className="st">
          <b>{usage ? usage.switches : "—"}</b>
          <span>{t("me.todaySwitches")}</span>
        </span>
        <span className="st">
          <b>{usage ? usd(usage.savedUsd) : "—"}</b>
          <span>{t("me.todaySaved")}</span>
        </span>
      </button>

      <hr />
      <button className="mi" type="button" aria-expanded={dndOpen} onClick={() => setDndOpen(!dndOpen)}>
        <Moon size={16} />
        <span className="g">
          {dnd ? t("me.dndUntil", { time: hhmm(profile.dndUntil) }) : t("me.dnd")}
          <span>{t("me.dndNote")}</span>
        </span>
        <span className="tg" role="switch" aria-checked={dnd} aria-hidden />
      </button>
      {dndOpen && (
        <div className="subm">
          <button className="mi" type="button" onClick={() => setDnd(new Date(Date.now() + 3600_000))}>
            <span className="g">{t("me.dnd1h")}</span>
          </button>
          <button className="mi" type="button" onClick={() => setDnd(nextMorning())}>
            <span className="g">{t("me.dndMorning")}</span>
          </button>
          <button className="mi" type="button" disabled={!dnd} onClick={() => setDnd(null)}>
            <span className="g">{t("me.dndOff")}</span>
            {!dnd && (
              <span className="ck">
                <Check size={14} />
              </span>
            )}
          </button>
        </div>
      )}
      <hr />
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
          openExternal(ISSUES_URL);
        }}
      >
        <LifeBuoy size={16} />
        <span className="g">{t("me.help")}</span>
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
        <kbd>{keyLabel("settings")}</kbd>
      </button>
      <hr />
      {shell?.quit ? (
        <button
          className="mi"
          type="button"
          onClick={() => {
            close();
            shell.quit?.();
          }}
        >
          <Power size={16} />
          <span className="g">
            {t("me.quit")}
            <span>{t("me.quitDesc")}</span>
          </span>
        </button>
      ) : (
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
          <span className="g">
            {t("me.logout")}
            <span>{t("me.logoutDesc")}</span>
          </span>
        </button>
      )}
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

/** Which team the sidebar shows (only when there is more than one). */
function TeamSwitcher({ teams, current }: { teams: TeamView[]; current: string }) {
  const m = useMenu();
  const manage = useStore((s) => s.tier === "desktop");
  const cur = teams.find((x) => x.id === current) ?? teams[0];
  return (
    <>
      <button className="teamsw" type="button" ref={m.ref} onClick={m.toggle} aria-haspopup="menu" title={t("teams.switch")}>
        <span className="tdot" style={{ background: cur.color || "var(--accent)" }}>
          {cur.name.slice(0, 1)}
        </span>
        <b>{cur.name}</b>
        {cur.visibility === "private" && <Lock size={12} />}
        <ChevronDown size={14} />
      </button>
      <Popover anchor={m.anchor} open={m.open} onClose={m.close} width={260}>
        <div className="mlbl">{t("teams.title")}</div>
        {teams.map((x) => (
          <button
            key={x.id}
            className="mi"
            type="button"
            onClick={() => {
              switchTeam(x.id);
              m.close();
            }}
          >
            <span className="tdot" style={{ background: x.color || "var(--accent)" }}>
              {x.name.slice(0, 1)}
            </span>
            <span className="g">
              {x.name}
              <span>{t("teams.counts", { agents: x.agents, channels: x.channels })}</span>
            </span>
            {!x.canAct &&
              (x.visibility === "public" ? (
                <span
                  className="btn sm"
                  role="button"
                  tabIndex={0}
                  onClick={(e) => {
                    e.stopPropagation();
                    API.joinTeam(x.id).then(
                      () => {
                        toast(t("teams.joined", { name: x.name }));
                        void refreshTeams();
                      },
                      (err) => toast(err?.message ?? String(err)),
                    );
                  }}
                >
                  {t("teams.join")}
                </span>
              ) : (
                <span className="ro">{t("teams.readOnly")}</span>
              ))}
            <span className="ck">{x.id === current && <Check size={14} />}</span>
          </button>
        ))}
        {manage && (
          <>
            <hr />
            <button
              className="mi"
              type="button"
              onClick={() => {
                m.close();
                openSettings("teams");
              }}
            >
              <Users size={16} />
              <span className="g">{t("teams.manage")}</span>
            </button>
          </>
        )}
      </Popover>
    </>
  );
}
