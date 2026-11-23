import { useContext, useState } from "react";
import { API, type AgentView } from "../../api";
import { t } from "../../i18n";
import { toast } from "../../store";
import { Avatar } from "../Avatar";
import { MsgCtx, SessionCtx } from "./context";
import { speaker } from "./util";

/** 重试 on the error row of the last turn when it failed without a reply:
 *  resends that turn's messages as they were (like regenerate). */
export function RetryButton({ id }: { id: string }) {
  const acts = useContext(MsgCtx);
  const [busy, setBusy] = useState(false);
  if (!acts || acts.lastTurn?.item !== id || acts.lastTurn.from <= acts.compactedUpto) return null;
  return (
    <button
      className="linkbtn"
      type="button"
      disabled={!acts.idle || busy}
      title={acts.idle ? t("msg.retryTitle") : t("msg.busy", { name: acts.agent.name })}
      onClick={async () => {
        setBusy(true);
        await acts.resend(acts.lastTurn!.from);
        setBusy(false);
      }}
    >
      {t("msg.retry")}
    </button>
  );
}

/** A greeting's example requests: its lines starting with "> " (they show
 *  as buttons, not in the text). */
export function splitStarters(text: string): { text: string; starters: string[] } {
  const starters: string[] = [];
  const kept: string[] = [];
  for (const line of text.split("\n")) {
    const m = /^\s*>\s?(.+)$/.exec(line);
    if (m) starters.push(m[1].trim().replace(/^[「“"']|[」”"']$/g, ""));
    else kept.push(line);
  }
  return { text: kept.join("\n").trim(), starters: starters.filter(Boolean).slice(0, 4) };
}

/** Example requests the person can send with one click. */
export function Starters({ agent, items, session }: { agent: AgentView; items: string[]; session?: string }) {
  const sid = useContext(SessionCtx);
  const [busy, setBusy] = useState(false);
  if (!items.length) return null;
  const s = session ?? sid;
  return (
    <div className="starters">
      {items.map((x) => (
        <button
          key={x}
          type="button"
          className="starter"
          disabled={busy}
          onClick={async () => {
            setBusy(true);
            try {
              await API.send(agent.id, { text: x, session: s && s !== agent.session ? s : undefined });
            } catch (e: any) {
              toast(t("chat.sendFailed", { error: e?.message ?? String(e) }));
            } finally {
              setBusy(false);
            }
          }}
        >
          {x}
        </button>
      ))}
    </div>
  );
}

/** A new chat's opening: a fixed greeting, no model call. */
export function ChatOpening({ agent }: { agent: AgentView }) {
  return (
    <div className="m-agent turn opening">
      <Avatar agent={agent} size={28} />
      <div style={{ minWidth: 0 }}>
        <div className="mh">
          <b>{speaker(agent)}</b>
        </div>
        <div className="mt">
          <p>{t("chat.opening")}</p>
        </div>
      </div>
    </div>
  );
}
