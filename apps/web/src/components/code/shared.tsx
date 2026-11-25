// What the 代码 tab's parts share: who an actor is, running an action,
// a path cut in the middle, and handing the agent a message.

import { useMemo, useState } from "react";
import { User } from "lucide-react";
import { API, type AgentView } from "../../api";
import { t } from "../../i18n";
import { toast, useStore } from "../../store";
import { Avatar } from "../Avatar";

export const errOf = (e: any): string => e?.message ?? String(e);

/** Runs an action, says what went wrong, and refreshes. */
export function useAct(reload: () => void) {
  const [busy, setBusy] = useState(false);
  const run = async (f: () => Promise<unknown>, ok?: string) => {
    setBusy(true);
    try {
      await f();
      if (ok) toast(ok);
    } catch (e: any) {
      toast(errOf(e));
    } finally {
      setBusy(false);
      reload();
    }
  };
  return { busy, run };
}

/** agent:kai → Kai; agent:kai-sub-2 → Kai 的子 Agent; person:owner → 你 */
export function useActor() {
  const agents = useStore((s) => s.agents);
  return useMemo(() => {
    const name = (actor: string): string => {
      if (actor.startsWith("person:")) {
        const id = actor.slice(7);
        return id === "owner" ? t("git.you") : id.replace(/^github:/, "");
      }
      const id = actor.replace(/^agent:/, "");
      if (agents[id]) return agents[id].name;
      const m = id.match(/^(.*)-sub-\d+$/);
      if (m) return t("git.subOf", { name: agents[m[1]]?.name ?? m[1] });
      return id;
    };
    const agentOf = (actor: string): AgentView | undefined => {
      const id = actor.replace(/^agent:/, "");
      return agents[id] ?? agents[id.replace(/-sub-\d+$/, "")];
    };
    return { name, agentOf };
  }, [agents]);
}

export function Who({ actor, size = 18, label = true }: { actor: string; size?: number; label?: boolean }) {
  const { name, agentOf } = useActor();
  const a = actor.startsWith("agent:") ? agentOf(actor) : undefined;
  const sub = /-sub-\d+$/.test(actor);
  return (
    <span className="gxwho" title={name(actor)}>
      {a ? (
        <span className={"gxav" + (sub ? " sub" : "")}>
          <Avatar agent={a} size={size} />
        </span>
      ) : (
        <span className="gxav person" style={{ width: size, height: size }}>
          <User size={size - 6} />
        </span>
      )}
      {label && <span className="gxwhon">{name(actor)}</span>}
    </span>
  );
}

/** A path that, when it does not fit, loses its middle: the start says
 *  where it is, the end what it is. */
export function MidPath({ path, className }: { path: string; className?: string }) {
  const i = path.lastIndexOf("/");
  const head = i > 0 ? path.slice(0, i + 1) : "";
  const tail = i > 0 ? path.slice(i + 1) : path;
  return (
    <span className={"cdmid" + (className ? " " + className : "")} title={path}>
      {head && <span className="cdmidh">{head}</span>}
      <span className="cdmidt">{tail}</span>
    </span>
  );
}

/** Hands the agent a message in this conversation, as if the person typed
 *  it (it waits in the queue while the agent is busy). */
export async function tellAgent(agent: AgentView, session: string, text: string): Promise<boolean> {
  try {
    const busy = agent.status === "running" || agent.queue.length > 0;
    await API.send(agent.id, { text, session: session && session !== agent.session ? session : undefined, queue: busy || undefined });
    toast(busy ? t("code.toldQueued", { name: agent.name }) : t("code.told", { name: agent.name }));
    return true;
  } catch (e) {
    toast(t("chat.sendFailed", { error: errOf(e) }));
    return false;
  }
}

/** What was already handed to the agent (a failed check at a commit, a
 *  review comment): remembered on this device (the latest 300), so it is
 *  not offered again after a reload. */
const HANDED = "crewbase.code.handed";
const handed: string[] = (() => {
  try {
    return JSON.parse(localStorage.getItem(HANDED) || "[]");
  } catch {
    return [];
  }
})();
export function useHanded() {
  const [, bump] = useState(0);
  return {
    has: (k: string) => handed.includes(k),
    mark: (...ks: string[]) => {
      handed.push(...ks.filter((k) => !handed.includes(k)));
      handed.splice(0, Math.max(0, handed.length - 300));
      try {
        localStorage.setItem(HANDED, JSON.stringify(handed));
      } catch {
        /* private mode: this page only */
      }
      bump((x) => x + 1);
    },
  };
}
