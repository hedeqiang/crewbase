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

export function AvatarRaw({ color, shape, size = 30, title }: { color: string; shape?: string; size?: number; title?: string }) {
  const sh = SHAPES.includes(shape as Shape) && shape !== "sq" ? ` sh-${shape}` : "";
  return <span className={"av" + sh} style={{ "--s": `${size}px`, "--c": color } as CSSProperties} title={title} aria-hidden={title ? undefined : true} />;
}

export function Avatar({ agent, size = 30, status = false }: { agent?: AgentView; size?: number; status?: boolean }) {
  const pending = useStore((s) => !!agent && s.approvals.some((a) => a.agent === agent.id));
  const color = agent?.color || colorFor(agent?.id ?? "?");
  const raw = <AvatarRaw color={color} shape={agent?.shape} size={size} />;
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
