// 插件与技能 › 已安装: installed servers, skills and plugins, with their menus.

import { Skeleton } from "../../Skeleton";
import { useEffect, useMemo, useRef, useState, type CSSProperties, type MouseEvent } from "react";
import { createPortal } from "react-dom";
import { BadgeCheck, LogIn, MoreHorizontal, Plus, ShieldAlert, Timer } from "lucide-react";
import { API, type McpServer } from "../../../api";
import { t, type Key } from "../../../i18n";
import { toast, useStore } from "../../../store";
import { errMsg, MCP_TONE, McpForm, McpSkills, McpTools, PluginCards, stateLabel } from "../ExtParts";

// Brand icons from LobeHub Icons (@lobehub/icons-static-svg, MIT), as in
// ProviderMark, where the package has the mark; the others are the
// companies' own marks from their brand pages or sites (src/assets/brands).
// Anything else gets a letter tile.
import { confirmDialog } from "../../Confirm";
import { ItemIcon, MANY_TOOLS, catalogAPI, isDocsServer, matches, needsReauth, tx, verLabel, type CatItem, type Shared } from "./shared";

// ---------------------------------------------------------------- installed

export interface MenuItem {
  label: string;
  danger?: boolean;
  disabled?: boolean;
  onClick: () => void;
}

/** ⋯ button with a small menu (fixed, so it escapes scrolling lists). */
export function MoreMenu({ items }: { items: MenuItem[] }) {
  const [at, setAt] = useState<{ x: number; y: number } | null>(null);
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!at) return;
    const close = (e: Event) => {
      if (ref.current && e.target instanceof Node && ref.current.contains(e.target)) return;
      setAt(null);
    };
    const esc = (e: KeyboardEvent) => e.key === "Escape" && setAt(null);
    window.addEventListener("mousedown", close);
    window.addEventListener("keydown", esc);
    window.addEventListener("resize", close);
    return () => {
      window.removeEventListener("mousedown", close);
      window.removeEventListener("keydown", esc);
      window.removeEventListener("resize", close);
    };
  }, [at]);
  const open = (e: MouseEvent) => {
    const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
    setAt(at ? null : { x: r.right, y: r.bottom + 4 });
  };
  return (
    <>
      <button className="ix" type="button" aria-label={t("mk.more")} title={t("mk.more")} aria-haspopup="menu" aria-expanded={!!at} onClick={open}>
        <MoreHorizontal size={15} />
      </button>
      {at &&
        createPortal(
          <div ref={ref} className="menu" role="menu" style={{ top: at.y, right: Math.max(8, window.innerWidth - at.x), minWidth: 180 } as CSSProperties}>
            {items.map((m) => (
              <button
                key={m.label}
                type="button"
                role="menuitem"
                className={"mi" + (m.danger ? " dng" : "")}
                disabled={m.disabled}
                onClick={() => {
                  setAt(null);
                  m.onClick();
                }}
              >
                {m.label}
              </button>
            ))}
          </div>,
          document.body,
        )}
    </>
  );
}

/** A connected server's status in words: 已连接 / 需要重新登录 / 出错 … */
export function stLabel(s: McpServer) {
  if (needsReauth(s)) return t("mk2.reauth");
  const k = `ext.mcpSt.${s.status?.state}` as Key;
  return t(k) === k ? s.status?.state || "—" : t(k);
}

export function InstalledView({ d, q, reload, reloadMarket, onApprove, onSignIn, onScope, onAccess, focus }: Shared) {
  const agents = useStore((s) => s.agents);
  const teams = useStore((s) => s.teams);
  const [adding, setAdding] = useState(false);
  const [open, setOpen] = useState<string | null>(focus);
  const [busy, setBusy] = useState<string | null>(null);
  const byServer = useMemo(() => {
    const m: Record<string, CatItem> = {};
    for (const it of d.catalog?.items ?? []) if (it.kind === "mcp" && it.server && !m[it.server]) m[it.server] = it;
    return m;
  }, [d.catalog]);
  const builtinSkills = useMemo(() => new Map((d.catalog?.items ?? []).filter((it) => it.kind === "skill").map((it) => [it.name, it])), [d.catalog]);
  const sharedDir = (d.skills ?? []).find((s) => !s.agent)?.source;
  const localSkillNames = useMemo(() => new Set((d.skills ?? []).map((s) => s.name)), [d.skills]);
  useEffect(() => {
    if (focus) document.getElementById("mk-srv-" + focus)?.scrollIntoView({ block: "center" });
  }, [focus, d.servers === null]);

  const scopeLabel = (scope?: string) =>
    !scope || scope === "global"
      ? t("ext.scopeGlobal")
      : scope.startsWith("team:")
        ? t("ext.scopeTeam", { name: teams.find((x) => "team:" + x.id === scope)?.name ?? scope.slice(5) })
        : t("ext.scopeAgent", { name: agents[scope]?.name ?? scope });

  const run = async (key: string, f: () => Promise<unknown>, ok?: string) => {
    setBusy(key);
    try {
      await f();
      if (ok) toast(ok);
      reload();
    } catch (e) {
      toast(errMsg(e));
    } finally {
      setBusy(null);
    }
  };

  const servers = (d.servers ?? []).filter((s) => !isDocsServer(s.name)).filter((s) => matches(q, s.name, byServer[s.name]?.name, s.url, (s.command ?? []).join(" ")));
  const skills = (d.skills ?? []).filter((s) => matches(q, s.name, s.description));
  const plugins = (d.plugins ?? []).filter((p) => matches(q, p.name, p.description));
  const loading = d.servers === null || d.skills === null || d.plugins === null;
  if (loading) return <Skeleton />;

  return (
    <>
      <section className="mksec">
        <div className="mksh">
          <h3>
            {t("mk2.instServices")} <span className="mkcount">{servers.length}</span>
          </h3>
          <span className="sp" />
          {!adding && (
            <button className="btn sm" type="button" onClick={() => setAdding(true)}>
              <Plus size={12} />
              {t("mk.custom")}
            </button>
          )}
        </div>
        {adding && (
          <McpForm
            onCancel={() => setAdding(false)}
            onSaved={(s) => {
              setAdding(false);
              setOpen(s.name);
              reload();
            }}
          />
        )}
        {servers.length === 0 ? (
          !adding && <p className="small">{t("mk2.noneServices")}</p>
        ) : (
          <div className="mklist">
            {servers.map((s) => {
              const it = byServer[s.name];
              const label = it?.name ?? s.name;
              const st = s.status?.state;
              const reauth = needsReauth(s);
              return (
                <div className={"mkirow" + (s.enabled ? "" : " off")} key={s.name} id={"mk-srv-" + s.name}>
                  <div className="mkrow">
                    <ItemIcon icon={it?.icon} id={s.name} label={label} kind={it ? it.kind : "server"} />
                    <div className="g">
                      <b>
                        {label}
                        <span className={"pill sm " + (reauth ? "amber" : MCP_TONE[st] ?? "")}>{stLabel(s)}</span>
                      </b>
                      <span className="mkmeta">
                        {s.status?.version && <span className="mkver">{verLabel(s.status.version)}</span>}
                        {scopeLabel(s.scope)} · {t("ext.toolsN", { n: s.status?.tools ?? 0 })}
                        {(s.status?.skills ?? 0) > 0 && (
                          <>
                            {" · "}
                            <button className="linkbtn" type="button" onClick={() => setOpen(s.name)}>
                              {t("ext.skillsN", { n: s.status.skills ?? 0 })}
                            </button>
                          </>
                        )}
                        {s.status?.tasks && (
                          <span className="mktasks" title={t("ext.mcpTasksHint")}>
                            {" · "}
                            <Timer size={11} aria-hidden /> {t("ext.mcpTasks")}
                          </span>
                        )}
                        {(s.transport === "http" || (s.command ?? []).some((a) => /@latest$/.test(a))) && (
                          <span className="autoupd" title={s.transport === "http" ? t("aup.autoRemote") : t("aup.autoNpx")}>
                            {" · "}
                            {t("aup.auto")}
                          </span>
                        )}
                        {it?.name ? "" : (
                          <span className="mono" title={s.transport === "http" ? s.url : (s.command ?? []).join(" ")}>
                            {" · "}
                            {s.transport === "http" ? s.url : (s.command ?? []).join(" ")}
                          </span>
                        )}
                      </span>
                    </div>
                    {st === "login" && s.status.loginUrl && (
                      <a className="btn pri sm" href={s.status.loginUrl} target="_blank" rel="noreferrer noopener">
                        <LogIn size={12} /> {t("ext.mcpLogin")}
                      </a>
                    )}
                    {reauth && it?.auth === "device" && (
                      <button className="btn pri sm" type="button" onClick={() => onSignIn(it)}>
                        <LogIn size={12} /> {t("mk2.signInAgain")}
                      </button>
                    )}
                    <button
                      className="tg"
                      type="button"
                      role="switch"
                      aria-checked={s.enabled}
                      aria-label={t("auto.enabled")}
                      disabled={s.source === "config" || busy === s.name}
                      onClick={() => run(s.name, () => API.patchMcp(s.name, { enabled: !s.enabled }))}
                    />
                    <MoreMenu
                      items={[
                        { label: t("mk.tools"), onClick: () => setOpen(open === s.name ? null : s.name) },
                        { label: t("mk2.scopeMenu"), disabled: s.source === "config", onClick: () => onScope(s) },
                        { label: t("mk2.pickAgents"), onClick: () => onAccess(s) },
                        ...(s.oauth ? [{ label: t("ext.mcpSignOut"), onClick: () => run(s.name, () => API.mcpSignOut(s.name), t("ext.mcpSignedOut", { name: label })) }] : []),
                        {
                          label: t("mk2.disconnect"),
                          danger: true,
                          disabled: s.source === "config",
                          onClick: async () => {
                            if (await confirmDialog(t("mk2.disconnectConfirm", { name: label }))) void run(s.name, () => API.deleteMcp(s.name), t("mk2.disconnected", { name: label }));
                          },
                        },
                      ]}
                    />
                  </div>
                  {s.status?.error && <div className="small errt mkerr">{s.status.error}</div>}
                  {s.enabled && (!s.scope || s.scope === "global") && (s.status?.tools ?? 0) >= MANY_TOOLS && (
                    <div className="small mknotice">
                      <span>{t("mk2.globalMany", { name: label, n: s.status.tools })}</span>
                      <button className="linkbtn" type="button" onClick={() => onAccess(s)}>
                        {t("mk2.pickAgents")}
                      </button>
                    </div>
                  )}
                  {open === s.name && (
                    <div className="mkdetail">
                      <McpTools name={s.name} server={s} onProbed={reload} />
                      {(s.status?.skills ?? 0) > 0 && <McpSkills name={s.name} count={s.status.skills} localNames={localSkillNames} />}
                    </div>
                  )}
                </div>
              );
            })}
          </div>
        )}
      </section>

      <section className="mksec">
        <div className="mksh">
          <h3>
            {t("mk.instSkills")} <span className="mkcount">{skills.length}</span>
          </h3>
        </div>
        {skills.length === 0 ? (
          <p className="small">{t("mk.noneSkills")}</p>
        ) : (
          <div className="mklist">
            {skills.map((s) => {
              const b = builtinSkills.get(s.name);
              const removable = !!s.agent || s.source === sharedDir;
              return (
                <div className="mkirow" key={(s.agent ?? "") + "/" + s.name}>
                  <div className="mkrow">
                    <ItemIcon icon={b?.icon} id={s.name} label={s.name} kind="skill" />
                    <div className="g">
                      <b>
                        {s.name}
                        <span className="pill sm">{s.agent ? t("ext.scopeAgent", { name: agents[s.agent]?.name ?? s.agent }) : t("mk.skillAll")}</span>
                      </b>
                      <span title={t("mk.skillDir", { dir: s.dir })}>
                        {s.version && <span className="mkver">{verLabel(s.version)}</span>}
                        {(b && tx(b.desc)) || s.description}
                      </span>
                    </div>
                    <MoreMenu
                      items={[
                        {
                          label: removable ? t("mk.remove") : t("mk.skillManaged"),
                          danger: removable,
                          disabled: !removable,
                          onClick: async () => {
                            if (await confirmDialog(t("mk.removeSkill", { name: s.name }))) void run("skill:" + s.name, () => catalogAPI.removeSkill(s.name, s.agent), t("mk.removed", { name: s.name }));
                          },
                        },
                      ]}
                    />
                  </div>
                </div>
              );
            })}
          </div>
        )}
      </section>

      <section className="mksec">
        <div className="mksh">
          <h3>
            {t("mk.instPlugins")} <span className="mkcount">{plugins.length}</span>
          </h3>
        </div>
        {plugins.length === 0 ? (
          <p className="small">{t("mk.nonePlugins")}</p>
        ) : (
          <div className="mklist">
            {plugins.map((p) => {
              const pending = !p.approved || (p.needsApproval?.length ?? 0) > 0;
              return (
                <div className={"mkirow" + (p.enabled ? "" : " off")} key={p.name}>
                  <div className="mkrow">
                    <ItemIcon id={p.name} label={p.name} kind="plugin" />
                    <div className="g">
                      <b>
                        {p.name}
                        <span className={"pill sm " + (p.state === "running" || p.state === "ACTIVE" ? "ok" : p.error ? "red" : !p.approved ? "amber" : "")}>{stateLabel(p)}</span>
                        {p.publisher && (
                          <span className="pubok" title={p.integrity}>
                            <BadgeCheck size={12} />
                          </span>
                        )}
                      </b>
                      <span>
                        <span className="mkver">{verLabel(p.version)}</span>
                        {p.description || p.source}
                      </span>
                    </div>
                    {pending && (
                      <button className="btn warn sm" type="button" onClick={() => onApprove(p)}>
                        <ShieldAlert size={12} />
                        {t("ext.reviewPerms")}
                      </button>
                    )}
                    <button
                      className="tg"
                      type="button"
                      role="switch"
                      aria-checked={p.enabled}
                      aria-label={t("auto.enabled")}
                      disabled={busy === p.name}
                      onClick={() => run(p.name, () => API.pluginAction(p.name, p.enabled ? "disable" : "enable"))}
                    />
                    <MoreMenu
                      items={[
                        { label: t("mk.perms"), onClick: () => onApprove(p) },
                        { label: t("mk.reload"), disabled: !p.enabled, onClick: () => run(p.name, () => API.pluginAction(p.name, "reload")) },
                        {
                          label: t("mk.remove"),
                          danger: true,
                          onClick: async () => {
                            if (await confirmDialog(t("ext.removeConfirm", { name: p.name })))
                              void run(p.name, async () => {
                                await API.deletePlugin(p.name);
                                void reloadMarket();
                              });
                          },
                        },
                      ]}
                    />
                  </div>
                  {p.error?.includes("SANDBOX_UNAVAILABLE") && p.approved ? (
                    // no sandbox works on this computer: a person may let it run without one
                    <div className="small mkerr mknosb">
                      <span className="errt" title={p.error}>
                        {t("ext.noSandbox")}
                      </span>
                      <button
                        className="btn sm dng"
                        type="button"
                        disabled={busy === p.name}
                        onClick={async () => {
                          if (await confirmDialog({ text: t("ext.allowUnsandboxedConfirm", { name: p.name }), ok: t("ext.allowUnsandboxed"), danger: true }))
                            void run(p.name, () => API.approvePlugin(p.name, { ...(p.grant?.permissions ?? p.requested), unsandboxed: true }, p.grant?.trustFutureVersions ?? false));
                        }}
                      >
                        {t("ext.allowUnsandboxed")}
                      </button>
                    </div>
                  ) : (
                    p.error && <div className="small errt mkerr">{p.error}</div>
                  )}
                </div>
              );
            })}
          </div>
        )}
        <p className="small mkhint">{t("mk3.useHero")}</p>
      </section>
      <PluginCards />
    </>
  );
}
