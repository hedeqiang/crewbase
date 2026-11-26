// 何时运行, picked rather than typed: shared by 工作流, 例行巡检 and the
// 做成例行 card in a conversation. The value is the daemon's schedule spec
// (manual, webhook, daily HH:MM, weekly mon HH:MM, every 30m, a cron line).

import { t, type Key } from "../../i18n";

type When = { kind: "manual" } | { kind: "webhook" } | { kind: "daily"; time: string } | { kind: "weekly"; day: string; time: string } | { kind: "every"; every: string } | { kind: "custom"; raw: string };
const DAYS = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"];
const EVERY = ["15m", "30m", "1h", "2h", "3h", "6h", "12h"];
const pad = (s: string) => (s.length === 4 ? "0" + s : s);

function parseWhen(v: string): When {
  const s = v.trim().toLowerCase();
  if (!s || s === "manual") return { kind: "manual" };
  if (s === "webhook") return { kind: "webhook" };
  let m = /^daily (\d\d?:\d\d)$/.exec(s);
  if (m) return { kind: "daily", time: pad(m[1]) };
  m = /^weekly (\w{3})\w* (\d\d?:\d\d)$/.exec(s);
  if (m && DAYS.includes(m[1])) return { kind: "weekly", day: m[1], time: pad(m[2]) };
  m = /^every (\d+[mh])$/.exec(s);
  if (m) return { kind: "every", every: m[1] };
  return { kind: "custom", raw: v };
}
function whenSpec(w: When): string {
  switch (w.kind) {
    case "manual":
      return "manual";
    case "webhook":
      return "webhook";
    case "daily":
      return `daily ${w.time}`;
    case "weekly":
      return `weekly ${w.day} ${w.time}`;
    case "every":
      return `every ${w.every}`;
    case "custom":
      return w.raw;
  }
}
const everyLabel = (e: string) => t("auto.every", { d: e.replace(/h$/, t("auto.hour")).replace(/m$/, t("auto.minute")) });

/** 何时运行: by hand, on a schedule (said in words, not cron), or from a
 *  webhook. noManual: a 例行巡检 always runs on its own. */
export function TriggerPicker({ value, onChange, noManual }: { value: string; onChange: (v: string) => void; noManual?: boolean }) {
  const parsed = parseWhen(value);
  const w: When = noManual && parsed.kind === "manual" ? { kind: "daily", time: "09:00" } : parsed;
  const kind = w.kind === "manual" || w.kind === "webhook" ? w.kind : "schedule";
  const set = (x: When) => onChange(whenSpec(x));
  return (
    <div className="trig">
      <div className="seg">
        {!noManual && (
          <button type="button" aria-pressed={kind === "manual"} onClick={() => set({ kind: "manual" })}>
            {t("wf.byHand")}
          </button>
        )}
        <button type="button" aria-pressed={kind === "schedule"} onClick={() => kind !== "schedule" && set({ kind: "daily", time: "09:00" })}>
          {t("wf.onSchedule")}
        </button>
        <button type="button" aria-pressed={kind === "webhook"} onClick={() => set({ kind: "webhook" })}>
          Webhook
        </button>
      </div>
      {kind === "schedule" && (
        <div className="trig-s">
          <span className="ssel sm">
            <select
              value={w.kind}
              aria-label={t("wf.often")}
              onChange={(e) => {
                const k = e.target.value;
                const time = "time" in w ? w.time : "09:00";
                if (k === "daily") set({ kind: "daily", time });
                else if (k === "weekly") set({ kind: "weekly", day: "mon", time });
                else if (k === "every") set({ kind: "every", every: "1h" });
                else set({ kind: "custom", raw: "0 9 * * 1-5" });
              }}
            >
              <option value="daily">{t("wf.daily")}</option>
              <option value="weekly">{t("wf.weekly")}</option>
              <option value="every">{t("wf.everyN")}</option>
              <option value="custom">{t("wf.cron")}</option>
            </select>
          </span>
          {w.kind === "weekly" && (
            <span className="ssel sm">
              <select value={w.day} aria-label={t("wf.day")} onChange={(e) => set({ ...w, day: e.target.value })}>
                {DAYS.map((d) => (
                  <option key={d} value={d}>
                    {t(`auto.wd.${d}` as Key)}
                  </option>
                ))}
              </select>
            </span>
          )}
          {(w.kind === "daily" || w.kind === "weekly") && (
            <input className="fi sm trig-time" type="time" value={w.time} aria-label={t("wf.time")} onChange={(e) => e.target.value && set({ ...w, time: e.target.value })} />
          )}
          {w.kind === "every" && (
            <span className="ssel sm">
              <select value={w.every} aria-label={t("wf.interval")} onChange={(e) => set({ kind: "every", every: e.target.value })}>
                {[...new Set([...EVERY, w.every])].map((e) => (
                  <option key={e} value={e}>
                    {everyLabel(e)}
                  </option>
                ))}
              </select>
            </span>
          )}
          {w.kind === "custom" && (
            <input className="fi sm mono trig-cron" value={w.raw} placeholder="0 9 * * 1-5" aria-label={t("wf.cron")} title={t("wf.cronHint")} onChange={(e) => set({ kind: "custom", raw: e.target.value })} />
          )}
        </div>
      )}
      {kind === "webhook" && <span className="small muted">{t("wf.webhookHint")}</span>}
    </div>
  );
}

