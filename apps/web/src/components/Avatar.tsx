import { MessageSquare } from "lucide-react";
import type { CSSProperties } from "react";
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
export const SHAPES = ["sq", "circle", "blob", "pill", "diamond", "hex", "tri"] as const;
export type Shape = (typeof SHAPES)[number];

/** Stable fallback colour for agents created without one. */
export function colorFor(id: string): string {
  let h = 0;
  for (const c of id) h = (h * 31 + c.charCodeAt(0)) >>> 0;
  return COLORS[h % COLORS.length];
}

// Shape outlines on a 40-unit grid. Original "crew badge" set: the agent's
// monogram sits in a coloured badge; no faces, no borrowed mascots.
const PATHS: Record<Shape, string> = {
  sq: "M12 2h16a10 10 0 0 1 10 10v16a10 10 0 0 1-10 10H12A10 10 0 0 1 2 28V12A10 10 0 0 1 12 2z",
  circle: "M20 2a18 18 0 1 1 0 36a18 18 0 1 1 0-36z",
  blob: "M6 36C3 36 2 34 2 31C2 14 14 2 31 2C34 2 36 3 36 6C36 25 25 36 6 36z",
  pill: "M14 7h12a13 13 0 0 1 0 26H14a13 13 0 0 1 0-26z",
  diamond: "M17.2 3.6a4 4 0 0 1 5.6 0l13.6 13.6a4 4 0 0 1 0 5.6L22.8 36.4a4 4 0 0 1-5.6 0L3.6 22.8a4 4 0 0 1 0-5.6z",
  hex: "M13 3.5h14a4 4 0 0 1 3.5 2l7 12.5a4 4 0 0 1 0 4l-7 12.5a4 4 0 0 1-3.5 2H13a4 4 0 0 1-3.5-2l-7-12.5a4 4 0 0 1 0-4l7-12.5a4 4 0 0 1 3.5-2z",
  tri: "M16.6 5a4 4 0 0 1 6.8 0l14 24a4 4 0 0 1-3.4 6H6a4 4 0 0 1-3.4-6z",
};
const TEXT_Y: Partial<Record<Shape, number>> = { tri: 25.5, blob: 21 };

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

export function AvatarRaw({ color, shape, size = 30, title, name }: { color: string; shape?: string; size?: number; title?: string; name?: string }) {
  const sh: Shape = SHAPES.includes(shape as Shape) ? (shape as Shape) : "sq";
  const letter = monogram(name);
  return (
    <svg
      className="av"
      width={size}
      height={size}
      viewBox="0 0 40 40"
      role={title ? "img" : undefined}
      aria-label={title}
      aria-hidden={title ? undefined : true}
      style={{ "--s": `${size}px` } as CSSProperties}
    >
      <path d={PATHS[sh]} fill={color} />
      {letter && (
        <text x="20" y={TEXT_Y[sh] ?? 20} textAnchor="middle" dominantBaseline="central" fill={ink(color)} fontSize={sh === "tri" ? 13 : 17} fontWeight={650}>
          {letter}
        </text>
      )}
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
    <span className="avw">
      {raw}
      <span className={`sd ${tone}`} style={{ width: dot, height: dot }} />
    </span>
  );
}

export function MeAvatar({ size = 26, label = "你" }: { size?: number; label?: string }) {
  return (
    <span className="av me-av" style={{ "--s": `${size}px` } as CSSProperties} aria-hidden>
      {label}
    </span>
  );
}
