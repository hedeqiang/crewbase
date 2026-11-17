// The desktop app's in-place update (apps/desktop/updates.go): the shell
// pushes its state; the sidebar pill and Settings › 关于 read it here.
import { useSyncExternalStore } from "react";
import { desktop, type DesktopUpdateState } from "./api";
import { confirmDialog } from "./components/Confirm";
import { t } from "./i18n";
import { getState } from "./store";

let current: DesktopUpdateState | null = null;
let notesOpen = false;
const subs = new Set<() => void>();
let started = false;

function notify() {
  for (const fn of subs) fn();
}

function start() {
  if (started) return;
  const d = desktop();
  if (!d?.updateState || !d.onUpdate) return;
  started = true;
  d.onUpdate((st) => {
    current = st;
    notify();
  });
  d.updateState().then(
    (st) => {
      // an event may have arrived first: it is newer
      if (!current && st) {
        current = st;
        notify();
      }
    },
    () => {},
  );
}

function subscribe(fn: () => void) {
  start();
  subs.add(fn);
  return () => {
    subs.delete(fn);
  };
}

/** The update state, null outside the desktop app (or before it answers). */
export function useDesktopUpdate(): DesktopUpdateState | null {
  return useSyncExternalStore(subscribe, () => current);
}

/** Whether this shell updates in place (else Settings shows the daemon's check). */
export function inPlaceUpdates(): boolean {
  const d = desktop();
  return !!(d?.updateState && d.onUpdate);
}

export function useUpdateNotesOpen(): boolean {
  return useSyncExternalStore(subscribe, () => notesOpen);
}

export function openUpdateNotes(open = true) {
  notesOpen = open;
  notify();
}

export function checkForUpdates() {
  desktop()?.checkForUpdates?.();
}

export function downloadUpdate() {
  desktop()?.downloadUpdate?.();
}

/** Relaunches into the update; asks first while agents are at work, which
 *  the restart would interrupt (the update also installs at the next quit). */
export async function restartToUpdate() {
  const busy = Object.values(getState().agents).filter((a) => a.status === "running" || a.status === "waiting").length;
  if (busy > 0 && !(await confirmDialog({ text: t("upd.restartBusy", { n: busy }), ok: t("upd.restartOk") }))) return;
  try {
    await desktop()?.restartToUpdate?.();
  } catch (e) {
    console.warn("restartToUpdate", e);
  }
}

/** The download's progress as a whole percent. */
export function percent(st: DesktopUpdateState): number {
  return Math.max(0, Math.min(100, Math.floor((st.progress || 0) * 100)));
}
