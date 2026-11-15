import { MessageSquare } from "lucide-react";
import { useEffect, type CSSProperties } from "react";
import type { AgentView } from "../api";
import { statusTone } from "../format";
import { useStore } from "../store";

// 18 identity colours × 7 shapes (docs/03 §4). Identity colours never mix
// with status colours.
export const COLORS = [
  "#3987e5", "#e5484d", "#ef8a45", "#f2b33d", "#8bc34a", "#24b07e",
  "#1fb5a8", "#2db7f5", "#5b8def", "#3a4aa8", "#6c6cf0", "#8b7cf6",
  "#d651c8", "#f07aa8", "#b8314f", "#9a6a3f", "#d8b98a", "#9b9ba3",
];
export const SHAPES = ["sq", "circle", "blob", "pill", "diamond", "hex", "tri", "drop", "shield", "oct", "flower", "spark", "ticket"] as const;
export type Shape = (typeof SHAPES)[number];

/** Stable fallback colour for agents created without one. */
export function colorFor(id: string): string {
  let h = 0;
  for (const c of id) h = (h * 31 + c.charCodeAt(0)) >>> 0;
  return COLORS[h % COLORS.length];
}

// Shape outlines on a 40-unit grid: Crewbase's own "crew" set of coloured
// creatures with round eyes (no borrowed mascots).
const PATHS: Record<Shape, string> = {
  sq: "M12 2h16a10 10 0 0 1 10 10v16a10 10 0 0 1-10 10H12A10 10 0 0 1 2 28V12A10 10 0 0 1 12 2z",
  circle: "M20 2a18 18 0 1 1 0 36a18 18 0 1 1 0-36z",
  blob: "M6 36C3 36 2 34 2 31C2 14 14 2 31 2C34 2 36 3 36 6C36 25 25 36 6 36z",
  pill: "M14 7h12a13 13 0 0 1 0 26H14a13 13 0 0 1 0-26z",
  diamond: "M17.2 3.6a4 4 0 0 1 5.6 0l13.6 13.6a4 4 0 0 1 0 5.6L22.8 36.4a4 4 0 0 1-5.6 0L3.6 22.8a4 4 0 0 1 0-5.6z",
  hex: "M13 3.5h14a4 4 0 0 1 3.5 2l7 12.5a4 4 0 0 1 0 4l-7 12.5a4 4 0 0 1-3.5 2H13a4 4 0 0 1-3.5-2l-7-12.5a4 4 0 0 1 0-4l7-12.5a4 4 0 0 1 3.5-2z",
  tri: "M16.6 5a4 4 0 0 1 6.8 0l14 24a4 4 0 0 1-3.4 6H6a4 4 0 0 1-3.4-6z",
  // a drop leaning on its point (top right)
  drop: "M20 38C10 38 3 31 3 22C3 12 11 3 33 3C36 3 37 4 37 7C37 29 28 38 20 38z",
  shield: "M20 2.5l14 5a3 3 0 0 1 2 2.8V19c0 9.5-6.6 16.2-15.1 18.8a3 3 0 0 1-1.8 0C10.6 35.2 4 28.5 4 19V10.3a3 3 0 0 1 2-2.8z",
  oct: "M14 2h12a4 4 0 0 1 2.8 1.2l8 8A4 4 0 0 1 38 14v12a4 4 0 0 1-1.2 2.8l-8 8A4 4 0 0 1 26 38H14a4 4 0 0 1-2.8-1.2l-8-8A4 4 0 0 1 2 26V14a4 4 0 0 1 1.2-2.8l8-8A4 4 0 0 1 14 2z",
  // four round petals around a centre
  flower: "M20 2a8 8 0 0 1 8 8a8 8 0 0 1 0 20a8 8 0 0 1-16 0a8 8 0 0 1 0-20a8 8 0 0 1 8-8z",
  // a soft four-pointed spark
  spark: "M20 1.5c1.6 0 2.4 1.2 3 3.2l2.4 7.9l7.9 2.4c2 .6 3.2 1.4 3.2 3s-1.2 2.4-3.2 3l-7.9 2.4l-2.4 7.9c-.6 2-1.4 3.2-3 3.2s-2.4-1.2-3-3.2l-2.4-7.9l-7.9-2.4c-2-.6-3.2-1.4-3.2-3s1.2-2.4 3.2-3l7.9-2.4l2.4-7.9c.6-2 1.4-3.2 3-3.2z",
  // a ticket: a rounded rectangle notched on both sides
  ticket: "M8 6h24a4 4 0 0 1 4 4v6a4 4 0 0 0 0 8v6a4 4 0 0 1-4 4H8a4 4 0 0 1-4-4v-6a4 4 0 0 0 0-8v-6a4 4 0 0 1 4-4z",
};


/** Readable ink on an identity colour. */
function ink(hex: string): string {
  const m = /^#?([0-9a-f]{6})$/i.exec(hex);
  if (!m) return "#fff";
  const n = parseInt(m[1], 16);
  const [r, g, b] = [(n >> 16) & 255, (n >> 8) & 255, n & 255].map((v) => {
    const c = v / 255;
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * r + 0.7152 * g + 0.0722 * b > 0.45 ? "#141414" : "#fff";
}

/** First visible character of a name (CJK-safe), upper-cased. */
export function monogram(name?: string): string {
  const ch = Array.from((name ?? "").trim())[0] ?? "";
  return ch.toUpperCase();
}

// Eyes: every badge is a little crew member. Pupils follow the pointer
// (one shared listener), eyes blink now and then (staggered per name) and
// squint happily while pressed. Small badges keep still eyes.
const EYE_Y: Partial<Record<Shape, number>> = { tri: 24, blob: 20, drop: 22, shield: 18.5, pill: 20 };
const EYE_GAP: Partial<Record<Shape, number>> = { tri: 5, pill: 6, spark: 4.6, flower: 5.2, diamond: 5.2 };

function seed(s: string): number {
  let h = 0;
  for (const c of s) h = (h * 33 + c.charCodeAt(0)) >>> 0;
  return h;
}

let watchers = 0;
let raf = 0;
let lastX = 0;
let lastY = 0;
function track() {
  raf = 0;
  document.querySelectorAll<SVGGElement>("svg.av.eyed").forEach((svg) => {
    const r = svg.getBoundingClientRect();
    if (r.width < 24 || r.bottom < 0 || r.top > innerHeight) return;
    const dx = lastX - (r.left + r.width / 2);
    const dy = lastY - (r.top + r.height / 2);
    const d = Math.hypot(dx, dy) || 1;
    const k = Math.min(1, d / 260);
    svg.style.setProperty("--px", ((dx / d) * 1.5 * k).toFixed(2) + "px");
    svg.style.setProperty("--py", ((dy / d) * 1.5 * k).toFixed(2) + "px");
  });
}
function onMove(e: PointerEvent) {
  lastX = e.clientX;
  lastY = e.clientY;
  if (!raf) raf = requestAnimationFrame(track);
}
function useEyeTracking() {
  useEffect(() => {
    if (matchMedia("(prefers-reduced-motion: reduce)").matches) return;
    if (watchers++ === 0) window.addEventListener("pointermove", onMove, { passive: true });
    return () => {
      if (--watchers === 0) window.removeEventListener("pointermove", onMove);
    };
  }, []);
}

export function AvatarRaw({ color, shape, size = 30, title, name }: { color: string; shape?: string; size?: number; title?: string; name?: string }) {
  const sh: Shape = SHAPES.includes(shape as Shape) ? (shape as Shape) : "sq";
  useEyeTracking();
  const ey = EYE_Y[sh] ?? 19.5;
  const gap = EYE_GAP[sh] ?? 5.8;
  const fg = ink(color);
  const sclera = fg === "#fff" ? "#fff" : "#141414";
  const pupil = fg === "#fff" ? "#141414" : "#fff";
  // blink rhythm differs per agent so a list does not blink in unison
  const n = seed(name || color + sh);
  const style = { "--s": `${size}px`, "--blink": `${4 + (n % 5)}s`, "--blinkd": `${(n % 37) / 10}s` } as CSSProperties;
  return (
    <svg
      className={"av" + (size >= 24 ? " eyed" : "")}
      width={size}
      height={size}
      viewBox="0 0 40 40"
      role={title ? "img" : undefined}
      aria-label={title}
      aria-hidden={title ? undefined : true}
      style={style}
    >
      <g className="avb">
        <path d={PATHS[sh]} fill={color} />
        <g className="eyes">
          {[-gap, gap].map((dx) => (
            <g key={dx} className="eye" transform={`translate(${20 + dx} ${ey})`}>
              <g className="open">
                <ellipse rx="3.6" ry="4.4" fill={sclera} />
                <circle className="pupil" r="1.9" fill={pupil} />
                <circle className="glint" r=".65" cx=".9" cy="-1.4" fill={sclera} />
              </g>
              <path className="happy" d="M-3.2 1.2 Q0 -3.4 3.2 1.2" fill="none" stroke={sclera} strokeWidth="2" strokeLinecap="round" />
            </g>
          ))}
        </g>
      </g>
    </svg>
  );
}

export function Avatar({ agent, size = 30, status = false }: { agent?: AgentView; size?: number; status?: boolean }) {
  const pending = useStore((s) => !!agent && s.approvals.some((a) => a.agent === agent.id));
  const color = agent?.color || colorFor(agent?.id ?? "?");
  // blank chats have no persona: a neutral speech-bubble tile
  const raw =
    agent?.kind === "chat" ? (
      <span className="chatav" style={{ width: size, height: size, borderRadius: Math.round(size * 0.3) }} aria-hidden>
        <MessageSquare size={Math.round(size * 0.5)} />
      </span>
    ) : (
      <AvatarRaw color={color} shape={agent?.shape} size={size} name={agent?.name} />
    );
  if (!status || !agent) return raw;
  // Waiting on an approval is shown as amber even while the turn is "running".
  const tone = pending && agent.status !== "failed" ? "wait" : statusTone(agent.status);
  const dot = Math.max(8, Math.round(size * 0.36));
  return (
    <span className={"avw " + tone}>
      {raw}
      <span className={`sd ${tone}`} style={{ width: dot, height: dot }} />
    </span>
  );
}

export function MeAvatar({ size = 26, label = "•" }: { size?: number; label?: string }) {
  return (
    <span className="av me-av" style={{ "--s": `${size}px` } as CSSProperties} aria-hidden>
      {label}
    </span>
  );
}
