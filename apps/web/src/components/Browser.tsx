import { useEffect, useRef, useState } from "react";
import { ArrowDown, ArrowUp, CornerDownLeft, Globe, Hand, MousePointerClick, Undo2 } from "lucide-react";
import { API, ApiError, type AgentView, type Screenshot } from "../api";
import { t } from "../i18n";
import { setState, toast, useStore } from "../store";

/** Detail of a waiting agent that asks a person to take over its browser. */
export function takeoverReason(a?: AgentView): string | null {
  if (!a || a.status !== "waiting" || !a.detail?.startsWith("takeover:")) return null;
  return a.detail.slice("takeover:".length) || "login";
}

const KEYS = ["Enter", "Tab", "Backspace", "Escape"] as const;

/** 浏览器接管卡 (docs/03 §6 4.4): take over → frame view → hand back. */
export function TakeoverCard({ agent, reason }: { agent: AgentView; reason: string }) {
  const st = useStore((s) => s.browser[agent.id]);
  const [busy, setBusy] = useState(false);
  const [other, setOther] = useState(false);

  useEffect(() => {
    API.browser(agent.id)
      .then((b) => setState((s) => ({ browser: { ...s.browser, [agent.id]: b } })))
      .catch(() => {});
  }, [agent.id]);

  // The server says whether the takeover is ours (same caller identity).
  const held = !!st?.takenOverBy;
  const active = held && (st?.mine ?? !other);

  const takeover = async () => {
    setBusy(true);
    try {
      const b = await API.takeover(agent.id);
      setState((s) => ({ browser: { ...s.browser, [agent.id]: b } }));
      setOther(false);
    } catch (e: any) {
      if (e instanceof ApiError && e.status === 403) setOther(true);
      toast(t("br.failed", { error: e?.message ?? String(e) }));
    } finally {
      setBusy(false);
    }
  };
  const handback = async () => {
    setBusy(true);
    try {
      const b = await API.handback(agent.id);
      setState((s) => ({ browser: { ...s.browser, [agent.id]: b } }));
      toast(t("br.handedBack", { name: agent.name }));
    } catch (e: any) {
      toast(t("br.failed", { error: e?.message ?? String(e) }));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className={"qcard brcard wait" + (active ? " live" : "")}>
      <h4>
        <Globe size={15} />
        {t("br.title", { name: agent.name })}
      </h4>
      <div className="qs">{t("br.reason", { reason, name: agent.name })}</div>
      {st?.url && !active && <code className="cmd">{st.url}</code>}
      {active ? (
        st?.view === "window" ? (
          <p className="small" style={{ margin: "10px 0 0" }}>
            {t("br.window")}
          </p>
        ) : (
          <FrameView agentId={agent.id} onForbidden={() => setOther(true)} />
        )
      ) : (
        <p className="small" style={{ margin: "8px 0 0" }}>
          {other ? t("br.other") : t("br.privacy")}
        </p>
      )}
      <div className="acts">
        {active ? (
          <button className="btn pri" type="button" disabled={busy} onClick={handback}>
            <Undo2 size={13} />
            {t("br.handback")}
          </button>
        ) : (
          <button className="btn blue" type="button" disabled={busy} onClick={takeover}>
            <Hand size={13} />
            {t("br.takeover")}
          </button>
        )}
      </div>
    </div>
  );
}

function FrameView({ agentId, onForbidden }: { agentId: string; onForbidden: () => void }) {
  const [shot, setShot] = useState<Screenshot | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [url, setUrl] = useState("");
  const [text, setText] = useState("");
  const [dot, setDot] = useState<{ x: number; y: number } | null>(null);
  const editing = useRef(false);
  const img = useRef<HTMLImageElement>(null);
  const wheel = useRef(0);

  // ~2 fps; each request waits for the previous one.
  useEffect(() => {
    let stop = false;
    let timer: number | undefined;
    const tick = async () => {
      try {
        const s = await API.screenshot(agentId);
        if (stop) return;
        setShot(s);
        setErr(null);
        if (!editing.current && s.url) setUrl(s.url);
      } catch (e: any) {
        if (stop) return;
        if (e instanceof ApiError && e.status === 403) onForbidden();
        setErr(e?.message ?? String(e));
      }
      if (!stop) timer = window.setTimeout(tick, 500);
    };
    void tick();
    return () => {
      stop = true;
      window.clearTimeout(timer);
    };
  }, [agentId]);

  const send = async (b: Parameters<typeof API.browserInput>[1]) => {
    try {
      await API.browserInput(agentId, b);
    } catch (e: any) {
      toast(t("br.failed", { error: e?.message ?? String(e) }));
    }
  };

  const click = (e: React.MouseEvent<HTMLImageElement>) => {
    const el = img.current;
    if (!el || !shot) return;
    const r = el.getBoundingClientRect();
    const w = shot.width || el.naturalWidth;
    const h = shot.height || el.naturalHeight;
    const x = Math.round(((e.clientX - r.left) / r.width) * w);
    const y = Math.round(((e.clientY - r.top) / r.height) * h);
    setDot({ x: ((e.clientX - r.left) / r.width) * 100, y: ((e.clientY - r.top) / r.height) * 100 });
    window.setTimeout(() => setDot(null), 450);
    void send({ type: "click", x, y });
  };

  const onWheel = (e: React.WheelEvent) => {
    const now = Date.now();
    if (now - wheel.current < 250) return;
    wheel.current = now;
    void send({ type: "scroll", dy: Math.sign(e.deltaY) * 400 });
  };

  return (
    <div className="frame">
      <form
        className="urlbar"
        onSubmit={(e) => {
          e.preventDefault();
          editing.current = false;
          const u = url.trim();
          if (u) void send({ type: "navigate", url: /^https?:\/\//.test(u) ? u : "https://" + u });
        }}
      >
        <Globe size={13} className="muted" />
        <input
          value={url}
          aria-label={t("br.url")}
          placeholder="https://"
          onFocus={() => (editing.current = true)}
          onBlur={() => (editing.current = false)}
          onChange={(e) => setUrl(e.target.value)}
        />
        <button className="btn ghost" type="submit">
          {t("br.go")}
        </button>
      </form>
      <div className="screen">
        {shot ? (
          <img
            ref={img}
            src={`data:${shot.mime};base64,${shot.data}`}
            alt={shot.title || t("br.frameAlt")}
            draggable={false}
            onClick={click}
            onWheel={onWheel}
          />
        ) : (
          <div className="screenph">{err ?? t("common.loading")}</div>
        )}
        {dot && <span className="clickdot" style={{ left: dot.x + "%", top: dot.y + "%" }} />}
        <span className="livetag">
          <MousePointerClick size={12} />
          {t("br.youControl")}
        </span>
      </div>
      <div className="kbdrow">
        <form
          className="urlbar grow"
          onSubmit={(e) => {
            e.preventDefault();
            if (!text) return;
            void send({ type: "key", text });
            setText("");
          }}
        >
          <input value={text} aria-label={t("br.type")} placeholder={t("br.typePh")} onChange={(e) => setText(e.target.value)} autoComplete="off" />
          <button className="btn ghost" type="submit" disabled={!text} title={t("br.typeSend")}>
            <CornerDownLeft size={13} />
          </button>
        </form>
        {KEYS.map((k) => (
          <button key={k} className="btn ghost keyb" type="button" onClick={() => send({ type: "key", text: k })}>
            {k === "Escape" ? "Esc" : k === "Backspace" ? "⌫" : k}
          </button>
        ))}
        <button className="ix" type="button" aria-label={t("br.scrollUp")} title={t("br.scrollUp")} onClick={() => send({ type: "scroll", dy: -400 })}>
          <ArrowUp size={13} />
        </button>
        <button className="ix" type="button" aria-label={t("br.scrollDown")} title={t("br.scrollDown")} onClick={() => send({ type: "scroll", dy: 400 })}>
          <ArrowDown size={13} />
        </button>
      </div>
      {err && shot && <div className="small errt">{err}</div>}
    </div>
  );
}
