/* Motion: one scale of durations and curves for the whole app (the CSS
 * tokens --dur-* / --ease-* in styles.css are the same values), and two
 * pieces that make it move as one (a change of place — another
 * conversation, settings — is instant, as in native apps):
 *
 *  - exit ghosts: a menu, dialog, scrim or toast that leaves the page is
 *    replaced for a moment by an inert copy that plays its exit, so every
 *    overlay closes as smoothly as it opens without each component
 *    keeping itself mounted;
 *  - entering: what is added to a conversation after it has loaded
 *    (messages, steps, new paragraphs of a streamed answer) is marked
 *    data-enter and animates in; what was there when it opened does not.
 *
 * All of it is off with prefers-reduced-motion. */

export const DUR = { fast: 120, hover: 150, normal: 200, overlay: 220, dialog: 260, slow: 360 } as const;

const reduce = typeof window !== "undefined" ? window.matchMedia("(prefers-reduced-motion: reduce)") : null;
export const reducedMotion = () => !!reduce?.matches;

// ---------------------------------------------------------------- exit ghosts

const GHOST = ".menu, .modal, .scrim, .toast, .ui-pop, .palette, .waitfor, .replychip";
const EXIT_MS: Record<string, number> = { menu: DUR.fast + 20, modal: DUR.normal, scrim: DUR.normal, toast: DUR.normal, "ui-pop": DUR.fast + 20, palette: DUR.normal, waitfor: DUR.normal, replychip: DUR.fast };

function exitMs(el: Element): number {
  for (const k of Object.keys(EXIT_MS)) if (el.classList.contains(k)) return EXIT_MS[k];
  return DUR.normal;
}

function ghost(el: HTMLElement, parent: Node, next: Node | null) {
  // never shown (a menu placed off-screen while it measures itself) or
  // heavy content that would load again (frames, media)
  if (el.style.visibility === "hidden" || el.style.left === "-9999px" || el.querySelector("iframe, video, audio")) return;
  const c = el.cloneNode(true) as HTMLElement;
  c.setAttribute("aria-hidden", "true");
  c.inert = true;
  c.removeAttribute("id");
  c.querySelectorAll("[id]").forEach((x) => x.removeAttribute("id"));
  c.dataset.exit = "";
  c.style.pointerEvents = "none";
  try {
    parent.insertBefore(c, next && next.parentNode === parent ? next : null);
  } catch {
    return;
  }
  const done = () => c.remove();
  c.addEventListener("animationend", done, { once: true });
  setTimeout(done, exitMs(el) + 80);
}

let observing = false;

/** Starts the exit ghosts (once, from App). */
export function installExitGhosts() {
  if (observing || typeof MutationObserver === "undefined") return;
  observing = true;
  new MutationObserver((records) => {
    if (reducedMotion()) return;
    let n = 0;
    for (const r of records) {
      if (!(r.target as Node).isConnected) continue;
      r.removedNodes.forEach((node) => {
        if (!(node instanceof HTMLElement) || node.dataset.exit !== undefined || n > 6) return;
        if (node.matches(GHOST)) {
          n++;
          ghost(node, r.target, r.nextSibling);
        }
      });
    }
  }).observe(document.body, { childList: true, subtree: true });
}

// ---------------------------------------------------------------- entering

/** Marks what is added under root from now on as data-enter: messages
 *  and rows at the end of the conversation, new parts of a turn (steps,
 *  cards, the typing line), new blocks of a streamed answer. The answer
 *  replacing its stream (.tans, .narr) is not marked (it is the same text),
 *  nor is anything added above the end (older messages shown).
 *  Only while root.dataset.sess is still session. Returns a stop function. */
export function markEntering(root: HTMLElement, session: string): () => void {
  if (typeof MutationObserver === "undefined") return () => {};
  const nearEnd = (n: HTMLElement) => {
    let after = 0;
    for (let x = n.nextElementSibling; x; x = x.nextElementSibling) if (++after > 2) return false;
    return true;
  };
  const mo = new MutationObserver((records) => {
    if (reducedMotion() || root.dataset.sess !== session) return;
    for (const r of records) {
      const p = r.target as HTMLElement;
      const inFeed = p === root;
      const inTurn = !inFeed && p.parentElement?.classList.contains("turn") === true;
      const inStream = !inFeed && !inTurn && p.classList?.contains("mt") === true && p.parentElement?.classList.contains("tans") === true && p.parentElement.parentElement?.parentElement?.classList.contains("turn") === true;
      if (!inFeed && !inTurn && !inStream) continue;
      r.addedNodes.forEach((node) => {
        if (!(node instanceof HTMLElement) || node.dataset.exit !== undefined) return;
        if (inTurn && (node.classList.contains("tans") || node.classList.contains("narr"))) return;
        if (!inStream && !nearEnd(node)) return;
        node.dataset.enter = inStream ? "text" : node.classList.contains("m-user") ? "sent" : "";
      });
    }
  });
  mo.observe(root, { childList: true, subtree: true });
  return () => mo.disconnect();
}
