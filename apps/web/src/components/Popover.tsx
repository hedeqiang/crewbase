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

// what was last pressed: a thing opened from code (a confirmation, a
// viewer) goes in place below it
let pressed: { el: HTMLElement; at: number } | null = null;
if (typeof document !== "undefined")
  document.addEventListener(
    "pointerdown",
    (e) => {
      const el = (e.target as HTMLElement | null)?.closest?.<HTMLElement>("button, [role=button], a, .mi");
      if (el) pressed = { el, at: Date.now() };
    },
    true,
  );
/** The button just pressed (or focused), to open something below it; for
 *  one in a menu that closes as it acts, the row the menu was for. */
export function lastPressed(): HTMLElement | null {
  const a = document.activeElement as HTMLElement | null;
  let el = a && a !== document.body && a.closest("button, [role=button], a, .mi") ? a : pressed && Date.now() - pressed.at < 3000 && pressed.el.isConnected ? pressed.el : null;
  const host = el?.closest<HTMLElement>(".popin-host");
  if (host && !host.querySelector(".menu.inline.keep")) el = (host.previousElementSibling ?? host.nextElementSibling) as HTMLElement | null;
  return el;
}

// open ones, innermost last: Esc closes only that one
const openStack: symbol[] = [];

/** A menu opened by a button, shown in place: inserted into the page
 *  right below the row the button is in (above it for placement "top"),
 *  pushing what follows down, rather than floating over the page. It
 *  closes on a click outside it and on Esc (the keyboard goes back to the
 *  button). */
export function Popover({ anchor, open, onClose, children, placement = "bottom", align = "start", className = "", width }: Props) {
  const ref = useRef<HTMLDivElement>(null);
  const [host, setHost] = useState<HTMLElement | null>(null);

  // where it goes: after (before) the nearest box around the button that
  // sits in a vertical stack (a list's row, a card's line, a header)
  useLayoutEffect(() => {
    if (!open || !anchor) return;
    const row = rowOf(anchor);
    if (!row?.parentElement) return;
    const h = document.createElement("div");
    h.className = "popin-host";
    row.parentElement.insertBefore(h, placement === "top" ? row : row.nextSibling);
    setHost(h);
    return () => {
      h.remove();
      setHost(null);
    };
  }, [open, anchor, placement]);

  useEffect(() => {
    if (!open || !host) return;
    const down = (e: PointerEvent) => {
      const t = e.target as Node;
      if (ref.current?.contains(t) || anchor?.contains(t)) return;
      onClose();
    };
    const id = Symbol();
    openStack.push(id);
    const key = (e: KeyboardEvent) => {
      if (e.key === "Escape" && openStack[openStack.length - 1] === id) {
        e.stopPropagation();
        // Esc gives the keyboard back where it was (the button that
        // opened it; else the composer), not to the page
        const back = anchor?.isConnected ? anchor : (document.querySelector<HTMLElement>(".composer textarea") ?? null);
        onClose();
        requestAnimationFrame(() => back?.focus({ preventScroll: true }));
      }
    };
    document.addEventListener("pointerdown", down, true);
    document.addEventListener("keydown", key, true);
    // in view, and the keyboard in it when it was opened from the keyboard
    ref.current?.scrollIntoView({ block: "nearest" });
    // a field in it takes the keyboard (its text selected, to retype);
    // a plain menu does when it was opened from the keyboard
    const field = ref.current?.querySelector<HTMLInputElement>("input:not([type=hidden]):not([type=checkbox]), textarea");
    if (field && !ref.current?.contains(document.activeElement)) {
      field.focus({ preventScroll: true });
      field.select?.();
    } else if (document.activeElement === anchor) ref.current?.querySelector<HTMLElement>("button:not(:disabled)")?.focus({ preventScroll: true });
    return () => {
      openStack.splice(openStack.indexOf(id), 1);
      document.removeEventListener("pointerdown", down, true);
      document.removeEventListener("keydown", key, true);
    };
  }, [open, anchor, onClose, host]);

  if (!open || !host) return null;
  return createPortal(
    <div
      ref={ref}
      className={"menu inline " + className}
      role="menu"
      data-side={placement === "top" ? "top" : "bottom"}
      data-align={align}
      style={{ width: width ? `min(${width}px, 100%)` : undefined, marginLeft: align === "end" ? "auto" : undefined }}
    >
      {children}
    </div>,
    host,
  );
}

/** The box around el that is a row of a vertical stack. */
function rowOf(el: HTMLElement): HTMLElement | null {
  for (let p: HTMLElement | null = el; p && p !== document.body; p = p.parentElement) {
    const parent = p.parentElement;
    if (!parent) return null;
    if (getComputedStyle(p).display.startsWith("inline") && p !== el) continue;
    const ps = getComputedStyle(parent);
    const vertical =
      ps.display === "block" ||
      ps.display === "flow-root" ||
      (ps.display.endsWith("flex") && ps.flexDirection.startsWith("column")) ||
      (ps.display.endsWith("grid") && ps.gridTemplateColumns.trim().split(/\s+/).length <= 1);
    // (a button or a label holds the button; in a paragraph a block is not allowed)
    if (vertical && !/^(BUTTON|LABEL|P|SPAN|A)$/.test(parent.tagName)) return p;
  }
  return null;
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
