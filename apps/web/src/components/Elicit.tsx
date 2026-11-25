// An MCP server asking the person mid-call (MCP elicitation, features/mcp/
// elicit.go): a form to fill in, or a link to open in the browser. Shown in
// the conversation, in 需要你 and on the approvals page, always with the
// server's name.
//
//   form   fields from the server's flat schema, prefilled with its
//          defaults and checked before sending
//   link   the whole address, its domain picked out; opened in the system
//          browser only on 在浏览器中打开 (never fetched before)
// 拒绝 says no; 不回答 dismisses it. Neither shows what was entered.

import { useMemo, useState } from "react";
import { AlertTriangle, ExternalLink, Server } from "lucide-react";
import { API, openExternal, type ApprovalRequest, type ElicitField, type ElicitSchema } from "../api";
import { t } from "../i18n";
import { canApprove, refreshApprovals, toast, useStore } from "../store";

type Values = Record<string, unknown>;

/** the options of an enum field, with their labels */
function optionsOf(f: ElicitField): { value: string; label: string }[] | null {
  if (f.enum) return f.enum.map((v) => ({ value: v, label: v }));
  if (f.oneOf) return f.oneOf.map((o) => ({ value: o.const, label: o.title || o.const }));
  return null;
}
function itemOptionsOf(f: ElicitField): { value: string; label: string }[] | null {
  const it = f.items;
  if (!it) return null;
  if (it.enum) return it.enum.map((v) => ({ value: v, label: v }));
  if (it.anyOf) return it.anyOf.map((o) => ({ value: o.const, label: o.title || o.const }));
  return null;
}

function initial(schema?: ElicitSchema): Values {
  const out: Values = {};
  for (const [k, f] of Object.entries(schema?.properties ?? {})) {
    if (f.default !== undefined) out[k] = f.default;
    else if (f.type === "boolean") out[k] = false;
    else if (f.type === "array") out[k] = [];
  }
  return out;
}

const EMAIL = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

/** what is wrong with v for f ("" = fine); required says it must be there */
function problem(f: ElicitField, v: unknown, required: boolean): string {
  const empty = v === undefined || v === null || v === "" || (Array.isArray(v) && v.length === 0);
  if (empty) return required && f.type !== "boolean" ? t("el.required") : "";
  switch (f.type) {
    case "number":
    case "integer": {
      const n = typeof v === "number" ? v : Number(v);
      if (!Number.isFinite(n)) return t("el.notNumber");
      if (f.type === "integer" && !Number.isInteger(n)) return t("el.notInteger");
      if (f.minimum !== undefined && n < f.minimum) return t("el.min", { n: f.minimum });
      if (f.maximum !== undefined && n > f.maximum) return t("el.max", { n: f.maximum });
      return "";
    }
    case "array": {
      const a = v as unknown[];
      if (f.minItems !== undefined && a.length < f.minItems) return t("el.minItems", { n: f.minItems });
      if (f.maxItems !== undefined && a.length > f.maxItems) return t("el.maxItems", { n: f.maxItems });
      return "";
    }
    case "string": {
      const s = String(v);
      if (f.minLength !== undefined && [...s].length < f.minLength) return t("el.minLength", { n: f.minLength });
      if (f.maxLength !== undefined && [...s].length > f.maxLength) return t("el.maxLength", { n: f.maxLength });
      if (f.format === "email" && !EMAIL.test(s)) return t("el.email");
      if (f.format === "uri") {
        try {
          new URL(s);
        } catch {
          return t("el.uri");
        }
      }
      return "";
    }
  }
  return "";
}

/** the values to send: numbers as numbers, dates as the schema wants them */
function encode(schema: ElicitSchema | undefined, v: Values): Values {
  const out: Values = {};
  for (const [k, f] of Object.entries(schema?.properties ?? {})) {
    const x = v[k];
    if (x === undefined || x === "" || x === null) continue;
    if (f.type === "number" || f.type === "integer") out[k] = Number(x);
    else if (f.format === "date-time" && typeof x === "string" && /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/.test(x)) out[k] = new Date(x).toISOString();
    else out[k] = x;
  }
  return out;
}

/** The answer to a server's question: its form or its link, and 拒绝 / 不回答. */
export function ElicitAnswer({ req }: { req: ApprovalRequest }) {
  const el = req.elicit!;
  const me = useStore((s) => s.me);
  const tier = useStore((s) => s.tier);
  const may = canApprove(me, req.category);
  const [values, setValues] = useState<Values>(() => initial(el.schema));
  const [tried, setTried] = useState(false);
  const [busy, setBusy] = useState(false);
  const required = new Set(el.schema?.required ?? []);
  // required fields first, in the order the server lists them (the daemon
  // decodes the schema into a map, which loses the properties' order)
  const fields = useMemo(() => {
    const all = Object.entries(el.schema?.properties ?? {});
    const req = (el.schema?.required ?? []).flatMap((k) => all.filter(([n]) => n === k));
    return [...req, ...all.filter(([n]) => !required.has(n))];
  }, [el.schema]);
  const errors = useMemo(() => Object.fromEntries(fields.map(([k, f]) => [k, problem(f, values[k], required.has(k))])), [values, el.schema]);
  const bad = Object.values(errors).some(Boolean);

  const answer = async (outcome: "allowed-once" | "rejected" | "cancelled", vals?: Values) => {
    setBusy(true);
    try {
      await API.decide(req.callId, { outcome, values: vals });
      toast(outcome === "allowed-once" ? t(el.mode === "url" ? "el.toastOpened" : "el.toastSent", { server: el.server }) : outcome === "rejected" ? t("el.toastDeclined") : t("el.toastDismissed"));
    } catch (e: any) {
      toast(t("appr.failed", { error: e?.message ?? String(e) }));
    } finally {
      setBusy(false);
      void refreshApprovals();
    }
  };

  const no = (
    <>
      <button className="btn" type="button" disabled={busy} onClick={() => void answer("rejected")}>
        {t("el.decline")}
      </button>
      <button className="btn ghost" type="button" disabled={busy} onClick={() => void answer("cancelled")}>
        {t("el.dismiss")}
      </button>
    </>
  );

  if (tier === "viewer") return <p className="small apprro">{t("team.viewerApproval")}</p>;

  if (el.mode === "url") {
    const url = el.url ?? "";
    let host = "";
    try {
      host = new URL(url).host;
    } catch {
      /* the daemon only passes http(s) links */
    }
    const at = host ? url.indexOf(host) : -1;
    const suspicious = /(^|\.)xn--/i.test(host) || /[^\x00-\x7f]/.test(host);
    return (
      <div className="elans">
        <div className="elurl" title={el.url}>
          {at >= 0 ? (
            <>
              <span className="muted">{url.slice(0, at)}</span>
              <b>{host}</b>
              <span className="muted">{url.slice(at + host.length)}</span>
            </>
          ) : (
            url
          )}
        </div>
        {suspicious && (
          <p className="small elwarn">
            <AlertTriangle size={13} />
            {t("el.punycode")}
          </p>
        )}
        <div className="acts">
          {may ? (
            <button
              className="btn pri"
              type="button"
              disabled={busy}
              onClick={() => {
                openExternal(url);
                void answer("allowed-once");
              }}
            >
              <ExternalLink size={14} />
              {t("el.open", { host: host || url })}
            </button>
          ) : (
            <span className="small apprro">{t("team.cannotApprove", { category: req.category })}</span>
          )}
          {no}
        </div>
      </div>
    );
  }

  const set = (k: string, v: unknown) => setValues((x) => ({ ...x, [k]: v }));
  return (
    <form
      className="elans"
      onSubmit={(e) => {
        e.preventDefault();
        setTried(true);
        if (!bad) void answer("allowed-once", encode(el.schema, values));
      }}
    >
      {fields.map(([k, f]) => {
        const label = f.title || k;
        const err = tried ? errors[k] : "";
        const opts = optionsOf(f);
        const itemOpts = itemOptionsOf(f);
        const id = `el-${req.callId}-${k}`;
        let input: React.ReactNode;
        if (f.type === "boolean") {
          input = (
            <span className="elbool">
              <button className="tg" type="button" role="switch" aria-checked={!!values[k]} aria-labelledby={id} onClick={() => set(k, !values[k])} />
            </span>
          );
        } else if (f.type === "array" && itemOpts) {
          const cur = (values[k] as string[]) ?? [];
          input = (
            <span className="elchips" role="group" aria-labelledby={id}>
              {itemOpts.map((o) => (
                <button key={o.value} type="button" className={"elchip" + (cur.includes(o.value) ? " on" : "")} aria-pressed={cur.includes(o.value)}
                  onClick={() => set(k, cur.includes(o.value) ? cur.filter((x) => x !== o.value) : [...cur, o.value])}>
                  {o.label}
                </button>
              ))}
            </span>
          );
        } else if (opts && opts.length <= 4) {
          input = (
            <span className="seg elseg" role="radiogroup" aria-labelledby={id}>
              {opts.map((o) => (
                <button key={o.value} type="button" role="radio" aria-checked={values[k] === o.value} aria-pressed={values[k] === o.value} onClick={() => set(k, o.value)}>
                  {o.label}
                </button>
              ))}
            </span>
          );
        } else if (opts) {
          input = (
            <span className="ssel">
              <select id={id + "-i"} value={(values[k] as string) ?? ""} onChange={(e) => set(k, e.target.value || undefined)}>
                <option value="">{t("el.pick")}</option>
                {opts.map((o) => (
                  <option key={o.value} value={o.value}>
                    {o.label}
                  </option>
                ))}
              </select>
            </span>
          );
        } else {
          const num = f.type === "number" || f.type === "integer";
          const type = num ? "number" : f.format === "email" ? "email" : f.format === "uri" ? "url" : f.format === "date" ? "date" : f.format === "date-time" ? "datetime-local" : "text";
          input = (
            <input
              id={id + "-i"}
              className="fi"
              type={type}
              inputMode={num ? "decimal" : undefined}
              step={f.type === "integer" ? 1 : undefined}
              min={f.minimum}
              max={f.maximum}
              maxLength={f.maxLength}
              autoComplete="off"
              spellCheck={false}
              value={(values[k] as string | number | undefined) ?? ""}
              onChange={(e) => set(k, e.target.value)}
            />
          );
        }
        return (
          <div key={k} className={"elfield" + (f.type === "boolean" ? " row" : "") + (err ? " bad" : "")}>
            <label id={id} htmlFor={id + "-i"}>
              {label}
              {required.has(k) && f.type !== "boolean" && <span className="req">*</span>}
            </label>
            {input}
            {f.description && <span className="small muted">{f.description}</span>}
            {err && <span className="small elerr">{err}</span>}
          </div>
        );
      })}
      <div className="acts">
        {may ? (
          <button className="btn pri" type="submit" disabled={busy || (tried && bad)}>
            {fields.length ? t("el.submit") : t("el.confirm")}
          </button>
        ) : (
          <span className="small apprro">{t("team.cannotApprove", { category: req.category })}</span>
        )}
        {no}
      </div>
    </form>
  );
}

/** "repos 需要你填写" with the server's mark. */
export function ElicitHead({ req, agentName }: { req: ApprovalRequest; agentName: string }) {
  const el = req.elicit!;
  return (
    <>
      <h4 className="elh">
        <Server size={15} />
        {t(el.mode === "url" ? "el.wantsLink" : "el.wantsForm", { server: el.server })}
      </h4>
      <div className="qs">{t("el.during", { agent: agentName })}</div>
      {el.message && <p className="elmsg">{el.message}</p>}
    </>
  );
}
