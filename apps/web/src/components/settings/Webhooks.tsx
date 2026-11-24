// 设置 › Webhook (features/webhooks): events in and out, per team.
//
//   overview   four figures over the last 24 hours; each opens the log
//              with that filter
//   入站        endpoints third parties call (GitHub, Sentry, Stripe…): a
//              card each — what comes in, the condition, where it goes, its
//              recent activity — that opens in place to the address, how to
//              set it up at the provider, what the agents are asked, the
//              limits and its latest deliveries; edited in place too
//   出站        subscriptions: internal events sent to a URL, signed; the
//              same cards, with a test send
//   投递记录    every delivery, both ways, filtered by direction, outcome,
//              source and text, each with why it ended as it did; opens in
//              place to its headers, body, prompt and replay
// Nothing opens over the page: no dialogs, no popovers.

import { useEffect, useMemo, useRef, useState } from "react";
import {
  ArrowDownToLine, ArrowRight, ArrowUpFromLine, Check, ChevronDown, CircleAlert, Copy, Globe, Hash, KeyRound, ListTree, Pencil, Play, Plus, RadioTower, RefreshCw,
  RotateCcw, Search, Send, Trash2, Workflow as WorkflowIcon, X,
} from "lucide-react";
import {
  API, teamOf, type Routine, type WhDelivery, type WhEndpoint, type WhEndpointInput, type WhSettings, type WhSubscription, type WhTarget, type Workflow,
} from "../../api";
import { t, type Key } from "../../i18n";
import { ago } from "../../format";

/** "25 分钟前", "刚刚" */
const since = (iso: string) => {
  const a = ago(iso);
  return a === t("time.justNow") ? a : t("wh.ago", { t: a });
};
import { toast, useStore } from "../../store";
import { Head } from "../Settings";
import { confirmDialog } from "../Confirm";
import { Skeleton } from "../Skeleton";
// brand marks: simple-icons (CC0), filled with currentColor
import githubIcon from "../../assets/webhooks/github.svg?raw";
import gitlabIcon from "../../assets/webhooks/gitlab.svg?raw";
import sentryIcon from "../../assets/webhooks/sentry.svg?raw";
import linearIcon from "../../assets/webhooks/linear.svg?raw";
import stripeIcon from "../../assets/webhooks/stripe.svg?raw";

type Tab = "in" | "out" | "log";
const errOf = (e: any) => e?.message ?? String(e);
const DAY = 24 * 3600 * 1000;

const PROVIDER_NAME: Record<string, string> = {
  github: "GitHub", gitlab: "GitLab", sentry: "Sentry", linear: "Linear", stripe: "Stripe", "hmac-sha256": "HMAC", bearer: "Bearer",
};
const BRAND: Record<string, string> = { github: githubIcon, gitlab: gitlabIcon, sentry: sentryIcon, linear: linearIcon, stripe: stripeIcon };
const QUICK = ["github", "gitlab", "sentry", "linear", "stripe"];
// common event types per provider (suggestions; anything can be typed)
const EVENT_HINTS: Record<string, string[]> = {
  github: ["pull_request", "push", "check_run", "workflow_run", "issues", "issue_comment", "release"],
  gitlab: ["merge_request", "push", "pipeline", "issue", "note"],
  sentry: ["issue.created", "issue.resolved", "event_alert.triggered", "metric_alert.critical"],
  linear: ["Issue.create", "Issue.update", "Comment.create"],
  stripe: ["invoice.payment_failed", "charge.dispute.created", "customer.subscription.deleted"],
};
const FILTER_EXAMPLES: Record<string, string> = {
  github: `conclusion == "failure"`,
  gitlab: `object_attributes.state == "failed"`,
  sentry: `data.issue.level in ["error", "fatal"]`,
  linear: `data.priority <= 2`,
  stripe: `data.object.amount_due > 10000`,
};

/** A provider's mark: its logo, or an icon for the generic kinds. */
function ProviderIcon({ p, size = 18 }: { p: string; size?: number }) {
  const svg = BRAND[p];
  if (svg) return <span className="whicon" style={{ width: size, height: size }} aria-hidden dangerouslySetInnerHTML={{ __html: svg }} />;
  return <KeyRound size={size - 2} aria-hidden />;
}

// ---------------------------------------------------------------- outcomes

type Tone = "ok" | "mute" | "warn" | "bad" | "info";
const TONE: Record<string, Tone> = {
  accepted: "ok", delivered: "ok", "dry-run": "info", filtered: "mute", duplicate: "mute", cooldown: "warn", limited: "warn", partial: "warn", pending: "info", rejected: "bad", dead: "bad",
};
const toneOf = (s: string): Tone => TONE[s] ?? "mute";

function StatusPill({ s }: { s: string }) {
  const k = `wh.st.${s}` as Key;
  return (
    <span className={"whpill t-" + toneOf(s)}>
      <i />
      {t(k) === k ? s : t(k)}
    </span>
  );
}

/** Why a delivery ended as it did, in a few words (the daemon's own words
 *  when it is not one of the usual reasons). */
function reasonOf(d: WhDelivery, e?: WhEndpoint): string {
  const err = d.error ?? "";
  if (!err) {
    if (d.dir === "out" && d.code) return "HTTP " + d.code;
    if (d.status === "pending" && d.next) return t("wh.r.retryAt", { at: new Date(d.next).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) });
    return "";
  }
  if (err === "the endpoint has no targets") return t("wh.r.noTargets");
  if (err.startsWith("condition not met")) return t("wh.r.condition");
  if (err.startsWith("condition: ")) return t("wh.r.conditionErr", { e: err.slice(11) });
  if (/is not one of/.test(err)) return t("wh.r.event", { ev: d.event || "?" });
  if (err.startsWith("signature does not match")) return t("wh.r.signature");
  if (/missing|no signature/i.test(err)) return t("wh.r.noSignature");
  if (/expired|too old|timestamp/i.test(err)) return t("wh.r.expired");
  if (err.startsWith("cooldown")) return e?.cooldown ? t("wh.r.cooldownN", { n: Math.round(e.cooldown / 60) || 1, key: e.cooldownKey || "" }) : t("wh.r.cooldown");
  if (/rate|limit/i.test(err) && d.status === "limited") return e?.rateLimit ? t("wh.r.limitedN", { n: e.rateLimit }) : t("wh.r.limited");
  if (/duplicate|already/i.test(err)) return t("wh.r.duplicate");
  if (err.startsWith("the receiver answered")) return t("wh.r.answered", { code: err.replace(/\D+/g, "") });
  if (err === "no target took the event") return t("wh.r.noneTook");
  return err;
}

/** The last deliveries as a strip of bars, oldest first, coloured by outcome. */
function Activity({ list, n = 24 }: { list: WhDelivery[]; n?: number }) {
  const shown = list.slice(0, n).reverse();
  const pad = Math.max(0, n - shown.length);
  return (
    <span className="whact" aria-hidden>
      {Array.from({ length: pad }, (_, i) => (
        <i key={"p" + i} className="t-none" />
      ))}
      {shown.map((d) => (
        <i key={d.id} className={"t-" + toneOf(d.status)} title={`${d.event || d.name} · ${t(`wh.st.${d.status}` as Key)} · ${since(d.at)}`} />
      ))}
    </span>
  );
}

// ---------------------------------------------------------------- data

function useTeam() {
  return useStore((s) => s.team);
}

/** the team's webhooks and their recent deliveries, refreshed on webhooks.changed */
function useData() {
  const team = useTeam();
  const tick = useStore((s) => s.webhookTick);
  const [n, setN] = useState(0);
  const [data, setData] = useState<Awaited<ReturnType<typeof API.webhooks>> | null>(null);
  const [log, setLog] = useState<WhDelivery[] | null>(null);
  useEffect(() => {
    let live = true;
    API.webhooks(team).then(
      (d) => live && setData(d),
      (e) => live && toast(errOf(e)),
    );
    API.deliveries(team, undefined, 500).then(
      (r) => live && setLog(r.deliveries),
      () => live && setLog([]),
    );
    return () => void (live = false);
  }, [team, tick, n]);
  return { team, data, log, reload: () => setN((x) => x + 1) };
}

type LogFilter = { dir: "" | "in" | "out"; tone: "" | Tone; of: string; q: string };
const NO_FILTER: LogFilter = { dir: "", tone: "", of: "", q: "" };

/** A copy-to-clipboard field. */
function CopyField({ value, mono = true }: { value: string; mono?: boolean }) {
  const [done, setDone] = useState(false);
  return (
    <div className="whcopy">
      <code className={mono ? "" : "plain"} title={value}>
        {value}
      </code>
      <button
        type="button"
        className="ix"
        aria-label={t("wh.copy")}
        title={t("wh.copy")}
        onClick={() => {
          void navigator.clipboard?.writeText(value);
          setDone(true);
          setTimeout(() => setDone(false), 1200);
        }}
      >
        {done ? <Check size={14} /> : <Copy size={14} />}
      </button>
    </div>
  );
}

// ---------------------------------------------------------------- page

export function Webhooks() {
  const { team, data, log, reload } = useData();
  const [tab, setTab] = useState<Tab>("in");
  const [filter, setFilter] = useState<LogFilter>(NO_FILTER);
  const openLog = (f: Partial<LogFilter>) => {
    setFilter({ ...NO_FILTER, ...f });
    setTab("log");
  };
  const now = Date.now();
  const day = (log ?? []).filter((d) => now - new Date(d.at).getTime() < DAY);
  const alarm = day.some((d) => d.status === "rejected" || d.status === "dead");
  return (
    <>
      <Head title={t("set.webhooks")} sub={t("sub.webhooks")} />
      <div className="vb wh">
        {data && log ? (
          <>
            {(data.endpoints.length > 0 || data.subscriptions.length > 0 || log.length > 0) && <Overview data={data} day={day} onOpen={openLog} />}
            <div className="whtabs" role="tablist">
              <button type="button" role="tab" aria-selected={tab === "in"} onClick={() => setTab("in")}>
                <ArrowDownToLine size={15} />
                {t("wh.tab.in")}
                <span className="n">{data.endpoints.length}</span>
              </button>
              <button type="button" role="tab" aria-selected={tab === "out"} onClick={() => setTab("out")}>
                <ArrowUpFromLine size={15} />
                {t("wh.tab.out")}
                <span className="n">{data.subscriptions.length}</span>
              </button>
              <button type="button" role="tab" aria-selected={tab === "log"} onClick={() => setTab("log")}>
                <ListTree size={15} />
                {t("wh.tab.log")}
                {alarm && <i className="dot" aria-label={t("wh.logAlarm")} />}
              </button>
            </div>
            {tab === "in" ? (
              <Inbound team={team} data={data} log={log} reload={reload} openLog={openLog} />
            ) : tab === "out" ? (
              <Outbound team={team} data={data} log={log} reload={reload} openLog={openLog} />
            ) : (
              <Log log={log} data={data} filter={filter} setFilter={setFilter} />
            )}
          </>
        ) : (
          <Skeleton kind="rows" />
        )}
      </div>
    </>
  );
}

function Overview({ data, day, onOpen }: { data: Awaited<ReturnType<typeof API.webhooks>>; day: WhDelivery[]; onOpen: (f: Partial<LogFilter>) => void }) {
  const ins = day.filter((d) => d.dir === "in");
  const handled = ins.filter((d) => toneOf(d.status) === "ok");
  const quiet = ins.filter((d) => toneOf(d.status) === "mute" || toneOf(d.status) === "warn");
  const bad = ins.filter((d) => d.status === "rejected");
  const dead = day.filter((d) => d.dir === "out" && d.status === "dead");
  const pending = day.filter((d) => d.dir === "out" && d.status === "pending");
  const enabled = data.endpoints.filter((e) => e.enabled).length;
  const cells: { k: string; tone: Tone; label: string; value: number; sub: string; f: Partial<LogFilter> }[] = [
    { k: "in", tone: "info", label: t("wh.ov.in"), value: ins.length, sub: t("wh.ov.inSub", { n: enabled }), f: { dir: "in" } },
    { k: "ok", tone: "ok", label: t("wh.ov.ok"), value: handled.length, sub: quiet.length ? t("wh.ov.okSub", { n: quiet.length }) : t("wh.ov.okNone"), f: { dir: "in", tone: "ok" } },
    {
      k: "bad", tone: bad.length ? "bad" : "mute", label: t("wh.ov.bad"), value: bad.length,
      sub: bad.length ? `${bad[0].name} · ${since(bad[0].at)}` : t("wh.ov.badNone"), f: { dir: "in", tone: "bad" },
    },
    {
      k: "dead", tone: dead.length ? "bad" : pending.length ? "info" : "mute", label: t("wh.ov.dead"), value: dead.length,
      sub: dead.length ? `${dead[0].name} · ${t("wh.ov.tried", { n: dead[0].attempts ?? 0 })}` : pending.length ? t("wh.ov.retrying", { n: pending.length }) : data.subscriptions.length ? t("wh.ov.deadNone") : t("wh.ov.noSubs"),
      f: { dir: "out", tone: dead.length ? "bad" : "" },
    },
  ];
  return (
    <div className="whov">
      {cells.map((c) => (
        <button key={c.k} type="button" className={"whovc t-" + c.tone} onClick={() => onOpen(c.f)} title={t("wh.ov.open")}>
          <span className="l">
            <i />
            {c.label}
          </span>
          <b>{c.value}</b>
          <span className="s">{c.sub}</span>
        </button>
      ))}
    </div>
  );
}

// ---------------------------------------------------------------- shared bits

function TargetChip({ t: tg }: { t: WhTarget }) {
  const channels = useStore((s) => s.channels);
  const agents = useStore((s) => s.agents);
  const [names, setNames] = useState<Record<string, string>>({});
  useEffect(() => {
    if (tg.kind === "workflow") API.workflows().then((r) => setNames(Object.fromEntries(r.workflows.map((w) => [w.id, w.name]))), () => {});
    if (tg.kind === "routine") API.routines().then((r) => setNames(Object.fromEntries(r.routines.map((x) => [x.id, x.name]))), () => {});
  }, [tg.kind]);
  const icon = tg.kind === "channel" ? <Hash size={12} /> : tg.kind === "workflow" ? <WorkflowIcon size={12} /> : <RefreshCw size={12} />;
  const name = tg.kind === "channel" ? channels[tg.id]?.title ?? tg.id : names[tg.id] ?? tg.id;
  return (
    <span className={"whtgt k-" + tg.kind} title={t(`wh.kind.${tg.kind}` as Key)}>
      {icon}
      {name || t("wh.pick")}
      {tg.mention && <span className="m">@{agents[tg.mention]?.name ?? tg.mention}</span>}
    </span>
  );
}

/** The latest deliveries of one endpoint or subscription, each opening in place. */
function Recent({ list, e, onAll }: { list: WhDelivery[]; e?: WhEndpoint; onAll: () => void }) {
  const [open, setOpen] = useState<string | null>(null);
  if (!list.length) return null;
  return (
    <div className="whsec">
      <div className="whsech">
        {t("wh.recent")}
        <button type="button" className="linkbtn small" onClick={onAll}>
          {t("wh.allDeliveries", { n: list.length })}
          <ArrowRight size={12} />
        </button>
      </div>
      <ul className="whrows">
        {list.slice(0, 5).map((d) => (
          <DeliveryRow key={d.id} d={d} e={e} open={open === d.id} onToggle={() => setOpen(open === d.id ? null : d.id)} compact />
        ))}
      </ul>
    </div>
  );
}

function DeliveryRow({ d, e, open, onToggle, compact }: { d: WhDelivery; e?: WhEndpoint; open: boolean; onToggle: () => void; compact?: boolean }) {
  const why = reasonOf(d, e);
  return (
    <li className={open ? "on" : ""}>
      <button type="button" className="whrow" aria-expanded={open} onClick={onToggle}>
        {d.dir === "in" ? <ArrowDownToLine size={14} className="muted" /> : <ArrowUpFromLine size={14} className="muted" />}
        {!compact && <span className="nm">{d.name}</span>}
        <span className="ev">{d.event || "—"}</span>
        <span className="why" title={d.error || why}>
          {why}
        </span>
        {d.via === "relay" && <RadioTower size={13} className="muted" aria-label={t("wh.viaRelay")} />}
        <StatusPill s={d.status} />
        <span className="at">{since(d.at)}</span>
        <ChevronDown size={14} className="chev" />
      </button>
      {open && <DeliveryDetail id={d.id} />}
    </li>
  );
}

/** Cards open in place: a header that toggles, its own controls kept out
 *  of the toggle. */
function useOpen(initial = false) {
  const [open, setOpen] = useState(initial);
  return { open, toggle: () => setOpen((x) => !x), set: setOpen };
}

const stop = (e: { stopPropagation: () => void }) => e.stopPropagation();

function SecretOnce({ secret, label }: { secret: string; label: string }) {
  return (
    <div className="whonce">
      <CircleAlert size={16} />
      <div className="g">
        <b>{label}</b>
        <CopyField value={secret} />
      </div>
    </div>
  );
}

// ---------------------------------------------------------------- 入站

type Data = Awaited<ReturnType<typeof API.webhooks>>;

function Inbound({ team, data, log, reload, openLog }: { team: string; data: Data; log: WhDelivery[]; reload: () => void; openLog: (f: Partial<LogFilter>) => void }) {
  const [q, setQ] = useState("");
  const [adding, setAdding] = useState<string | null>(null); // the provider a new endpoint starts with
  const [editing, setEditing] = useState<string | null>(null);
  const [fresh, setFresh] = useState<{ id: string; secret: string } | null>(null);
  const byEp = useMemo(() => {
    const m: Record<string, WhDelivery[]> = {};
    for (const d of log) if (d.dir === "in" && d.endpoint) (m[d.endpoint] ??= []).push(d);
    return m;
  }, [log]);
  const ql = q.trim().toLowerCase();
  const lastAt = (id: string) => (byEp[id]?.[0] ? new Date(byEp[id][0].at).getTime() : 0);
  const sorted = [...data.endpoints].sort((a, b) => Number(b.enabled) - Number(a.enabled) || lastAt(b.id) - lastAt(a.id) || a.name.localeCompare(b.name));
  const shown = sorted.filter(
    (e) => !ql || e.name.toLowerCase().includes(ql) || (PROVIDER_NAME[e.provider] ?? e.provider).toLowerCase().includes(ql) || (e.events ?? []).some((x) => x.toLowerCase().includes(ql)),
  );
  const saved = (e: WhEndpoint, secret: string) => {
    setAdding(null);
    setEditing(null);
    if (secret) setFresh({ id: e.id, secret });
    reload();
  };
  return (
    <div className="whlist">
      {data.endpoints.length > 0 && (
        <div className="whbar">
          <label className="search whsearch">
            <Search size={14} />
            <input value={q} placeholder={t("wh.searchIn")} onChange={(e) => setQ(e.target.value)} />
            {q && (
              <button type="button" className="ix" aria-label={t("wh.clearSearch")} onClick={() => setQ("")}>
                <X size={13} />
              </button>
            )}
          </label>
          <span className="sp" />
          {adding === null && (
            <button type="button" className="btn pri" onClick={() => setAdding("github")}>
              <Plus size={14} />
              {t("wh.newEndpoint")}
            </button>
          )}
        </div>
      )}
      {adding !== null && <EndpointEditor team={team} e={null} provider={adding} providers={data.providers} onClose={() => setAdding(null)} onSaved={saved} />}
      {data.endpoints.length === 0 && adding === null && (
        <div className="whempty">
          <span className="whemptyic">
            <ArrowDownToLine size={22} />
          </span>
          <b>{t("wh.emptyIn")}</b>
          <span className="small muted">{t("wh.emptyInSub")}</span>
          <button type="button" className="btn pri" onClick={() => setAdding("github")}>
            <Plus size={14} />
            {t("wh.newEndpoint")}
          </button>
          <div className="whquick">
            {QUICK.map((p) => (
              <button key={p} type="button" className="whqk" onClick={() => setAdding(p)}>
                <ProviderIcon p={p} size={15} />
                {PROVIDER_NAME[p]}
              </button>
            ))}
          </div>
        </div>
      )}
      {shown.map((e) =>
        editing === e.id ? (
          <EndpointEditor key={e.id} team={team} e={e} provider={e.provider} providers={data.providers} onClose={() => setEditing(null)} onSaved={saved} />
        ) : (
          <EndpointCard
            key={e.id}
            e={e}
            team={team}
            list={byEp[e.id] ?? []}
            secret={fresh?.id === e.id ? fresh.secret : ""}
            onEdit={() => setEditing(e.id)}
            onChanged={reload}
            onAll={() => openLog({ dir: "in", of: e.id })}
          />
        ),
      )}
      {data.endpoints.length > 0 && shown.length === 0 && <p className="small muted whnone">{t("wh.noMatch", { q })}</p>}
    </div>
  );
}

/** the address a provider calls: the relay's when it is on, else this computer's */
function useAddresses(e: WhEndpoint, open: boolean) {
  const [relay, setRelay] = useState<string | null>(null);
  useEffect(() => {
    if (open) API.relay().then((r) => setRelay(r.configured && r.status?.relay ? r.status.relay : null), () => setRelay(null));
  }, [open]);
  const local = `${location.origin}/api/webhooks/ingest/${e.id}`;
  const viaRelay = e.relay && e.relayToken && relay ? `${relay.replace(/\/$/, "")}/v1/hooks/${e.relayToken}` : null;
  return { local, viaRelay, relayConfigured: !!relay };
}

/** How to set the endpoint up at its provider, in its own words. */
function SetupSteps({ e }: { e: WhEndpoint }) {
  const k = `wh.setup.${e.provider}` as Key;
  const steps = t(k) === k ? t("wh.setup.generic") : t(k);
  return (
    <ol className="whsteps">
      {steps.split("\n").map((s, i) => (
        <li key={i}>{s.replace("{events}", e.events?.length ? e.events.join(", ") : t("wh.anyEvent"))}</li>
      ))}
    </ol>
  );
}

function EndpointCard({ e, team, list, secret, onEdit, onChanged, onAll }: { e: WhEndpoint; team: string; list: WhDelivery[]; secret: string; onEdit: () => void; onChanged: () => void; onAll: () => void }) {
  const o = useOpen(!!secret);
  const { local, viaRelay, relayConfigured } = useAddresses(e, o.open);
  const last = list[0];
  const toggle = async () => {
    try {
      await API.saveEndpoint(team, e.id, { enabled: !e.enabled });
      onChanged();
    } catch (err) {
      toast(errOf(err));
    }
  };
  const remove = async () => {
    if (!(await confirmDialog({ text: t("wh.deleteEndpointQ", { name: e.name }), ok: t("common.delete"), danger: true }))) return;
    try {
      await API.deleteEndpoint(team, e.id);
      onChanged();
    } catch (err) {
      toast(errOf(err));
    }
  };
  const limits = [
    e.cooldown ? (e.cooldownKey ? t("wh.lim.cooldownKey", { key: e.cooldownKey, n: Math.round(e.cooldown / 60) || 1 }) : t("wh.lim.cooldown", { n: Math.round(e.cooldown / 60) || 1 })) : "",
    e.rateLimit ? t("wh.lim.rate", { n: e.rateLimit }) : "",
    t(`wh.mode.${e.mode || "sandbox"}` as Key),
  ].filter(Boolean);
  return (
    <article className={"whcard" + (e.enabled ? "" : " off") + (o.open ? " open" : "")}>
      <div className="whcmain" role="button" tabIndex={0} aria-expanded={o.open} onClick={o.toggle} onKeyDown={(x) => (x.key === "Enter" || x.key === " ") && x.target === x.currentTarget && (x.preventDefault(), o.toggle())}>
        <span className={"whtile p-" + e.provider}>
          <ProviderIcon p={e.provider} size={20} />
        </span>
        <div className="whcbody">
          <div className="whct">
            <b>{e.name}</b>
            {e.relay && (
              <span className="whbadge" title={t("wh.relayOnHint")}>
                <RadioTower size={12} />
                {t("wh.viaRelay")}
              </span>
            )}
            {!e.enabled && <span className="whbadge mute">{t("wh.off")}</span>}
          </div>
          <div className="whflow">
            {e.events?.length ? e.events.map((x) => <span key={x} className="whev">{x}</span>) : <span className="whev any">{t("wh.anyEvent")}</span>}
            {e.filter && (
              <>
                <ArrowRight size={13} className="arr" />
                <code className="whcond" title={e.filter}>
                  {e.filter}
                </code>
              </>
            )}
            <ArrowRight size={13} className="arr" />
            {e.targets.length ? e.targets.map((tg, i) => <TargetChip key={i} t={tg} />) : <span className="whwarn">{t("wh.noTargets")}</span>}
          </div>
          <div className="whcfoot">
            <Activity list={list} />
            <span className="small muted">{last ? `${since(last.at)} · ${t(`wh.st.${last.status}` as Key)}` : t("wh.notYet")}</span>
          </div>
        </div>
        <span className="whcside" onClick={stop}>
          <button className="tg" type="button" role="switch" aria-checked={e.enabled} aria-label={t("wh.enabled")} onClick={() => void toggle()} />
          <button type="button" className="ix chev" aria-label={o.open ? t("wh.collapse") : t("wh.expand")} onClick={o.toggle}>
            <ChevronDown size={16} />
          </button>
        </span>
      </div>
      {o.open && (
        <div className="whcdet">
          {secret && <SecretOnce secret={secret} label={t("wh.secretOnce")} />}
          <dl className="whkv">
            <dt>{viaRelay ? t("wh.addrRelay") : t("wh.addrLocal")}</dt>
            <dd>
              <CopyField value={viaRelay ?? local} />
              {!viaRelay && e.relay && !relayConfigured && <p className="small whwarn">{t("wh.relayNotConfigured")}</p>}
              {!viaRelay && !e.relay && <p className="small muted">{t("wh.localHint")}</p>}
            </dd>
            <dt>{t("wh.setupAt", { p: PROVIDER_NAME[e.provider] ?? e.provider })}</dt>
            <dd>
              <SetupSteps e={e} />
            </dd>
            {e.instruction && (
              <>
                <dt>{t("wh.instruction")}</dt>
                <dd className="whinstr">{e.instruction}</dd>
              </>
            )}
            <dt>{t("wh.limits")}</dt>
            <dd>{limits.join(" · ")}</dd>
          </dl>
          <Recent list={list} e={e} onAll={onAll} />
          {!list.length && <p className="small muted whwait">{t("wh.waiting")}</p>}
          <div className="whcacts">
            <button type="button" className="btn sm" onClick={onEdit}>
              <Pencil size={13} />
              {t("common.edit")}
            </button>
            <span className="sp" />
            <button type="button" className="btn sm ghost dngt" onClick={() => void remove()}>
              <Trash2 size={13} />
              {t("common.delete")}
            </button>
          </div>
        </div>
      )}
    </article>
  );
}

/** Edits an endpoint in place (or makes one): where it comes from, how it
 *  is checked, when it counts, where it goes, and the limits. */
function EndpointEditor({ team, e, provider, providers, onClose, onSaved }: {
  team: string; e: WhEndpoint | null; provider: string; providers: string[]; onClose: () => void; onSaved: (e: WhEndpoint, secret: string) => void;
}) {
  const [f, setF] = useState<WhEndpointInput>(() =>
    e
      ? { name: e.name, provider: e.provider, events: e.events ?? [], filter: e.filter ?? "", instruction: e.instruction ?? "", targets: e.targets, cooldown: e.cooldown ?? 0, cooldownKey: e.cooldownKey ?? "", rateLimit: e.rateLimit ?? 0, mode: e.mode ?? "sandbox", relay: !!e.relay }
      : { name: "", provider, events: [], filter: "", instruction: "", targets: [], cooldown: 0, cooldownKey: "", rateLimit: 0, mode: "sandbox", relay: false },
  );
  const [secret, setSecret] = useState("");
  const [custom, setCustom] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [adv, setAdv] = useState(!!(e && (e.cooldown || e.rateLimit || e.relay || (e.mode && e.mode !== "sandbox"))));
  const top = useRef<HTMLFormElement>(null);
  useEffect(() => {
    top.current?.scrollIntoView({ block: "nearest", behavior: "smooth" });
  }, []);
  const set = <K extends keyof WhEndpointInput>(k: K, v: WhEndpointInput[K]) => (setF((x) => ({ ...x, [k]: v })), setErr(""));
  const p = f.provider ?? "github";
  const generates = p === "stripe" || p === "sentry" || p === "linear";
  const evs = f.events ?? [];
  const hints = EVENT_HINTS[p] ?? [];
  const toggleEv = (x: string) => set("events", evs.includes(x) ? evs.filter((y) => y !== x) : [...evs, x]);
  const addCustom = () => {
    const x = custom.trim();
    if (x && !evs.includes(x)) set("events", [...evs, x]);
    setCustom("");
  };
  const save = async () => {
    setBusy(true);
    try {
      const r = await API.saveEndpoint(team, e?.id ?? null, { ...f, name: f.name?.trim() || `${PROVIDER_NAME[p] ?? p}`, secret: secret.trim() || undefined });
      onSaved(r.endpoint, r.secret);
      toast(t("wh.saved"));
    } catch (x) {
      setErr(errOf(x));
    } finally {
      setBusy(false);
    }
  };
  const order = [...QUICK, ...providers.filter((x) => !QUICK.includes(x))].filter((x) => providers.includes(x));
  return (
    <form ref={top} className="whcard whedit" onSubmit={(x) => (x.preventDefault(), void save())}>
      <header className="wheh">
        <b>{e ? t("wh.editEndpoint") : t("wh.newEndpoint")}</b>
        <button type="button" className="ix" aria-label={t("common.close")} onClick={onClose}>
          <X size={15} />
        </button>
      </header>

      <section className="whstep">
        <h5>
          <span>1</span>
          {t("wh.s.from")}
        </h5>
        <div className="whprovs" role="radiogroup" aria-label={t("wh.provider")}>
          {order.map((x) => (
            <button key={x} type="button" role="radio" aria-checked={p === x} disabled={!!e && x !== p} className={"whprov" + (p === x ? " on" : "")} onClick={() => set("provider", x)}>
              <ProviderIcon p={x} size={16} />
              {PROVIDER_NAME[x] ?? x}
            </button>
          ))}
        </div>
        <label className="fl">
          {t("wh.name")}
          <input className="fi" value={f.name ?? ""} placeholder={t("wh.namePh")} onChange={(x) => set("name", x.target.value)} />
        </label>
        <label className="fl">
          {t("wh.secret")}
          <input
            className="fi mono"
            type="password"
            autoComplete="off"
            value={secret}
            onChange={(x) => setSecret(x.target.value)}
            placeholder={e ? t("wh.secretKeep") : generates ? t("wh.secretPaste", { p: PROVIDER_NAME[p] ?? p }) : t("wh.secretGen")}
          />
          <span className="small muted">{generates ? t("wh.secretFrom", { p: PROVIDER_NAME[p] ?? p }) : t("wh.secretHint")}</span>
        </label>
      </section>

      <section className="whstep">
        <h5>
          <span>2</span>
          {t("wh.s.when")}
        </h5>
        <div className="fl">
          {t("wh.events")}
          <div className="whevpick">
            {[...hints, ...evs.filter((x) => !hints.includes(x))].map((x) => (
              <button key={x} type="button" className={"whevp" + (evs.includes(x) ? " on" : "")} aria-pressed={evs.includes(x)} onClick={() => toggleEv(x)}>
                {evs.includes(x) && <Check size={12} />}
                {x}
              </button>
            ))}
            <span className="whevadd">
              <input
                value={custom}
                placeholder={t("wh.eventOther")}
                onChange={(x) => setCustom(x.target.value)}
                onKeyDown={(x) => x.key === "Enter" && (x.preventDefault(), addCustom())}
                onBlur={addCustom}
              />
            </span>
          </div>
          <span className="small muted">{evs.length ? t("wh.eventsN", { n: evs.length }) : t("wh.eventsAll")}</span>
        </div>
        <label className="fl">
          {t("wh.filter")}
          <input className="fi mono" value={f.filter ?? ""} placeholder={FILTER_EXAMPLES[p] ?? `action == "opened"`} onChange={(x) => set("filter", x.target.value)} />
          <span className="small muted">
            {t("wh.filterHint")}
            {FILTER_EXAMPLES[p] && !f.filter && (
              <>
                {" "}
                <button type="button" className="linkbtn" onClick={() => set("filter", FILTER_EXAMPLES[p])}>
                  {t("wh.useExample")}
                </button>
              </>
            )}
          </span>
        </label>
      </section>

      <section className="whstep">
        <h5>
          <span>3</span>
          {t("wh.s.to")}
        </h5>
        <TargetsEditor team={team} value={f.targets ?? []} onChange={(v) => set("targets", v)} />
        <label className="fl">
          {t("wh.instruction")}
          <textarea className="fi" rows={3} value={f.instruction ?? ""} placeholder={t("wh.instructionPh")} onChange={(x) => set("instruction", x.target.value)} />
          <span className="small muted">{t("wh.instructionHint")}</span>
        </label>
      </section>

      <section className={"whstep adv" + (adv ? " open" : "")}>
        <button type="button" className="whadvh" aria-expanded={adv} onClick={() => setAdv(!adv)}>
          <ChevronDown size={14} />
          {t("wh.s.limits")}
          {!adv && <span className="small muted">{t("wh.s.limitsSub")}</span>}
        </button>
        {adv && (
          <>
            <div className="whgrid three">
              <label className="fl">
                {t("wh.cooldown")}
                <input className="fi" type="number" min={0} value={f.cooldown || ""} placeholder="0" onChange={(x) => set("cooldown", Number(x.target.value) || 0)} />
              </label>
              <label className="fl">
                {t("wh.cooldownKey")}
                <input className="fi mono" value={f.cooldownKey ?? ""} placeholder="repository.full_name" onChange={(x) => set("cooldownKey", x.target.value)} />
              </label>
              <label className="fl">
                {t("wh.rateLimit")}
                <input className="fi" type="number" min={0} value={f.rateLimit || ""} placeholder="30" onChange={(x) => set("rateLimit", Number(x.target.value) || 0)} />
              </label>
            </div>
            <div className="whgrid">
              <div className="fl">
                {t("wh.mode")}
                <span className="seg whmode">
                  {(["sandbox", "read-only"] as const).map((m) => (
                    <button key={m} type="button" aria-pressed={f.mode === m} onClick={() => set("mode", m)}>
                      {t(`wh.mode.${m}` as Key)}
                    </button>
                  ))}
                </span>
              </div>
              <div className="fl whrelay">
                <span className="whrelayh">
                  <button className="tg" type="button" role="switch" aria-checked={!!f.relay} aria-label={t("wh.relay")} onClick={() => set("relay", !f.relay)} />
                  {t("wh.relay")}
                </span>
                <span className="small muted">{t("wh.relayHint")}</span>
              </div>
            </div>
          </>
        )}
      </section>

      {err && <p className="small errt">{err}</p>}
      <footer className="whefoot">
        <button type="button" className="btn ghost" onClick={onClose}>
          {t("common.cancel")}
        </button>
        <button type="submit" className="btn pri" disabled={busy}>
          {busy ? <span className="spin sm" /> : null}
          {e ? t("common.save") : t("wh.create")}
        </button>
      </footer>
    </form>
  );
}

/** the team's workflows, routines and channels a target can be */
function useTargets(team: string) {
  const agents = useStore((s) => s.agents);
  const channels = useStore((s) => s.channels);
  const [wfs, setWfs] = useState<Workflow[]>([]);
  const [rts, setRts] = useState<Routine[]>([]);
  useEffect(() => {
    API.workflows().then((r) => setWfs(r.workflows), () => {});
    API.routines().then((r) => setRts(r.routines), () => {});
  }, []);
  const inTeam = (id: string) => !!agents[id] && teamOf(agents[id]) === team;
  return {
    workflows: wfs.filter((w) => (w.steps ?? []).every((s: any) => !s.agent || inTeam(s.agent))),
    routines: rts.filter((r) => inTeam(r.agent)),
    channels: Object.values(channels).filter((c) => teamOf(c) === team && c.state !== "archived"),
  };
}

function TargetsEditor({ team, value, onChange }: { team: string; value: WhTarget[]; onChange: (v: WhTarget[]) => void }) {
  const opts = useTargets(team);
  const agents = useStore((s) => s.agents);
  const setAt = (i: number, tg: WhTarget) => onChange(value.map((x, j) => (j === i ? tg : x)));
  return (
    <div className="fl">
      {t("wh.targets")}
      <div className="whtargets">
        {value.map((tg, i) => {
          const list: { id: string; name: string }[] =
            tg.kind === "workflow" ? opts.workflows.map((w) => ({ id: w.id, name: w.name })) : tg.kind === "routine" ? opts.routines.map((r) => ({ id: r.id, name: r.name })) : opts.channels.map((c) => ({ id: c.id, name: "#" + c.title }));
          const ch = tg.kind === "channel" ? opts.channels.find((c) => c.id === tg.id) : undefined;
          return (
            <div key={i} className="whtg">
              <span className="seg sm whkinds">
                {(["channel", "routine", "workflow"] as const).map((k) => (
                  <button key={k} type="button" aria-pressed={tg.kind === k} onClick={() => setAt(i, { kind: k, id: "" })}>
                    {k === "channel" ? <Hash size={12} /> : k === "workflow" ? <WorkflowIcon size={12} /> : <RefreshCw size={12} />}
                    {t(`wh.kind.${k}` as Key)}
                  </button>
                ))}
              </span>
              <span className="ssel">
                <select value={tg.id} onChange={(x) => setAt(i, { ...tg, id: x.target.value, mention: undefined })}>
                  <option value="">{list.length ? t("wh.pick") : t(`wh.none.${tg.kind}` as Key)}</option>
                  {list.map((o) => (
                    <option key={o.id} value={o.id}>
                      {o.name}
                    </option>
                  ))}
                </select>
              </span>
              {ch && (
                <span className="ssel">
                  <select value={tg.mention ?? ""} onChange={(x) => setAt(i, { ...tg, mention: x.target.value || undefined })}>
                    <option value="">{t("wh.toOwner")}</option>
                    {ch.members.map((m) => (
                      <option key={m} value={m}>
                        @{agents[m]?.name ?? m}
                      </option>
                    ))}
                  </select>
                </span>
              )}
              <button type="button" className="ix" aria-label={t("common.delete")} onClick={() => onChange(value.filter((_, j) => j !== i))}>
                <X size={14} />
              </button>
            </div>
          );
        })}
        <button type="button" className="btn sm ghost whaddt" onClick={() => onChange([...value, { kind: "channel", id: "" }])}>
          <Plus size={13} />
          {t("wh.addTarget")}
        </button>
        {value.length === 0 && <p className="small whwarn">{t("wh.targetsHint")}</p>}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------- 出站

function Outbound({ team, data, log, reload, openLog }: { team: string; data: Data; log: WhDelivery[]; reload: () => void; openLog: (f: Partial<LogFilter>) => void }) {
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<string | null>(null);
  const [fresh, setFresh] = useState<{ id: string; secret: string } | null>(null);
  const bySub = useMemo(() => {
    const m: Record<string, WhDelivery[]> = {};
    for (const d of log) if (d.dir === "out" && d.subscription) (m[d.subscription] ??= []).push(d);
    return m;
  }, [log]);
  const saved = (x: WhSubscription, secret: string) => {
    setAdding(false);
    setEditing(null);
    if (secret) setFresh({ id: x.id, secret });
    reload();
  };
  return (
    <div className="whlist">
      {data.subscriptions.length > 0 && (
        <div className="whbar">
          <p className="small muted">{t("wh.outIntro")}</p>
          <span className="sp" />
          {!adding && (
            <button type="button" className="btn pri" onClick={() => setAdding(true)}>
              <Plus size={14} />
              {t("wh.newSub")}
            </button>
          )}
        </div>
      )}
      {adding && <SubEditor team={team} x={null} events={data.events} onClose={() => setAdding(false)} onSaved={saved} />}
      {data.subscriptions.length === 0 && !adding && (
        <div className="whempty">
          <span className="whemptyic">
            <ArrowUpFromLine size={22} />
          </span>
          <b>{t("wh.emptyOut")}</b>
          <span className="small muted">{t("wh.emptyOutSub")}</span>
          <button type="button" className="btn pri" onClick={() => setAdding(true)}>
            <Plus size={14} />
            {t("wh.newSub")}
          </button>
        </div>
      )}
      {data.subscriptions.map((x) =>
        editing === x.id ? (
          <SubEditor key={x.id} team={team} x={x} events={data.events} onClose={() => setEditing(null)} onSaved={saved} />
        ) : (
          <SubCard key={x.id} x={x} team={team} list={bySub[x.id] ?? []} secret={fresh?.id === x.id ? fresh.secret : ""} onEdit={() => setEditing(x.id)} onChanged={reload} onAll={() => openLog({ dir: "out", of: x.id })} />
        ),
      )}
      <OutSettings />
    </div>
  );
}

const evLabel = (e: string) => (t(`wh.ev.${e}` as Key) === `wh.ev.${e}` ? e : t(`wh.ev.${e}` as Key));

function SubCard({ x, team, list, secret, onEdit, onChanged, onAll }: { x: WhSubscription; team: string; list: WhDelivery[]; secret: string; onEdit: () => void; onChanged: () => void; onAll: () => void }) {
  const o = useOpen(!!secret);
  const host = (() => {
    try {
      return new URL(x.url).host;
    } catch {
      return x.url;
    }
  })();
  const last = list[0];
  const act = async (f: () => Promise<unknown>, msg?: string) => {
    try {
      await f();
      if (msg) toast(msg);
      onChanged();
    } catch (e) {
      toast(errOf(e));
    }
  };
  return (
    <article className={"whcard" + (x.enabled ? "" : " off") + (o.open ? " open" : "")}>
      <div className="whcmain" role="button" tabIndex={0} aria-expanded={o.open} onClick={o.toggle} onKeyDown={(e) => (e.key === "Enter" || e.key === " ") && e.target === e.currentTarget && (e.preventDefault(), o.toggle())}>
        <span className="whtile">
          <Globe size={19} />
        </span>
        <div className="whcbody">
          <div className="whct">
            <b>{x.name}</b>
            <span className="whhost">{host}</span>
            {!x.enabled && <span className="whbadge mute">{t("wh.off")}</span>}
          </div>
          <div className="whflow">
            {x.events.map((e) => (
              <span key={e} className="whev">
                {evLabel(e)}
              </span>
            ))}
            {x.filter && (
              <>
                <ArrowRight size={13} className="arr" />
                <code className="whcond" title={x.filter}>
                  {x.filter}
                </code>
              </>
            )}
          </div>
          <div className="whcfoot">
            <Activity list={list} />
            <span className="small muted">{last ? `${since(last.at)} · ${t(`wh.st.${last.status}` as Key)}${last.code ? " · HTTP " + last.code : ""}` : t("wh.notYetOut")}</span>
          </div>
        </div>
        <span className="whcside" onClick={stop}>
          <button className="tg" type="button" role="switch" aria-checked={x.enabled} aria-label={t("wh.enabled")} onClick={() => void act(() => API.saveSubscription(team, x.id, { enabled: !x.enabled }))} />
          <button type="button" className="ix chev" aria-label={o.open ? t("wh.collapse") : t("wh.expand")} onClick={o.toggle}>
            <ChevronDown size={16} />
          </button>
        </span>
      </div>
      {o.open && (
        <div className="whcdet">
          {secret && <SecretOnce secret={secret} label={t("wh.signSecretOnce")} />}
          <dl className="whkv">
            <dt>URL</dt>
            <dd>
              <CopyField value={x.url} />
            </dd>
            <dt>{t("wh.signing")}</dt>
            <dd className="small">{t("wh.signingHow")}</dd>
          </dl>
          <Recent list={list} onAll={onAll} />
          <div className="whcacts">
            <button type="button" className="btn sm" onClick={() => void act(() => API.testSubscription(team, x.id), t("wh.testQueued"))}>
              <Send size={13} />
              {t("wh.test")}
            </button>
            <button type="button" className="btn sm ghost" onClick={onEdit}>
              <Pencil size={13} />
              {t("common.edit")}
            </button>
            <span className="sp" />
            <button
              type="button"
              className="btn sm ghost dngt"
              onClick={async () => (await confirmDialog({ text: t("wh.deleteSubQ", { name: x.name }), ok: t("common.delete"), danger: true })) && act(() => API.deleteSubscription(team, x.id))}
            >
              <Trash2 size={13} />
              {t("common.delete")}
            </button>
          </div>
        </div>
      )}
    </article>
  );
}

function SubEditor({ team, x, events, onClose, onSaved }: { team: string; x: WhSubscription | null; events: string[]; onClose: () => void; onSaved: (x: WhSubscription, secret: string) => void }) {
  const [name, setName] = useState(x?.name ?? "");
  const [url, setUrl] = useState(x?.url ?? "");
  const [evs, setEvs] = useState<string[]>(x?.events ?? ["workflow.finished", "routine.finished"]);
  const [filter, setFilter] = useState(x?.filter ?? "");
  const [secret, setSecret] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const top = useRef<HTMLFormElement>(null);
  useEffect(() => {
    top.current?.scrollIntoView({ block: "nearest", behavior: "smooth" });
  }, []);
  const save = async () => {
    setBusy(true);
    try {
      const r = await API.saveSubscription(team, x?.id ?? null, { name, url, events: evs, filter, secret: secret.trim() || undefined });
      onSaved(r.subscription, r.secret);
      toast(t("wh.saved"));
    } catch (e) {
      setErr(errOf(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <form ref={top} className="whcard whedit" onSubmit={(e) => (e.preventDefault(), void save())}>
      <header className="wheh">
        <b>{x ? t("wh.editSub") : t("wh.newSub")}</b>
        <button type="button" className="ix" aria-label={t("common.close")} onClick={onClose}>
          <X size={15} />
        </button>
      </header>
      <section className="whstep">
        <h5>
          <span>1</span>
          {t("wh.s.where")}
        </h5>
        <div className="whgrid">
          <label className="fl">
            {t("wh.name")}
            <input className="fi" value={name} placeholder={t("wh.subNamePh")} onChange={(e) => setName(e.target.value)} />
          </label>
          <label className="fl">
            URL
            <input className="fi mono" value={url} placeholder="https://example.com/hooks/crewbase" onChange={(e) => (setUrl(e.target.value), setErr(""))} />
          </label>
        </div>
      </section>
      <section className="whstep">
        <h5>
          <span>2</span>
          {t("wh.s.what")}
        </h5>
        <div className="whevs">
          {events.map((e) => (
            <label key={e} className={"whevc" + (evs.includes(e) ? " on" : "")}>
              <input type="checkbox" checked={evs.includes(e)} onChange={() => setEvs(evs.includes(e) ? evs.filter((y) => y !== e) : [...evs, e])} />
              <span className="g">
                <b>{evLabel(e)}</b>
                <code>{e}</code>
              </span>
            </label>
          ))}
        </div>
        <label className="fl">
          {t("wh.filter")}
          <input className="fi mono" value={filter} placeholder={`status != "quiet"`} onChange={(e) => setFilter(e.target.value)} />
          <span className="small muted">{t("wh.outFilterHint")}</span>
        </label>
      </section>
      <section className="whstep">
        <h5>
          <span>3</span>
          {t("wh.s.sign")}
        </h5>
        <label className="fl">
          {t("wh.signSecret")}
          <input className="fi mono" type="password" autoComplete="off" value={secret} onChange={(e) => setSecret(e.target.value)} placeholder={x ? t("wh.secretKeep") : t("wh.secretGen")} />
          <span className="small muted">{t("wh.signHint")}</span>
        </label>
      </section>
      {err && <p className="small errt">{err}</p>}
      <footer className="whefoot">
        <button type="button" className="btn ghost" onClick={onClose}>
          {t("common.cancel")}
        </button>
        <button type="submit" className="btn pri" disabled={busy || !url.trim() || evs.length === 0}>
          {busy ? <span className="spin sm" /> : null}
          {x ? t("common.save") : t("wh.create")}
        </button>
      </footer>
    </form>
  );
}

function OutSettings() {
  const [s, setS] = useState<WhSettings | null>(null);
  const [allow, setAllow] = useState("");
  const [busy, setBusy] = useState(false);
  const [open, setOpen] = useState(false);
  useEffect(() => {
    API.webhookSettings().then(
      (x) => (setS(x), setAllow(x.allowPrivate.join(", "))),
      () => {},
    );
  }, []);
  if (!s) return null;
  const save = async () => {
    setBusy(true);
    try {
      const x = await API.setWebhookSettings({ ...s, allowPrivate: allow.split(",").map((y) => y.trim()).filter(Boolean) });
      setS(x);
      toast(t("wh.saved"));
    } catch (e) {
      toast(errOf(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <section className={"whset" + (open ? " open" : "")}>
      <button type="button" className="whadvh" aria-expanded={open} onClick={() => setOpen(!open)}>
        <ChevronDown size={14} />
        {t("wh.outSettings")}
        {!open && <span className="small muted">{t("wh.outSettingsSub", { n: s.maxAttempts, k: s.allowPrivate.length })}</span>}
      </button>
      {open && (
        <>
          <label className="fl">
            {t("wh.allowPrivate")}
            <input className="fi mono" value={allow} placeholder="10.0.0.0/8, 192.168.1.20" onChange={(e) => setAllow(e.target.value)} />
            <span className="small muted">{t("wh.allowPrivateHint")}</span>
          </label>
          <div className="whsetr">
            <label className="fl">
              {t("wh.maxAttempts")}
              <input className="fi" type="number" min={1} max={20} value={s.maxAttempts} onChange={(e) => setS({ ...s, maxAttempts: Number(e.target.value) })} />
            </label>
            <span className="sp" />
            <button type="button" className="btn sm" disabled={busy} onClick={() => void save()}>
              {t("common.save")}
            </button>
          </div>
        </>
      )}
    </section>
  );
}

// ---------------------------------------------------------------- 投递记录

const TONES: ("" | Tone)[] = ["", "ok", "mute", "warn", "bad", "info"];

function dayLabel(iso: string): string {
  const d = new Date(iso);
  const today = new Date();
  const y = new Date(today);
  y.setDate(today.getDate() - 1);
  if (d.toDateString() === today.toDateString()) return t("time.today");
  if (d.toDateString() === y.toDateString()) return t("time.yesterday");
  return d.toLocaleDateString([], { month: "long", day: "numeric", weekday: "short" });
}

function Log({ log, data, filter, setFilter }: { log: WhDelivery[]; data: Data; filter: LogFilter; setFilter: (f: LogFilter) => void }) {
  const [open, setOpen] = useState<string | null>(null);
  const [limit, setLimit] = useState(100);
  const eps = useMemo(() => Object.fromEntries(data.endpoints.map((e) => [e.id, e])), [data]);
  const sources = [...data.endpoints.map((e) => ({ id: e.id, name: e.name, dir: "in" as const })), ...data.subscriptions.map((x) => ({ id: x.id, name: x.name, dir: "out" as const }))];
  const ql = filter.q.trim().toLowerCase();
  const base = log.filter((d) => (!filter.dir || d.dir === filter.dir) && (!filter.of || d.endpoint === filter.of || d.subscription === filter.of));
  const matchQ = (d: WhDelivery) => !ql || d.name.toLowerCase().includes(ql) || (d.event ?? "").toLowerCase().includes(ql) || (d.error ?? "").toLowerCase().includes(ql);
  const counts = Object.fromEntries(TONES.map((x) => [x, base.filter((d) => matchQ(d) && (!x || toneOf(d.status) === x)).length]));
  const shown = base.filter((d) => matchQ(d) && (!filter.tone || toneOf(d.status) === filter.tone));
  const set = (f: Partial<LogFilter>) => setFilter({ ...filter, ...f });
  const any = filter.dir || filter.tone || filter.of || filter.q;
  // rows by day
  const groups: { day: string; rows: WhDelivery[] }[] = [];
  for (const d of shown.slice(0, limit)) {
    const k = dayLabel(d.at);
    const g = groups[groups.length - 1];
    if (g && g.day === k) g.rows.push(d);
    else groups.push({ day: k, rows: [d] });
  }
  return (
    <div className="whlog">
      <div className="whlogbar">
        <span className="seg sm">
          {(["", "in", "out"] as const).map((d) => (
            <button key={d || "all"} type="button" aria-pressed={filter.dir === d} onClick={() => set({ dir: d, of: d && filter.of && !sources.some((s) => s.id === filter.of && s.dir === d) ? "" : filter.of })}>
              {t(`wh.dir.${d || "all"}` as Key)}
            </button>
          ))}
        </span>
        <span className="ssel">
          <select value={filter.of} onChange={(e) => set({ of: e.target.value })} aria-label={t("wh.source")}>
            <option value="">{t("wh.allSources")}</option>
            {sources
              .filter((s) => !filter.dir || s.dir === filter.dir)
              .map((s) => (
                <option key={s.id} value={s.id}>
                  {s.dir === "in" ? "↓ " : "↑ "}
                  {s.name}
                </option>
              ))}
          </select>
        </span>
        <label className="search whsearch">
          <Search size={14} />
          <input value={filter.q} placeholder={t("wh.searchLog")} onChange={(e) => set({ q: e.target.value })} />
        </label>
        {any && (
          <button type="button" className="linkbtn small" onClick={() => setFilter(NO_FILTER)}>
            {t("wh.clearFilters")}
          </button>
        )}
      </div>
      <div className="whtones" role="radiogroup" aria-label={t("wh.outcome")}>
        {TONES.map((x) =>
          x && !counts[x] && filter.tone !== x ? null : (
            <button key={x || "all"} type="button" role="radio" aria-checked={filter.tone === x} className={"whtone" + (x ? " t-" + x : "") + (filter.tone === x ? " on" : "")} onClick={() => set({ tone: x })}>
              {x && <i />}
              {t(`wh.tone.${x || "all"}` as Key)}
              <span>{counts[x]}</span>
            </button>
          ),
        )}
      </div>
      {shown.length === 0 ? (
        <div className="whempty sm">
          <b>{log.length ? t("wh.noMatchLog") : t("wh.noDeliveries")}</b>
          {any && (
            <button type="button" className="btn sm" onClick={() => setFilter(NO_FILTER)}>
              {t("wh.clearFilters")}
            </button>
          )}
        </div>
      ) : (
        <>
          {groups.map((g) => (
            <section key={g.day} className="whday">
              <h5>{g.day}</h5>
              <ul className="whrows">
                {g.rows.map((d) => (
                  <DeliveryRow key={d.id} d={d} e={d.endpoint ? eps[d.endpoint] : undefined} open={open === d.id} onToggle={() => setOpen(open === d.id ? null : d.id)} />
                ))}
              </ul>
            </section>
          ))}
          {shown.length > limit && (
            <button type="button" className="btn sm ghost whmore" onClick={() => setLimit(limit + 100)}>
              {t("wh.more", { n: shown.length - limit })}
            </button>
          )}
        </>
      )}
    </div>
  );
}

function DeliveryDetail({ id }: { id: string }) {
  const team = useTeam();
  const agents = useStore((s) => s.agents);
  const agentName = (id: string) => agents[id]?.name ?? id;
  const tick = useStore((s) => s.webhookTick);
  const [d, setD] = useState<WhDelivery | null>(null);
  const [dry, setDry] = useState<WhDelivery | null>(null);
  const [busy, setBusy] = useState(false);
  const [tab, setTab] = useState<"body" | "headers" | "prompt">("body");
  useEffect(() => {
    API.delivery(team, id).then(setD, (e) => toast(errOf(e)));
  }, [team, id, tick]);
  if (!d)
    return (
      <div className="whdet">
        <Skeleton kind="code" />
      </div>
    );
  const body = d.body !== undefined ? JSON.stringify(d.body, null, 2) : d.bodyText ?? "";
  const heads = d.headers && Object.keys(d.headers).length ? Object.entries(d.headers).map(([k, v]) => `${k}: ${v}`).join("\n") : "";
  const tabs = [["body", t("wh.body"), body], ["headers", t("wh.headers"), heads], ["prompt", t("wh.prompt"), d.prompt ?? ""]].filter((x) => x[2]) as ["body" | "headers" | "prompt", string, string][];
  const cur = tabs.find((x) => x[0] === tab) ?? tabs[0];
  const replay = async (isDry: boolean) => {
    if (!isDry && !(await confirmDialog({ text: t("wh.replayQ"), ok: t("wh.replay") }))) return;
    setBusy(true);
    try {
      const r = await API.replayDelivery(team, d.id, isDry);
      if (isDry) setDry(r);
      else toast(t("wh.replayed"));
    } catch (e) {
      toast(errOf(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="whdet">
      <div className="whdetmeta small">
        <span>{new Date(d.at).toLocaleString()}</span>
        {d.key && <span className="mono">{d.key}</span>}
        {d.dir === "out" && <span>{t("wh.attempts", { n: d.attempts ?? 0 })}</span>}
        {d.dir === "out" && d.status === "pending" && d.next && <span>{t("wh.nextAt", { at: new Date(d.next).toLocaleTimeString() })}</span>}
        {d.code ? <span>HTTP {d.code}</span> : null}
      </div>
      {d.error && <p className="small whwarn mono">{d.error}</p>}
      {!!d.targets?.length && (
        <div className="whtres">
          {d.targets.map((tr, i) => (
            <div key={i} className={"whtr" + (tr.ok ? " ok" : " bad")}>
              {tr.ok ? <Check size={13} /> : <X size={13} />}
              <TargetChip t={tr} />
              <span className="small muted">{tr.ok ? (tr.kind === "channel" && tr.detail ? t("wh.wentTo", { name: agentName(tr.detail) }) : "") : tr.error}</span>
            </div>
          ))}
        </div>
      )}
      {cur && (
        <div className="whcode">
          {tabs.length > 1 && (
            <span className="seg sm">
              {tabs.map(([k, l]) => (
                <button key={k} type="button" aria-pressed={cur[0] === k} onClick={() => setTab(k)}>
                  {l}
                </button>
              ))}
            </span>
          )}
          <pre className="whpre">{cur[2]}</pre>
        </div>
      )}
      <div className="whdeta">
        {d.dir === "in" && d.status !== "rejected" && (
          <>
            <button type="button" className="btn sm ghost" disabled={busy} onClick={() => void replay(true)} title={t("wh.dryRunHint")}>
              <Play size={13} />
              {t("wh.dryRun")}
            </button>
            <button type="button" className="btn sm" disabled={busy} onClick={() => void replay(false)}>
              <RotateCcw size={13} />
              {t("wh.replay")}
            </button>
          </>
        )}
        {d.dir === "out" && d.status === "dead" && (
          <button type="button" className="btn sm" disabled={busy} onClick={() => void replay(false)}>
            <RotateCcw size={13} />
            {t("wh.resend")}
          </button>
        )}
      </div>
      {dry && (
        <div className="whdry">
          <div>
            <b className="small">{t("wh.dryResult")}</b>
            <StatusPill s={dry.status} />
            {dry.error && <span className="small muted">{reasonOf(dry)}</span>}
          </div>
          {dry.targets?.map((tr, i) => (
            <div key={i} className="whtr ok">
              <Check size={13} />
              <TargetChip t={tr} />
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

