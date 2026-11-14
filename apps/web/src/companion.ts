// Mobile companion (D-3): installable web app, system notifications for
// things that need the person while the app is in the background. Inside
// the desktop shell none of this runs (the shell has its own tray and
// notifications).

import type { ApprovalRequest } from "./api";
import { desktop } from "./api";
import { t } from "./i18n";

let reg: ServiceWorkerRegistration | null = null;

export function initCompanion(onOpen: (url: string) => void) {
  if (desktop() || !("serviceWorker" in navigator)) return;
  // service workers need a secure context (https or localhost)
  if (!window.isSecureContext) return;
  navigator.serviceWorker
    .register("/sw.js")
    .then((r) => {
      reg = r;
    })
    .catch(() => {});
  navigator.serviceWorker.addEventListener("message", (e) => {
    if (e.data?.type === "open" && typeof e.data.url === "string") onOpen(e.data.url);
  });
}

export function notifySupported(): boolean {
  return !desktop() && "Notification" in window && window.isSecureContext;
}

export function notifyPermission(): NotificationPermission | "unsupported" {
  return notifySupported() ? Notification.permission : "unsupported";
}

export async function askNotify(): Promise<NotificationPermission | "unsupported"> {
  if (!notifySupported()) return "unsupported";
  return Notification.requestPermission();
}

const seen = new Set<string>();
let primed = false;

/** Shows a notification for approvals that arrived while the page was hidden. */
export function notifyApprovals(list: ApprovalRequest[], agentName: (id: string) => string) {
  const fresh = list.filter((a) => !seen.has(a.callId));
  for (const a of list) seen.add(a.callId);
  // the first load only primes: approvals that were already waiting do not ring
  if (!primed) {
    primed = true;
    return;
  }
  if (!fresh.length || document.visibilityState === "visible" || notifyPermission() !== "granted") return;
  for (const a of fresh.slice(0, 3)) {
    const title = t("comp.notifyApproval", { name: agentName(a.agent) });
    const opts: NotificationOptions = { body: a.summary, tag: "approval:" + a.callId, data: { url: `/#/session/${encodeURIComponent(a.session)}` }, icon: "/icon-192.png", badge: "/icon-192.png" };
    if (reg) void reg.showNotification(title, opts);
    else new Notification(title, opts);
  }
}
