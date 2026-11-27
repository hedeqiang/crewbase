// In place of a dialog: what a dialog held (connect a server, pair a
// phone, add a member, …) opens as a card at the top of the page it was
// asked from, pushing the page down, instead of floating over it.
//
//   - it goes in the page's sheet slot (the settings heading has one), and
//     the page scrolls to it; closing it scrolls back to where you were
//   - Esc closes the topmost one (after the content's own Esc handlers:
//     one that handled Esc marks it); the keyboard goes into it, and back
//     to what opened it when it closes
//   - without a slot (a page that has none) it stays where it is rendered
//
// The content keeps its own header (title and ✕), body and footer.

import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";

const stack: symbol[] = [];

/** The slot sheets go into: put one under a page's heading. */
export function SheetSlot() {
  return <div className="sheet-slot" />;
}

export function Sheet({ onClose, className = "", label, children }: { onClose: () => void; className?: string; label?: string; children: ReactNode }) {
  const ref = useRef<HTMLElement>(null);
  const [slot, setSlot] = useState<HTMLElement | null | undefined>(undefined);
  const close = useRef(onClose);
  close.current = onClose;
  useLayoutEffect(() => setSlot(document.querySelector<HTMLElement>(".sheet-slot")), []);
  useEffect(() => {
    if (slot === undefined) return;
    const id = Symbol();
    stack.push(id);
    const back = document.activeElement as HTMLElement | null;
    const el = ref.current;
    const scroller = scrollParent(el);
    const top = scroller?.scrollTop ?? 0;
    el?.scrollIntoView({ block: "start", behavior: matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth" });
    // the content may focus a field itself (autoFocus); else its first one
    const f = window.setTimeout(() => {
      if (el && !el.contains(document.activeElement)) el.querySelector<HTMLElement>("input:not([type=hidden]), textarea, select, .mbd button, button")?.focus({ preventScroll: true });
    }, 0);
    const key = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || e.defaultPrevented || e.isComposing || stack[stack.length - 1] !== id) return;
      e.preventDefault();
      close.current();
    };
    window.addEventListener("keydown", key);
    return () => {
      window.clearTimeout(f);
      window.removeEventListener("keydown", key);
      stack.splice(stack.indexOf(id), 1);
      if (scroller) scroller.scrollTop = top;
      if (back?.isConnected) back.focus({ preventScroll: true });
    };
  }, [slot]);
  if (slot === undefined) return null;
  const body = (
    <section ref={ref} className={"sheet " + className} role="region" aria-label={label}>
      {children}
    </section>
  );
  return slot ? createPortal(body, slot) : body;
}

function scrollParent(el: HTMLElement | null): HTMLElement | null {
  for (let p = el?.parentElement; p; p = p.parentElement) {
    const o = getComputedStyle(p).overflowY;
    if ((o === "auto" || o === "scroll") && p.scrollHeight > p.clientHeight) return p;
  }
  return null;
}
