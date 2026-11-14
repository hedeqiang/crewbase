import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";

interface Props {
  anchor: HTMLElement | null;
  open: boolean;
  onClose: () => void;
  children: ReactNode;
  placement?: "top" | "bottom";
  align?: "start" | "end";
  className?: string;
  width?: number;
}

/** Anchored menu rendered in a portal; closes on outside click and Esc. */
export function Popover({ anchor, open, onClose, children, placement = "bottom", align = "start", className = "", width }: Props) {
  const ref = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState<{ left: number; top: number; maxH: number } | null>(null);

  useLayoutEffect(() => {
    if (!open || !anchor) return;
    const place = () => {
      const r = anchor.getBoundingClientRect();
      const el = ref.current;
      const w = el?.offsetWidth ?? width ?? 260;
      const h = el?.offsetHeight ?? 200;
      const vw = window.innerWidth;
      const vh = window.innerHeight;
      let left = align === "end" ? r.right - w : r.left;
      left = Math.max(8, Math.min(left, vw - w - 8));
      let top: number;
      let maxH: number;
      const below = vh - r.bottom - 12;
      const above = r.top - 12;
      const up = placement === "top" ? above > 160 || above > below : below < Math.min(h, 240) && above > below;
      if (up) {
        maxH = above;
        top = r.top - 6 - Math.min(h, maxH);
      } else {
        maxH = below;
        top = r.bottom + 6;
      }
      setPos({ left, top: Math.max(8, top), maxH: Math.max(120, maxH) });
    };
    place();
    const ro = new ResizeObserver(place);
    if (ref.current) ro.observe(ref.current);
    window.addEventListener("resize", place);
    return () => {
      ro.disconnect();
      window.removeEventListener("resize", place);
    };
  }, [open, anchor, placement, align, width]);

  useEffect(() => {
    if (!open) return;
    const down = (e: PointerEvent) => {
      const t = e.target as Node;
      if (ref.current?.contains(t) || anchor?.contains(t)) return;
      onClose();
    };
    const key = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        onClose();
      }
    };
    document.addEventListener("pointerdown", down, true);
    document.addEventListener("keydown", key, true);
    return () => {
      document.removeEventListener("pointerdown", down, true);
      document.removeEventListener("keydown", key, true);
    };
  }, [open, anchor, onClose]);

  if (!open) return null;
  return createPortal(
    <div
      ref={ref}
      className={"menu " + className}
      role="menu"
      style={{
        left: pos?.left ?? -9999,
        top: pos?.top ?? -9999,
        maxHeight: pos?.maxH,
        width,
        visibility: pos ? "visible" : "hidden",
      }}
    >
      {children}
    </div>,
    document.body,
  );
}

/** useState + ref helper for anchored menus. */
export function useMenu() {
  const [el, setEl] = useState<HTMLElement | null>(null);
  const [open, setOpen] = useState(false);
  return {
    ref: setEl,
    anchor: el,
    open,
    toggle: () => setOpen((o) => !o),
    close: () => setOpen(false),
    setOpen,
  };
}
