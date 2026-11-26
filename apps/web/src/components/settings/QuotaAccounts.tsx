// 设置 › 模型 › a gateway's 额度: the accounts behind it, grouped by vendor
// (a gateway can hold several Copilot or Codex subscriptions).
//
//   vendor card     its mark and name; with several accounts, a summary
//                   across them and 「展开全部 N 个账号」 (expands in place)
//   account         its name and plan, then each allowance: how much is
//                   used (counts when the vendor gives them), a bar, when it
//                   resets; allowances without a share show the vendor's
//                   own text (无限, $41.26)
//   balance         a prepaid account: its balance

import { useMemo, useState } from "react";
import { ChevronDown, Wallet } from "lucide-react";
import type { QuotaAccount, QuotaAccountWindow } from "../../api";
import { t } from "../../i18n";
import { until } from "../../format";
import { IconMark, vendorIcon } from "../ProviderMark";

const OPEN_KEY = "crewbase.quotaOpen";

function readOpen(): Record<string, boolean> {
  try {
    return JSON.parse(localStorage.getItem(OPEN_KEY) || "{}") || {};
  } catch {
    return {};
  }
}
function writeOpen(m: Record<string, boolean>) {
  try {
    localStorage.setItem(OPEN_KEY, JSON.stringify(m));
  } catch {
    /* private mode */
  }
}

// the vendors' window names, as people read them here
const NAMES: Record<string, string> = {
  "premium requests": "qa.n.premium",
  "chat requests": "qa.n.chat",
  completions: "qa.n.completions",
  "code completions": "qa.n.completions",
  "5 hours": "qa.n.5h",
  "5h": "qa.n.5h",
  "7 days": "qa.n.7d",
  week: "qa.n.7d",
  weekly: "qa.n.7d",
  monthly: "qa.n.month",
  total: "qa.n.total",
  "on-demand": "qa.n.ondemand",
  "free credit": "qa.n.freeCredit",
  "free usage": "qa.n.freeUsage",
  "other models": "qa.n.other",
  "cursor models": "qa.n.cursorModels",
};
function windowName(n: string): string {
  const k = NAMES[n.trim().toLowerCase()];
  return k ? t(k as any) : n;
}

const UNLIMITED = /^(unlimited|∞|无限)$/i;

// vendors' plan names, as people read them here (the rest as given)
const PLANS: Record<string, string> = { free: "qa.p.free", pro: "qa.p.pro", plus: "qa.p.plus", team: "qa.p.team", business: "qa.p.business", enterprise: "qa.p.enterprise", max: "qa.p.max", ultra: "qa.p.ultra" };
function planName(p: string): string {
  const k = PLANS[p.trim().toLowerCase().replace(/[_\s]+/g, " ")];
  return k ? t(k as any) : p.replace(/_/g, " ");
}
/** A vendor's own wording of an amount: "+$60.39 on us" → "赠送 $60.39". */
function vendorText(s: string): string {
  const m = /^\+?\s*([$¥€£]?[\d.,]+)\s+on us$/i.exec(s.trim());
  return m ? t("qa.onUs", { amount: m[1] }) : s;
}

function num(n: number): string {
  if (Math.abs(n) >= 1000) return n.toLocaleString(undefined, { maximumFractionDigits: 1 });
  return Number.isInteger(n) ? String(n) : n.toFixed(1);
}
function amount(n: number, unit?: string): string {
  if (unit && /^usd$/i.test(unit)) return "$" + n.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
  return num(n);
}

/** What a window says on its right: counts and share, or the vendor's text. */
function windowValue(w: QuotaAccountWindow): { text: string; bar: boolean } {
  if (w.display && UNLIMITED.test(w.display.trim())) return { text: t("qa.unlimited"), bar: false };
  if (w.text) return { text: w.display ? vendorText(w.display) : "—", bar: false };
  const pct = Math.round(w.used * 1000) / 10;
  if (w.limit && w.limit > 0) return { text: `${amount(w.spent ?? 0, w.unit)} / ${amount(w.limit, w.unit)} · ${t("qa.usedPct", { pct })}`, bar: true };
  return { text: t("qa.usedPct", { pct }), bar: true };
}

function tone(used: number): string {
  return used >= 1 ? "red" : used >= 0.9 ? "amber" : "";
}

function WindowCell({ w }: { w: QuotaAccountWindow }) {
  const v = windowValue(w);
  const resets = w.resetsAt && !w.resetsAt.startsWith("0001") && v.bar ? until(w.resetsAt) : "";
  return (
    <div className="qawin">
      <div className="qawh">
        <span className="qawn" title={resets ? t("qa.resets", { time: resets }) : undefined}>
          {windowName(w.name)}
          {resets && <span className="qares"> · {t("qa.resets", { time: resets })}</span>}
        </span>
        <b className={"qawv" + (v.bar && w.used >= 1 ? " errt" : "")}>{v.text}</b>
      </div>
      {v.bar && (
        <div className="meter qameter" role="meter" aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(w.used * 100)} aria-label={windowName(w.name)}>
          <i className={tone(w.used)} style={{ width: Math.max(Math.min(w.used, 1) * 100, w.used > 0 ? 2 : 0) + "%" }} />
        </div>
      )}
    </div>
  );
}

/** The allowances of several accounts, added up: counts where all have
 *  them, else the average share; text when they agree. */
function summary(accts: QuotaAccount[]): QuotaAccountWindow[] {
  const order: string[] = [];
  const by = new Map<string, QuotaAccountWindow[]>();
  for (const a of accts)
    for (const w of a.windows ?? []) {
      if (!by.has(w.name)) (order.push(w.name), by.set(w.name, []));
      by.get(w.name)!.push(w);
    }
  return order.map((name) => {
    const ws = by.get(name)!;
    const texts = ws.filter((w) => w.text || (w.display && UNLIMITED.test(w.display.trim())));
    if (texts.length === ws.length) {
      const d = texts[0].display ?? "";
      return { name, used: 0, text: true, display: texts.every((w) => (w.display ?? "") === d) ? d : t("qa.varies") };
    }
    const counted = ws.filter((w) => !w.text);
    if (counted.length && counted.every((w) => (w.limit ?? 0) > 0)) {
      const spent = counted.reduce((s, w) => s + (w.spent ?? 0), 0);
      const limit = counted.reduce((s, w) => s + (w.limit ?? 0), 0);
      return { name, used: limit ? spent / limit : 0, spent, limit, unit: counted[0].unit };
    }
    return { name, used: counted.reduce((s, w) => s + w.used, 0) / Math.max(counted.length, 1) };
  });
}

function AccountCard({ a, bare }: { a: QuotaAccount; bare?: boolean }) {
  // the account's own name when there is one (not the vendor's again)
  const name = a.account && a.account !== a.vendorName ? a.account : "";
  return (
    <div className={"qaacct" + (bare ? " bare" : "")}>
      {(name || a.plan) && (
        <div className="qaah">
          <span className="qaname" title={name}>
            {name || a.vendorName || a.vendor}
          </span>
          {a.plan && <span className="qaplan">{planName(a.plan)}</span>}
        </div>
      )}
      {a.balance ? (
        <div className="qabal">
          <Wallet size={13} />
          <span>{t("qa.balance")}</span>
          <b>{a.balance}</b>
        </div>
      ) : (
        <div className="qagrid">
          {(a.windows ?? []).map((w) => (
            <WindowCell key={w.name} w={w} />
          ))}
        </div>
      )}
    </div>
  );
}

function VendorCard({ vendor, accts }: { vendor: string; accts: QuotaAccount[] }) {
  const [open, setOpen] = useState(() => readOpen()[vendor] ?? false);
  const name = accts[0].vendorName || vendor;
  const many = accts.length > 1;
  const subs = accts.filter((a) => !a.balance);
  const sum = useMemo(() => (many && subs.length > 1 ? summary(subs) : []), [accts]);
  const toggle = () => {
    const next = !open;
    setOpen(next);
    writeOpen({ ...readOpen(), [vendor]: next });
  };
  return (
    <section className="qavendor">
      <header className="qavh">
        <span className="qavmark">
          <IconMark icon={vendorIcon(vendor) ?? vendor} id={vendor} label={name} size={18} />
        </span>
        <b>{name}</b>
        {many && (
          <button type="button" className="linkbtn small qaexp" aria-expanded={open} onClick={toggle}>
            {open ? t("qa.collapse") : t("qa.expandAll", { n: accts.length })}
            <ChevronDown size={13} />
          </button>
        )}
      </header>
      {sum.length > 0 && (
        <div className="qasum">
          <span className="qasumh small">{t("qa.allAccounts", { n: subs.length })}</span>
          <div className="qagrid">
            {sum.map((w) => (
              <WindowCell key={w.name} w={w} />
            ))}
          </div>
        </div>
      )}
      {(!many || open || sum.length === 0) && (
        <div className="qaaccts">
          {accts.map((a, i) => (
            // one prepaid account: its balance straight in the vendor's card
            <AccountCard key={(a.account || "") + i} a={a} bare={!many && !!a.balance && !a.plan} />
          ))}
        </div>
      )}
    </section>
  );
}

/** Every account behind a gateway, grouped by vendor; solo: the provider
 *  is the vendor (its page already names it), so one balance is just that. */
export function QuotaAccounts({ accounts, solo }: { accounts: QuotaAccount[]; solo?: boolean }) {
  const groups = useMemo(() => {
    const order: string[] = [];
    const m = new Map<string, QuotaAccount[]>();
    for (const a of accounts) {
      if (!m.has(a.vendor)) (order.push(a.vendor), m.set(a.vendor, []));
      m.get(a.vendor)!.push(a);
    }
    // subscriptions first, prepaid balances after
    const balanceOnly = (v: string) => m.get(v)!.every((a) => !!a.balance);
    return order.sort((x, y) => Number(balanceOnly(x)) - Number(balanceOnly(y))).map((v) => [v, m.get(v)!] as const);
  }, [accounts]);
  if (solo && accounts.length === 1 && accounts[0].balance) return <AccountCard a={accounts[0]} bare />;
  return (
    <div className="qavendors">
      {groups.map(([v, accts]) => (
        <VendorCard key={v} vendor={v} accts={accts} />
      ))}
    </div>
  );
}
