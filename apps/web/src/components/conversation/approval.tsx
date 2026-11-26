import { catLabel } from "../settings/labels";
import { Check, CircleX, AlarmClock } from "lucide-react";
import { type AgentView, type ApprovalRequest } from "../../api";
import { scheduleLabel } from "../settings/Automation";
import { t, type Key } from "../../i18n";
import { argsPreview, type FeedItem } from "../../projection";
import { useStore } from "../../store";
import { ago, modeLabel } from "../../format";
import { ApprovalActions } from "../Approval";
import { ElicitHead } from "../Elicit";
import { Markdown } from "../Markdown";
import { toolLabel } from "./steps";

/** Why a request asks, in the UI's language: the category ("删除文件"),
 *  and whether a rule or the default asked; the engine's own wording
 *  ("rules: delete requires approval (default)") is only a tooltip. */
function approvalWhy(req: { category?: string; reason?: string; asked?: string }, time: string): string {
  const cat = req.category ? catLabel(req.category) : "";
  const byRule = !!req.reason && !/\(default\)\s*$/.test(req.reason) && /^rules:/.test(req.reason);
  const when = ago(req.asked || time);
  const waited = when === t("time.justNow") ? t("appr.justAsked") : t("appr.waited", { wait: when });
  return t(byRule ? "appr.byRule" : "appr.byDefault", { cat: cat || req.reason || "", waited });
}

export function ApprovalCard({ it, agent }: { it: Extract<FeedItem, { k: "approval" }>; agent?: AgentView }) {
  const pending = useStore((s) => s.approvals.some((a) => a.callId === it.req.callId));
  const req = useStore((s) => s.approvals.find((a) => a.callId === it.req.callId)) ?? it.req;
  const name = agent?.name ?? it.req.agent;
  const summary = it.req.summary || it.req.tool;
  if (it.req.elicit) return <ElicitCard it={it} pending={pending} req={req} name={name} />;
  if (it.decided) {
    const d = it.decided;
    const by = d.by && d.by !== "desktop" ? d.by + " " : t("common.you");
    const key: Key =
      d.outcome === "allowed-once"
        ? d.remember
          ? "appr.approvedRemember"
          : "appr.approved"
        : d.outcome === "rejected"
          ? d.message
            ? "appr.rejectedWith"
            : "appr.rejected"
          : d.outcome === "cancelled"
            ? "appr.cancelled"
            : "appr.unavailable";
    return (
      <div className={"sys" + (d.outcome === "allowed-once" ? "" : " muted")}>
        {d.outcome === "allowed-once" ? <Check size={14} /> : <CircleX size={14} />}
        <span>{t(key, { by, summary, message: d.message ?? "" })}</span>
      </div>
    );
  }
  if (it.req.tool === "routine_create" || it.req.tool === "routine_update") {
    return <RoutineAsk it={it} name={name} pending={pending} req={req} />;
  }
  const cmd = it.call ? argsPreview(it.call.args) : "";
  return (
    <div className={"qcard" + (pending ? " wait" : "")}>
      {/* the title says what kind of action; the command itself shows once, below */}
      <h4>{t("appr.wants", { agent: name, summary: cmd && toolLabel(it.req.tool) ? toolLabel(it.req.tool) : summary })}</h4>
      <div className="qs">{approvalWhy(it.req, it.time)}</div>
      {cmd && <code className="cmd">{cmd}</code>}
      {req.preview && (
        <details className="apreview">
          <summary>{t("appr.preview")}</summary>
          <Markdown text={req.preview} />
        </details>
      )}
      {/* the friendly tool name (when the title did not already use it); ids only in the tooltip */}
      {toolLabel(it.req.tool) && !cmd && (
        <div className="small" title={[it.req.tool, it.req.category].filter(Boolean).join(" · ")}>
          {toolLabel(it.req.tool)}
        </div>
      )}
      {pending ? <ApprovalActions req={req} /> : <p className="small" style={{ margin: "10px 0 0" }}>{t("appr.expired")}</p>}
    </div>
  );
}

/** An agent asks to set up (or change) a routine: what, when and how, in
 *  plain words. */
function RoutineAsk({ it, name, pending, req }: { it: Extract<FeedItem, { k: "approval" }>; name: string; pending: boolean; req: ApprovalRequest }) {
  let a: { name?: string; prompt?: string; schedule?: string; mode?: string } = {};
  try {
    const raw = it.call?.args;
    a = typeof raw === "string" ? JSON.parse(raw) : (raw ?? {});
  } catch {
    /* the summary still says it */
  }
  const create = it.req.tool === "routine_create";
  if (!create && !a.schedule && !a.prompt) {
    // a change of name or mode only: the server's summary says it
    a = {};
  }
  return (
    <div className={"qcard routineask" + (pending ? " wait" : "")}>
      <h4>
        <AlarmClock size={15} />
        {create ? t("sched.askCreate", { agent: name, name: a.name ?? "" }) : t("sched.askUpdate", { agent: name })}
      </h4>
      {a.schedule || a.mode ? (
        <div className="rawhen">
          {a.schedule && <span className="pill blue">{scheduleLabel(a.schedule)}</span>}
          {(create || a.mode) && <span className="pill">{modeLabel(a.mode || "read-only")}</span>}
        </div>
      ) : (
        <div className="qs">{it.req.summary}</div>
      )}
      {a.prompt && <blockquote className="raprompt">{a.prompt}</blockquote>}
      <p className="small">{t("sched.askNote")}</p>
      {pending ? <ApprovalActions req={req} /> : <p className="small" style={{ margin: "10px 0 0" }}>{t("appr.expired")}</p>}
    </div>
  );
}

/** An MCP server's question during a call: its form or link while it
 *  waits; afterwards one line saying how it was answered (never what). */
function ElicitCard({ it, pending, req, name }: { it: Extract<FeedItem, { k: "approval" }>; pending: boolean; req: ApprovalRequest; name: string }) {
  const el = it.req.elicit!;
  if (it.decided) {
    const d = it.decided;
    const by = d.by && d.by !== "desktop" ? d.by + " " : t("common.you");
    const key: Key =
      d.outcome === "allowed-once" ? (el.mode === "url" ? "el.doneOpened" : "el.doneAnswered") : d.outcome === "rejected" ? "el.doneDeclined" : d.outcome === "cancelled" ? "el.doneDismissed" : "el.doneExpired";
    return (
      <div className={"sys" + (d.outcome === "allowed-once" ? "" : " muted")}>
        {d.outcome === "allowed-once" ? <Check size={14} /> : <CircleX size={14} />}
        <span>{t(key, { by, server: el.server })}</span>
      </div>
    );
  }
  return (
    <div className={"qcard elcard" + (pending ? " wait" : "")}>
      <ElicitHead req={it.req} agentName={name} />
      {pending ? <ApprovalActions req={req} /> : <p className="small" style={{ margin: "10px 0 0" }}>{t("appr.expired")}</p>}
    </div>
  );
}
