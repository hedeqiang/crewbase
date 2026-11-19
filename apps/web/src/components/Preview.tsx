import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { AppWindow, Eye, ExternalLink, Monitor, RotateCw, Smartphone, Tablet, Unplug, X } from "lucide-react";
import { API, type AgentView, type PreviewInfo } from "../api";
import { t } from "../i18n";
import { getState, openPreview, sessionAgent, setState, toast, useStore } from "../store";

// 预览 (docs/previews.md): pages an agent made, framed from their own origin
// (a per-root static server on 127.0.0.1 or the dev server's address), never
// the app's. The frame's allow-same-origin is that origin, not this one.
const SANDBOX = "allow-scripts allow-same-origin allow-forms allow-popups allow-modals allow-downloads";

export const previewKey = (p: PreviewInfo) => (p.kind === "path" ? `p|${p.agent ?? ""}|${p.path ?? ""}` : `u|${p.url}`);

// per session, for this page's lifetime: what was opened from the app, the
// one shown, and the ones closed
const opened = new Map<string, PreviewInfo[]>();
const shown = new Map<string, string>();
const closed = new Map<string, Set<string>>();

type Width = "desktop" | "tablet" | "phone";
const WIDTHS: Record<Width, number> = { desktop: 0, tablet: 768, phone: 390 };
const WIDTH_KEY = "crewbase.previewWidth";
function savedWidth(): Width {
  try {
    const v = localStorage.getItem(WIDTH_KEY);
    return v === "tablet" || v === "phone" ? v : "desktop";
  } catch {
    return "desktop";
  }
}

/** host:port/path of a URL, for the address line. */
export function addrOf(u: string): string {
  try {
    const x = new URL(u);
    return x.host + (x.pathname === "/" ? "" : x.pathname) + x.search;
  } catch {
    return u;
  }
}

/** Something answers HTTP at u (an opaque no-cors response is enough;
 *  nothing listening rejects). */
async function answers(u: string): Promise<boolean> {
  const ctl = new AbortController();
  const tm = setTimeout(() => ctl.abort(), 5000);
  try {
    await fetch(u, { mode: "no-cors", cache: "no-store", credentials: "omit", signal: ctl.signal });
    return true;
  } catch {
    return false;
  } finally {
    clearTimeout(tm);
  }
}

function merge(server: PreviewInfo[], local: PreviewInfo[]): PreviewInfo[] {
  const all = [...local, ...server].sort((a, b) => (b.time || "").localeCompare(a.time || ""));
  const seen = new Set<string>();
  return all.filter((p) => {
    const k = previewKey(p);
    if (seen.has(k)) return false;
    seen.add(k);
    return true;
  });
}

/** The right panel's 预览 tab. */
export function PreviewTab({ session }: { session: string }) {
  const want = useStore((s) => s.previewOpen);
  // the session's preview/opened events: the list follows them
  const evs = useStore((s) => (s.events[session] ?? []).filter((e) => e.type === "preview/opened").length);
  const [server, setServer] = useState<PreviewInfo[]>([]);
  const [, bump] = useState(0);

  useEffect(() => {
    if (want && want.session === session) {
      const k = previewKey(want.item);
      const list = (opened.get(session) ?? []).filter((p) => previewKey(p) !== k);
      opened.set(session, [...list, { ...want.item, time: want.item.time || new Date().toISOString() }]);
      shown.set(session, k);
      closed.get(session)?.delete(k);
      setState({ previewOpen: null });
      bump((x) => x + 1);
    }
  }, [want, session]);

  useEffect(() => {
    let live = true;
    API.previews(session)
      .then((r) => live && setServer(r.previews ?? []))
      .catch(() => live && setServer([]));
    return () => {
      live = false;
    };
  }, [session, evs]);

  const gone = closed.get(session);
  const items = merge(server, opened.get(session) ?? []).filter((p) => !gone?.has(previewKey(p)));
  const curKey = shown.get(session);
  const cur = items.find((p) => previewKey(p) === curKey) ?? items[0];

  if (!cur)
    return (
      <div className="prvempty">
        <AppWindow size={22} />
        <p className="small">{t("preview.empty")}</p>
      </div>
    );
  const pick = (p: PreviewInfo) => {
    shown.set(session, previewKey(p));
    bump((x) => x + 1);
  };
  const close = () => {
    const k = previewKey(cur);
    if (!closed.has(session)) closed.set(session, new Set());
    closed.get(session)!.add(k);
    shown.delete(session);
    bump((x) => x + 1);
  };
  return (
    <div className="prview">
      {items.length > 1 && (
        <div className="prvlist" role="tablist" aria-label={t("preview.recent")}>
          {items.slice(0, 6).map((p) => (
            <button key={previewKey(p)} type="button" role="tab" className="prvchip" aria-selected={p === cur} title={p.kind === "path" ? p.path : p.url} onClick={() => pick(p)}>
              {p.title || addrOf(p.url)}
            </button>
          ))}
        </div>
      )}
      <Viewer key={previewKey(cur)} item={cur} onClose={close} />
    </div>
  );
}

function Viewer({ item, onClose }: { item: PreviewInfo; onClose: () => void }) {
  const [url, setUrl] = useState<string | null>(item.kind === "url" ? item.url : null);
  const [state, setSt] = useState<"loading" | "ok" | "down" | "failed">("loading");
  const [err, setErr] = useState("");
  const [tick, setTick] = useState(0); // reload / retry
  const [width, setWidth] = useState<Width>(savedWidth);
  const box = useRef<HTMLDivElement>(null);
  const [boxW, setBoxW] = useState(0);

  useEffect(() => {
    let live = true;
    setSt("loading");
    (async () => {
      let u = item.url;
      if (item.kind === "path" && item.agent && item.path) {
        // the static server may have stopped (idle, restart): reopen the path
        try {
          u = (await API.openPreview(item.agent, item.path)).url;
        } catch (e: any) {
          if (!live) return;
          setErr(e?.message ?? String(e));
          setSt("failed");
          return;
        }
      }
      if (!live) return;
      setUrl(u);
      const ok = await answers(u);
      if (live) setSt(ok ? "ok" : "down");
    })();
    return () => {
      live = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [item.kind, item.url, item.agent, item.path, item.time, tick]);

  // keep checking while it is on screen: a dev server that stopped turns the
  // tab into "not answering" (instead of a stale page), and one that comes
  // back reloads by itself; our static server, if it went idle, is reopened
  useEffect(() => {
    if (!url || (state !== "ok" && !(state === "down" && item.kind === "url"))) return;
    let live = true;
    let busy = false;
    const iv = window.setInterval(async () => {
      if (busy || document.hidden) return;
      busy = true;
      const ok = await answers(url);
      busy = false;
      if (!live) return;
      if (state === "ok" && !ok) {
        if (item.kind === "url") setSt("down");
        else setTick((x) => x + 1);
      } else if (state === "down" && ok) setTick((x) => x + 1);
    }, 5000);
    return () => {
      live = false;
      clearInterval(iv);
    };
  }, [url, state, item.kind]);

  useLayoutEffect(() => {
    const el = box.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setBoxW(el.clientWidth));
    ro.observe(el);
    setBoxW(el.clientWidth);
    return () => ro.disconnect();
  }, [state]);

  const setW = (w: Width) => {
    setWidth(w);
    try {
      localStorage.setItem(WIDTH_KEY, w);
    } catch {
      /* private window */
    }
  };
  // tablet / phone widths wider than the panel are scaled down to fit
  const want = WIDTHS[width];
  const scale = want && boxW && want > boxW ? boxW / want : 1;
  const addr = addrOf(url ?? item.url);
  const shown = url ?? item.url;
  return (
    <div className="prvwrap">
      <div className="prvbar">
        <div className="prvtitle">
          <b title={item.title}>{item.title || addr}</b>
          <span className="mono small" title={shown}>
            {addr}
          </span>
        </div>
        <button className="ix" type="button" aria-label={t("preview.reload")} title={t("preview.reload")} onClick={() => setTick((x) => x + 1)}>
          <RotateCw size={13} />
        </button>
        <a className="ix" href={shown} target="_blank" rel="noopener noreferrer" aria-label={t("preview.openBrowser")} title={t("preview.openBrowser")}>
          <ExternalLink size={13} />
        </a>
        <span className="prvseg" role="radiogroup" aria-label={t("preview.width")}>
          {(
            [
              ["desktop", Monitor, t("preview.desktop")],
              ["tablet", Tablet, t("preview.tablet")],
              ["phone", Smartphone, t("preview.phone")],
            ] as const
          ).map(([k, Icon, label]) => (
            <button key={k} type="button" role="radio" aria-checked={width === k} aria-label={label} title={label} onClick={() => setW(k)}>
              <Icon size={13} />
            </button>
          ))}
        </span>
        <button className="ix" type="button" aria-label={t("preview.close")} title={t("preview.close")} onClick={onClose}>
          <X size={13} />
        </button>
      </div>
      <div className="prvstage" ref={box}>
        {state === "loading" ? (
          <div className="prvmsg small muted">{t("preview.loading")}</div>
        ) : state === "down" || state === "failed" ? (
          <div className="prvmsg prverr">
            <Unplug size={20} />
            <p>{state === "down" ? (item.kind === "url" ? t("preview.down", { addr }) : t("preview.downStatic", { addr })) : t("preview.failed", { error: err })}</p>
            <button className="btn sm" type="button" onClick={() => setTick((x) => x + 1)}>
              {t("common.retry")}
            </button>
          </div>
        ) : (
          <div className="prvframe" style={want ? { width: want, transform: scale < 1 ? `scale(${scale})` : undefined, height: scale < 1 ? `${100 / scale}%` : "100%" } : undefined}>
            <iframe key={tick} src={shown} title={item.title || addr} sandbox={SANDBOX} referrerPolicy="no-referrer" allow="clipboard-write; fullscreen" />
          </div>
        )}
      </div>
    </div>
  );
}

/** The turn's line for preview/opened: 打开了预览：xx (opens it again). */
export function PreviewLine({ p, session }: { p: PreviewInfo; session: string }) {
  return (
    <button className="prvline" type="button" onClick={() => openPreview(session, { ...p, time: new Date().toISOString() })}>
      <Eye size={13} />
      <span>{t("preview.opened", { title: p.title || addrOf(p.url) })}</span>
    </button>
  );
}

function agentFor(session: string | null): AgentView | undefined {
  return sessionAgent(session);
}

/** Opens a workspace file or folder (the Files panel's 预览). */
export async function previewPath(agent: string, path: string, title?: string) {
  const session = getState().current ?? "";
  try {
    const p = await API.openPreview(agent, path, session || undefined, title);
    openPreview(session, p);
  } catch (e: any) {
    toast(t("preview.failed", { error: e?.message ?? String(e) }));
  }
}

/** Opens an ```html code block (saved under .crewbase/previews/ in the
 *  workspace of the conversation's agent). */
export async function previewSnippet(html: string) {
  const session = getState().current ?? "";
  const a = agentFor(session);
  if (!a) return;
  try {
    // the page's own <title> names the tab when it has one
    // (a parsed document runs no scripts; it also decodes &amp; and the like)
    const own = new DOMParser().parseFromString(html, "text/html").title.replace(/\s+/g, " ").trim();
    const p = await API.previewHTML(a.id, html, session || undefined, own ? own.slice(0, 60) : t("preview.snippet"));
    openPreview(session, p);
  } catch (e: any) {
    toast(t("preview.failed", { error: e?.message ?? String(e) }));
  }
}

const LOCAL_RE = /https?:\/\/(?:localhost|127\.0\.0\.1|0\.0\.0\.0|\[::1\])(?::\d{1,5})?(?:\/[^\s<>()`'"　-〿＀-￯]*)?/g;

/** localhost addresses in an answer (outside code), at most three. */
export function localLinks(text: string): string[] {
  const prose = text.replace(/(```|~~~)[\s\S]*?(\1|$)/g, " ").replace(/`[^`\n]*`/g, " ");
  const out: string[] = [];
  for (const m of prose.matchAll(LOCAL_RE)) {
    const u = m[0].replace(/[.,;:!?)\]]+$/, "").replace("//0.0.0.0", "//127.0.0.1");
    if (!out.includes(u)) out.push(u);
    if (out.length === 3) break;
  }
  return out;
}

// one address however it is written: 127.0.0.1:5173 and localhost:5173/
const norm = (u: string) => u.replace("//localhost", "//127.0.0.1").replace("//0.0.0.0", "//127.0.0.1").replace(/\/+$/, "");

/** 在右侧预览 cards for the localhost links of an answer (opened: ones the
 *  turn already showed with preview_open). */
export function LocalPreviewCards({ text, opened }: { text: string; opened?: string[] }) {
  const links = useMemo(() => {
    const seen = new Set((opened ?? []).map(norm));
    return localLinks(text).filter((u) => !seen.has(norm(u)));
  }, [text, opened?.join(" ")]);
  if (!links.length) return null;
  return (
    <div className="prvcards">
      {links.map((u) => (
        <button
          key={u}
          type="button"
          className="prvcard"
          onClick={() => {
            const session = getState().current ?? "";
            openPreview(session, { kind: "url", url: u, title: addrOf(u), agent: agentFor(session)?.id, source: "link", time: new Date().toISOString() });
          }}
        >
          <span className="prvcic">
            <AppWindow size={15} />
          </span>
          <span className="prvct">
            <b>{t("preview.inPanel")}</b>
            <span className="mono small">{addrOf(u)}</span>
          </span>
        </button>
      ))}
    </div>
  );
}
