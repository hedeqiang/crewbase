import { useState } from "react";
import { API, type ApprovalRequest } from "../api";
import { t } from "../i18n";
import { refreshApprovals, toast, useStore } from "../store";

/** 批准 / 拒绝(填原因) / 以后都允许 — shared by the card, the 需要你 menu and settings. */
export function ApprovalActions({ req, small = false, extra }: { req: ApprovalRequest; small?: boolean; extra?: React.ReactNode }) {
  const agent = useStore((s) => s.agents[req.agent]);
  const name = agent?.name ?? req.agent;
  const [mode, setMode] = useState<"idle" | "reject" | "confirm">("idle");
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);

  const decide = async (outcome: "allowed-once" | "rejected", opts: { message?: string; remember?: boolean; confirmed?: boolean } = {}) => {
    setBusy(true);
    try {
      await API.decide(req.callId, { outcome, ...opts });
      toast(outcome === "rejected" ? t("appr.toastNo", { agent: name }) : t("appr.toastOk", { agent: name }));
      setMode("idle");
    } catch (e: any) {
      toast(t("appr.failed", { error: e?.message ?? String(e) }));
    } finally {
      setBusy(false);
      void refreshApprovals();
    }
  };

  if (mode === "reject")
    return (
      <div className="rejbox">
        <textarea
          className="fi"
          rows={2}
          autoFocus
          placeholder={t("appr.rejectReason")}
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) void decide("rejected", { message: reason.trim() || undefined });
            if (e.key === "Escape") setMode("idle");
          }}
        />
        <div className="acts">
          <button className="btn dng" type="button" disabled={busy} onClick={() => decide("rejected", { message: reason.trim() || undefined })}>
            {t("appr.rejectConfirm")}
          </button>
          <button className="btn ghost" type="button" onClick={() => setMode("idle")}>
            {t("common.cancel")}
          </button>
        </div>
      </div>
    );

  if (mode === "confirm")
    return (
      <div className="rejbox">
        <p className="small warnline">{t("appr.confirmRemember", { agent: name, category: req.category || req.tool })}</p>
        <div className="acts">
          <button className="btn warn" type="button" disabled={busy} onClick={() => decide("allowed-once", { remember: true, confirmed: true })}>
            {t("appr.confirmRememberOk")}
          </button>
          <button className="btn ghost" type="button" onClick={() => setMode("idle")}>
            {t("common.cancel")}
          </button>
        </div>
      </div>
    );

  return (
    <div className={"acts" + (small ? " sm" : "")}>
      <button className="btn pri" type="button" disabled={busy} onClick={() => decide("allowed-once")}>
        {t("appr.approve")}
      </button>
      <button className="btn" type="button" disabled={busy} onClick={() => setMode("reject")}>
        {t("appr.reject")}
      </button>
      {req.rememberable && (
        <button
          className="btn ghost"
          type="button"
          disabled={busy}
          title={t("appr.alwaysTitle", { agent: name, category: req.category || req.tool })}
          onClick={() => (req.confirmRemember ? setMode("confirm") : decide("allowed-once", { remember: true }))}
        >
          {t("appr.always")}
        </button>
      )}
      {extra}
    </div>
  );
}
