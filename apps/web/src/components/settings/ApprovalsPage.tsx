// 设置 › 审批: what waits for approval, the choices remembered, history.

import { EmptyState } from "../EmptyState";
import { Skeleton } from "../Skeleton";
import { useEffect, useState } from "react";
import { CheckCheck, Info, Workflow } from "lucide-react";
import { Head } from "./Head";
import { catLabel } from "./labels";
import { API, type ApprovalHistoryItem, type RememberedChoice } from "../../api";
import { t } from "../../i18n";
import { ago } from "../../format";
import { openSession, toast, useStore } from "../../store";
import { toolLabel } from "../Conversation";
import { Avatar } from "../Avatar";
import { ApprovalActions } from "../Approval";

import { confirmDialog } from "../Confirm";

/** 设置 › 审批: what waits for the person, the remembered choices and what
 *  was decided. */
export function Approvals() {
  const approvals = useStore((s) => s.approvals);
  const agents = useStore((s) => s.agents);
  return (
    <>
      <Head title={t("set.approvals")} sub={t("sub.approvals")} />
      <div className="vb">
        <div className="vsec">{t("set.pending", { n: approvals.length })}</div>
        {approvals.length === 0 && <EmptyState icon={<CheckCheck size={22} />} title={t("appr.none")} desc={t("es.approvalsDesc")} />}
        {approvals.map((r) => {
          const a = agents[r.agent];
          return (
            <div className="ap" key={r.callId}>
              {a ? <Avatar agent={a} size={36} /> : <span className="wfi big"><Workflow size={17} /></span>}
              <div style={{ minWidth: 0 }}>
                <div className="small" style={{ display: "flex", gap: 6, alignItems: "center", flexWrap: "wrap" }}>
                  <span className="pill amber">{t("appr.waiting")}</span>
                  <span title={r.reason}>
                    {a?.name ?? (r.agent || t("appr.workflow"))} · {whyLabel(r)} · {ago(r.asked)}
                  </span>
                </div>
                <h4>{r.summary || r.tool}</h4>
                <div className="small" title={[r.tool, r.category].filter(Boolean).join(" · ")}>
                  {toolLabel(r.tool) || r.tool}
                </div>
              </div>
              <ApprovalActions
                req={r}
                extra={
                  <button className="btn ghost" type="button" onClick={() => openSession(r.session)}>
                    {t("appr.open")}
                  </button>
                }
              />
            </div>
          );
        })}
        <Remembered />
        <History />
        <div className="box rulenote">
          <Info size={16} />
          <div>
            <b>{t("set.whichAsk")}</b>
            <span>{t("set.whichAskDesc")}</span>
          </div>
        </div>
      </div>
    </>
  );
}

function Remembered() {
  const agents = useStore((s) => s.agents);
  const n = useStore((s) => s.approvals.length);
  const [list, setList] = useState<RememberedChoice[] | null>(null);
  const load = () => API.remembered().then((r) => setList(r.remembered ?? []), () => setList([]));
  useEffect(() => {
    void load();
  }, [n]);
  const revoke = async (c: RememberedChoice) => {
    const name = agents[c.agent]?.name ?? c.agent;
    if (!(await confirmDialog(t("set.revokeConfirm", { agent: name, category: c.category })))) return;
    try {
      await API.forget(c.agent, c.category);
      toast(t("set.revoked", { agent: name, category: c.category }));
      void load();
    } catch (e: any) {
      toast(e?.message ?? String(e));
    }
  };
  return (
    <>
      <div className="vsec">
        {t("set.remembered")} {list && list.length > 0 ? `· ${list.length}` : ""}
      </div>
      {list === null ? (
        <Skeleton />
      ) : list.length === 0 ? (
        <p className="small" style={{ margin: 0 }}>
          {t("set.rememberedNone")}
        </p>
      ) : (
        <div className="sbox">
          {list.map((c) => {
            const a = agents[c.agent];
            return (
              <div className="srow" key={c.agent + "/" + c.category}>
                <Avatar agent={a} size={28} />
                <div className="g">
                  <b>{t("set.rememberedRow", { agent: a?.name ?? c.agent, category: catLabel(c.category) })}</b>
                  <span>
                    {t("set.rememberedAt", { time: ago(c.at) })}
                    {c.overridden ? ` · ${t("set.overridden")}` : ""}
                  </span>
                </div>
                <div className="c">
                  <button className="btn" type="button" onClick={() => revoke(c)}>
                    {t("set.revoke")}
                  </button>
                </div>
              </div>
            );
          })}
        </div>
      )}
    </>
  );
}

/** Why a request asks, in people's words (the engine's reason, in English,
 *  only as a tooltip). */
function whyLabel(r: { tool: string; category?: string; reason?: string }): string {
  if (r.tool === "routine_create" || r.tool === "routine_update") return t("aprx.routine");
  if (r.category && r.category !== "none") return t("aprx.cat", { cat: catLabel(r.category) });
  return t("aprx.other");
}

function History() {
  const agents = useStore((s) => s.agents);
  const n = useStore((s) => s.approvals.length);
  const [list, setList] = useState<ApprovalHistoryItem[] | null>(null);
  useEffect(() => {
    API.approvalHistory(60).then((r) => setList((r.history ?? []).filter((h) => h.decision)), () => setList([]));
  }, [n]);
  return (
    <>
      <div className="vsec">{t("set.history")}</div>
      {list === null ? (
        <Skeleton />
      ) : list.length === 0 ? (
        <p className="small" style={{ margin: 0 }}>
          {t("set.historyNone")}
        </p>
      ) : (
        <div className="htable">
          {list.map((h) => {
            const a = agents[h.agent];
            const o = h.decision?.outcome ?? "";
            const tone = o === "allowed-once" ? "ok" : o === "rejected" ? "red" : "";
            return (
              <button className="hrow" type="button" key={h.callId} onClick={() => openSession(h.session)}>
                <Avatar agent={a} size={22} />
                <span className="g">
                  <b>{h.summary || h.tool}</b>
                  <span>
                    {a?.name ?? h.agent} · {catLabel(h.category || h.tool)}
                    {/* a person's words on rejecting (a cancel's message is the system's) */}
                    {o === "rejected" && h.decision?.message ? ` · 「${h.decision.message}」` : ""}
                  </span>
                </span>
                <span className={"pill " + tone}>
                  {t(o === "allowed-once" ? (h.decision?.remember ? "set.hAllowedRemember" : "set.hAllowed") : o === "rejected" ? "set.hRejected" : o === "cancelled" ? "set.hCancelled" : "set.hOther")}
                </span>
                <span className="small tm">{ago(h.decided || h.asked)}</span>
              </button>
            );
          })}
        </div>
      )}
    </>
  );
}
