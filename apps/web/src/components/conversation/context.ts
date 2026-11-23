import { createContext } from "react";
import { type AgentView } from "../../api";

// ---------------------------------------------------------------- message actions

/** Regenerate / edit in a one-to-one conversation (null elsewhere: channels
 *  and sub-agent runs offer copy only). */
export interface MsgActions {
  agent: AgentView;
  /** the agent is not working and has nothing queued */
  idle: boolean;
  /** history up to this seq is a compaction summary: no resending from it */
  compactedUpto: number;
  /** the last finished turn, when nothing came after it */
  lastTurn?: { item: string; from: number };
  /** regenerate (no text) or edit and resend from the user message seq */
  resend: (seq: number, text?: string) => Promise<boolean>;
}

export const MsgCtx = createContext<MsgActions | null>(null);
/** The session the feed shows (for its live streams). */
export const SessionCtx = createContext("");
