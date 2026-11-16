// Copy text to the clipboard: the async API when the page may use it
// (secure context), else a hidden textarea and execCommand (plain http on
// a LAN address, older webviews).

import { useEffect, useRef, useState } from "react";

export async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    // fall through to the fallback
  }
  try {
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.setAttribute("readonly", "");
    ta.style.position = "fixed";
    ta.style.top = "-1000px";
    ta.style.opacity = "0";
    const active = document.activeElement as HTMLElement | null;
    document.body.appendChild(ta);
    ta.select();
    const ok = document.execCommand("copy");
    ta.remove();
    active?.focus?.();
    return ok;
  } catch {
    return false;
  }
}

/** copied is true for a moment after copy() succeeds ("已复制"). */
export function useCopy(ms = 1600): { copied: boolean; copy: (text: string) => Promise<boolean> } {
  const [copied, setCopied] = useState(false);
  const timer = useRef<number | undefined>(undefined);
  useEffect(() => () => window.clearTimeout(timer.current), []);
  return {
    copied,
    copy: async (text: string) => {
      const ok = await copyText(text);
      if (ok) {
        setCopied(true);
        window.clearTimeout(timer.current);
        timer.current = window.setTimeout(() => setCopied(false), ms);
      }
      return ok;
    },
  };
}
