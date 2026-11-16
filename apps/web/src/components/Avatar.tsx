import { MessageSquare } from "lucide-react";
import { useEffect, type CSSProperties, type ReactNode } from "react";
import type { AgentView } from "../api";
import { statusTone } from "../format";
import { useStore } from "../store";
import { t, type Key } from "../i18n";

// 18 identity colours × 7 shapes (docs/03 §4). Identity colours never mix
// with status colours.
export const COLORS = [
  "#3987e5", "#e5484d", "#ef8a45", "#f2b33d", "#8bc34a", "#24b07e",
  "#1fb5a8", "#2db7f5", "#5b8def", "#3a4aa8", "#6c6cf0", "#8b7cf6",
  "#d651c8", "#f07aa8", "#b8314f", "#9a6a3f", "#d8b98a", "#9b9ba3",
];
export const SHAPES = ["sq", "circle", "blob", "pill", "diamond", "hex", "tri", "drop", "shield", "oct", "flower", "spark", "ticket", "cat", "ghost", "cloud", "bean", "star"] as const;
export type Shape = (typeof SHAPES)[number];
/** Faces: how the crew member looks out (independent of shape and colour). */
export const FACES = ["eyes", "dots", "smile", "glasses", "sleepy", "visor", "cyclops"] as const;
export type Face = (typeof FACES)[number];

/** A stable colour for a channel: any identity colour but the grey. */
export function channelColor(id: string): string {
  const cs = COLORS.filter((c) => c !== "#9b9ba3" && c !== "#d8b98a");
  let h = 0;
  for (const c of id) h = (h * 31 + c.charCodeAt(0)) >>> 0;
  return cs[h % cs.length];
}

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
  // a round head with two pointed ears
  cat: "M6.2 13.6L7.6 4.6c.3-1.9 2.2-2.6 3.6-1.4L17 8.6h6l5.8-5.4c1.4-1.2 3.3-.5 3.6 1.4l1.4 9C36.4 16.6 38 20.2 38 24c0 8.6-8 13.5-18 13.5S2 32.6 2 24c0-3.8 1.6-7.4 4.2-10.4z",
  // a little ghost with a wavy hem
  ghost: "M4 18a16 16 0 0 1 32 0v16.5c0 1.6-1.8 2.4-3 1.4l-2.6-2.2a2 2 0 0 0-2.6 0l-2.5 2.2a2 2 0 0 1-2.6 0l-2.4-2.2a2 2 0 0 0-2.6 0l-2.5 2.2a2 2 0 0 1-2.6 0l-2.6-2.2a2 2 0 0 0-2.6 0L7 35.9c-1.2 1-3 .2-3-1.4z",
  cloud: "M11 35a9 9 0 0 1-1.6-17.9A11 11 0 0 1 30.4 14A8.6 8.6 0 0 1 29.6 35z",
  bean: "M14 3c7 0 10 4.4 12.4 7.6c2.2 2.9 6.2 4.3 8.8 8C38.8 24 37 37 20 37S1.6 29.6 3 19.6C4.2 10.4 7.6 3 14 3z",
  // five points, rounded by a stroke of the body colour
  star: "M20 6.5L24 16L34.3 16.9L26.5 23.6L28.8 33.6L20 28.3L11.2 33.6L13.5 23.6L5.7 16.9L16 16z",
};
/** Shapes drawn with a rounding stroke. */
const STROKED: Partial<Record<Shape, number>> = { star: 4.5 };


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
const EYE_Y: Partial<Record<Shape, number>> = { tri: 24, blob: 20, drop: 22, shield: 18.5, pill: 20, cat: 22, ghost: 17.5, cloud: 23, bean: 20, star: 21 };
const EYE_GAP: Partial<Record<Shape, number>> = { tri: 5, pill: 6, spark: 4.6, flower: 5.2, diamond: 5.2, star: 4.4, cloud: 5.4 };

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

export function AvatarRaw({ color, shape, face, size = 30, title, name }: { color: string; shape?: string; face?: string; size?: number; title?: string; name?: string }) {
  const sh: Shape = SHAPES.includes(shape as Shape) ? (shape as Shape) : "sq";
  const fc: Face = FACES.includes(face as Face) ? (face as Face) : "eyes";
  useEyeTracking();
  const ey = EYE_Y[sh] ?? 19.5;
  const gap = EYE_GAP[sh] ?? 5.8;
  const fg = ink(color);
  const sclera = fg === "#fff" ? "#fff" : "#141414";
  const pupil = fg === "#fff" ? "#141414" : "#fff";
  // blink rhythm differs per agent so a list does not blink in unison
  const n = seed(name || color + sh);
  const style = { "--s": `${size}px`, "--blink": `${4 + (n % 5)}s`, "--blinkd": `${(n % 37) / 10}s` } as CSSProperties;
  const happy = <path className="happy" d="M-3.2 1.2 Q0 -3.4 3.2 1.2" fill="none" stroke={sclera} strokeWidth="2" strokeLinecap="round" />;
  const roundEye = (
    <>
      <ellipse rx="3.6" ry="4.4" fill={sclera} />
      <circle className="pupil" r="1.9" fill={pupil} />
      <circle className="glint" r=".65" cx=".9" cy="-1.4" fill={sclera} />
    </>
  );
  const pair = (open: ReactNode, g = gap) =>
    [-g, g].map((dx) => (
      <g key={dx} className="eye" transform={`translate(${20 + dx} ${ey})`}>
        <g className="open">{open}</g>
        {happy}
      </g>
    ));
  let eyes: ReactNode;
  let extra: ReactNode = null;
  switch (fc) {
    case "dots":
      eyes = pair(<circle className="pupil" r="2.3" fill={sclera} />, gap - 0.4);
      extra = [-1, 1].map((k) => <ellipse key={k} cx={20 + k * (gap + 3.6)} cy={ey + 4.2} rx="2.4" ry="1.5" fill="#ff6f91" opacity=".55" />);
      break;
    case "smile":
      eyes = pair(roundEye);
      extra = <path d={`M16.6 ${ey + 6.2} Q20 ${ey + 9.4} 23.4 ${ey + 6.2}`} fill="none" stroke={sclera} strokeWidth="1.9" strokeLinecap="round" />;
      break;
    case "glasses":
      eyes = pair(roundEye);
      extra = (
        <g fill="none" stroke={sclera} strokeWidth="1.5" opacity=".9">
          <circle cx={20 - gap} cy={ey} r="5.4" />
          <circle cx={20 + gap} cy={ey} r="5.4" />
          <path d={`M${20 - gap + 5.4} ${ey - 0.6} Q20 ${ey - 2.2} ${20 + gap - 5.4} ${ey - 0.6}`} />
        </g>
      );
      break;
    case "sleepy":
      eyes = pair(
        <>
          {roundEye}
          <path d="M-4.1 .2A4 4.8 0 0 1 4.1 .2z" fill={color} />
          <path d="M-3.9 .2H3.9" stroke={sclera} strokeWidth="1.3" strokeLinecap="round" />
        </>,
      );
      break;
    case "visor":
      extra = <rect x={20 - gap - 5.2} y={ey - 4} width={2 * gap + 10.4} height="8" rx="4" fill="#141414" opacity=".88" />;
      eyes = pair(<circle className="pupil" r="1.9" fill="#8ff3ff" />);
      break;
    case "cyclops":
      eyes = (
        <g className="eye" transform={`translate(20 ${ey})`}>
          <g className="open">
            <ellipse rx="5.6" ry="6.2" fill={sclera} />
            <circle className="pupil" r="2.8" fill={pupil} />
            <circle className="glint" r=".9" cx="1.3" cy="-2" fill={sclera} />
          </g>
          <path className="happy" d="M-4.6 1.6 Q0 -4.6 4.6 1.6" fill="none" stroke={sclera} strokeWidth="2.2" strokeLinecap="round" />
        </g>
      );
      break;
    default:
      eyes = pair(roundEye);
  }
  const sw = STROKED[sh];
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
        <path d={PATHS[sh]} fill={color} stroke={sw ? color : undefined} strokeWidth={sw} strokeLinejoin="round" />
        {fc === "visor" && extra}
        <g className="eyes">{eyes}</g>
        {fc !== "visor" && extra}
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
      <AvatarRaw color={color} shape={agent?.shape} face={agent?.face} size={size} name={agent?.name} />
    );
  if (!status || !agent) return raw;
  // Waiting on an approval is shown as amber even while the turn is "running".
  const tone = pending && agent.status !== "failed" ? "wait" : statusTone(agent.status);
  // idle is the normal state: no dot, so a list of resting agents stays calm
  if (tone === "idle") return raw;
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

/** Shape, face and colour choices for an existing agent; each click saves. */
export function LookPicker({ agent, onChange }: { agent: AgentView; onChange: (p: { color?: string; shape?: string; face?: string }) => void }) {
  const color = agent.color || colorFor(agent.id);
  const shape = agent.shape || "sq";
  const face = agent.face || "eyes";
  return (
    <div className="lookpick">
      <div className="lookhero">
        <AvatarRaw color={color} shape={shape} face={face} size={64} name={agent.name} />
      </div>
      <div className="na-shapes" role="radiogroup" aria-label={t("na.shape")}>
        {SHAPES.map((sh) => (
          <button key={sh} type="button" className="shp" aria-pressed={shape === sh} aria-label={sh} onClick={() => onChange({ shape: sh })}>
            <AvatarRaw color={color} shape={sh} face={face} size={24} name={agent.name} />
          </button>
        ))}
      </div>
      <div className="na-shapes" role="radiogroup" aria-label={t("na.face")}>
        {FACES.map((fc) => (
          <button key={fc} type="button" className="shp" aria-pressed={face === fc} aria-label={t(`face.${fc}` as Key)} title={t(`face.${fc}` as Key)} onClick={() => onChange({ face: fc })}>
            <AvatarRaw color={color} shape={shape} face={fc} size={24} name={agent.name} />
          </button>
        ))}
      </div>
      <div className="na-colors" role="radiogroup" aria-label={t("na.color")}>
        {COLORS.map((c) => (
          <button key={c} type="button" className="dotc" style={{ ["--c" as string]: c } as CSSProperties} aria-pressed={color === c} aria-label={c} onClick={() => onChange({ color: c })} />
        ))}
      </div>
    </div>
  );
}
