// 设置 › Webhook (features/webhooks): events in and out, per team.
//
//   入站     endpoints third parties call (GitHub, Sentry, Stripe…): the
//            address to paste, the provider and secret, which events count,
//            a condition, what the agents are asked, and where it goes
//   出站     subscriptions: internal events sent to a URL, signed
//   投递记录 every delivery, both ways, with what happened; replay

import { useEffect, useMemo, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import {
  ArrowDownToLine, ArrowUpFromLine, Check, CircleAlert, Copy, GitBranch, Globe, Hash, Pencil, Play, Plus, RadioTower, RefreshCw, RotateCcw, Send, Trash2, Workflow as WorkflowIcon, X,
} from "lucide-react";
import {
  API, teamOf, type Routine, type WhDelivery, type WhEndpoint, type WhEndpointInput, type WhSettings, type WhSubscription, type WhTarget, type Workflow,
} from "../../api";
import { t, type Key } from "../../i18n";
import { ago } from "../../format";
import { toast, useStore } from "../../store";
import { Head } from "../Settings";
import { confirmDialog } from "../Confirm";
import { EmptyState } from "../EmptyState";
import { Skeleton } from "../Skeleton";

type Tab = "in" | "out" | "log";
const errOf = (e: any) => e?.message ?? String(e);

const PROVIDER_NAME: Record<string, string> = {
  github: "GitHub", gitlab: "GitLab", sentry: "Sentry", linear: "Linear", stripe: "Stripe", "hmac-sha256": "HMAC (Crewbase)", bearer: "Bearer token",
};
// common event types per provider (suggestions; anything can be typed)
const EVENT_HINTS: Record<string, string[]> = {
  github: ["pull_request", "push", "check_run", "workflow_run", "issues", "issue_comment", "release"],
  gitlab: ["merge_request", "push", "pipeline", "issue", "note"],
  sentry: ["issue.created", "issue.resolved", "event_alert.triggered", "metric_alert.critical"],
  linear: ["Issue.create", "Issue.update", "Comment.create"],
  stripe: ["invoice.payment_failed", "charge.dispute.created", "customer.subscription.deleted"],
};
const FILTER_EXAMPLES: Record<string, string> = {
  github: `action == "opened" && pull_request.base.ref == "main"`,
  gitlab: `object_attributes.state == "failed"`,
  sentry: `data.issue.level in ["error", "fatal"]`,
  linear: `data.priority <= 2`,
  stripe: `data.object.amount_due > 10000`,
};

function useTeam() {
  return useStore((s) => s.team);
}

/** Dialogs render on the page itself, above the settings view's header. */
function Layer({ children }: { children: ReactNode }) {
  return createPortal(<>{children}</>, document.body);
}

/** A copy-to-clipboard field. */
function CopyField({ value, label }: { value: string; label?: string }) {
  const [done, setDone] = useState(false);
  return (
    <div className="whcopy">
      {label && <span className="small muted">{label}</span>}
      <code title={value}>{value}</code>
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
        {done ? <Check size={13} /> : <Copy size={13} />}
      </button>
    </div>
  );
}

export function Webhooks() {
  const [tab, setTab] = useState<Tab>("in");
  return (
    <>
      <Head
        title={t("set.webhooks")}
        sub={t("sub.webhooks")}
        right={
          <div className="seg">
            <button type="button" aria-pressed={tab === "in"} onClick={() => setTab("in")}>
              <ArrowDownToLine size={13} />
              {t("wh.tab.in")}
            </button>
            <button type="button" aria-pressed={tab === "out"} onClick={() => setTab("out")}>
              <ArrowUpFromLine size={13} />
              {t("wh.tab.out")}
            </button>
            <button type="button" aria-pressed={tab === "log"} onClick={() => setTab("log")}>
              {t("wh.tab.log")}
            </button>
          </div>
        }
      />
      <div className="vb">{tab === "in" ? <Inbound /> : tab === "out" ? <Outbound /> : <Log of="" />}</div>
    </>
  );
}

/** the team's webhooks, refreshed on webhooks.changed */
function useWebhooks() {
  const team = useTeam();
  const tick = useStore((s) => s.webhookTick);
  const [data, setData] = useState<Awaited<ReturnType<typeof API.webhooks>> | null>(null);
  const [n, setN] = useState(0);
  useEffect(() => {
    let live = true;
    API.webhooks(team).then(
      (d) => live && setData(d),
      (e) => live && toast(errOf(e)),
    );
    return () => void (live = false);
  }, [team, tick, n]);
  return { team, data, reload: () => setN((x) => x + 1) };
}

/** the address a provider calls: the relay's when it is on, else this computer's */
function useAddresses(e: WhEndpoint) {
  const [relay, setRelay] = useState<string | null>(null);
  useEffect(() => {
    API.relay().then((r) => setRelay(r.configured && r.status?.relay ? r.status.relay : null), () => setRelay(null));
  }, []);
  const local = `${location.origin}/api/webhooks/ingest/${e.id}`;
  const viaRelay = e.relay && e.relayToken && relay ? `${relay.replace(/\/$/, "")}/v1/hooks/${e.relayToken}` : null;
  return { local, viaRelay, relayConfigured: !!relay };
}

// ---------------------------------------------------------------- 入站

function Inbound() {
  const { team, data, reload } = useWebhooks();
  const [edit, setEdit] = useState<WhEndpoint | "new" | null>(null);
  const [shown, setShown] = useState<{ id: string; secret: string } | null>(null);
  if (!data) return <Skeleton kind="rows" />;
  return (
    <div className="wh">
      <div className="whbar">
        <p className="small muted">{t("wh.inIntro")}</p>
        <button type="button" className="btn pri sm" onClick={() => setEdit("new")}>
          <Plus size={13} />
          {t("wh.newEndpoint")}
        </button>
      </div>
      {data.endpoints.length === 0 ? (
        <EmptyState icon={<ArrowDownToLine size={22} />} title={t("wh.noEndpoints")} desc={t("wh.noEndpointsSub")} />
      ) : (
        data.endpoints.map((e) => <EndpointCard key={e.id} e={e} team={team} secret={shown?.id === e.id ? shown.secret : ""} onEdit={() => setEdit(e)} onChanged={reload} />)
      )}
      {edit && (
        <EndpointEditor
          team={team}
          e={edit === "new" ? null : edit}
          providers={data.providers}
          onClose={() => setEdit(null)}
          onSaved={(e, secret) => {
            setEdit(null);
            if (secret) setShown({ id: e.id, secret });
            reload();
          }}
        />
      )}
    </div>
  );
}

function TargetChip({ t: tg }: { t: WhTarget }) {
  const channels = useStore((s) => s.channels);
  const agents = useStore((s) => s.agents);
  const [names, setNames] = useState<Record<string, string>>({});
  useEffect(() => {
    if (tg.kind === "workflow") API.workflows().then((r) => setNames(Object.fromEntries(r.workflows.map((w) => [w.id, w.name]))), () => {});
    if (tg.kind === "routine") API.routines().then((r) => setNames(Object.fromEntries(r.routines.map((x) => [x.id, x.name]))), () => {});
  }, [tg.kind]);
  const icon = tg.kind === "channel" ? <Hash size={11} /> : tg.kind === "workflow" ? <WorkflowIcon size={11} /> : <RefreshCw size={11} />;
  const name = tg.kind === "channel" ? channels[tg.id]?.title ?? tg.id : names[tg.id] ?? tg.id;
  return (
    <span className={"whchip k-" + tg.kind} title={t(`wh.kind.${tg.kind}` as Key)}>
      {icon}
      {name}
      {tg.mention && <span className="muted">@{agents[tg.mention]?.name ?? tg.mention}</span>}
    </span>
  );
}

function EndpointCard({ e, team, secret, onEdit, onChanged }: { e: WhEndpoint; team: string; secret: string; onEdit: () => void; onChanged: () => void }) {
  const { local, viaRelay, relayConfigured } = useAddresses(e);
  const [last, setLast] = useState<WhDelivery | null>(null);
  const tick = useStore((s) => s.webhookTick);
  const [log, setLog] = useState(false);
  useEffect(() => {
    API.deliveries(team, e.id, 1).then((r) => setLast(r.deliveries[0] ?? null), () => {});
  }, [team, e.id, tick]);
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
  return (
    <article className={"whcard" + (e.enabled ? "" : " off")}>
      <header className="whch">
        <span className={"whprov p-" + e.provider}>{PROVIDER_NAME[e.provider] ?? e.provider}</span>
        <b>{e.name}</b>
        {e.relay && (
          <span className="pill sm" title={t("wh.relayOnHint")}>
            <RadioTower size={11} />
            {t("wh.viaRelay")}
          </span>
        )}
        <span className="sp" />
        <button className="tg" type="button" role="switch" aria-checked={e.enabled} aria-label={t("wh.enabled")} onClick={() => void toggle()} />
      </header>
      {secret && (
        <div className="whsecret">
          <CircleAlert size={14} />
          <div className="g">
            <b>{t("wh.secretOnce")}</b>
            <CopyField value={secret} />
          </div>
        </div>
      )}
      <CopyField value={viaRelay ?? local} label={viaRelay ? t("wh.addrRelay") : t("wh.addrLocal")} />
      {!viaRelay && e.relay && !relayConfigured && <p className="small whwarn">{t("wh.relayNotConfigured")}</p>}
      <div className="whrow">
        <span className="small muted">{t("wh.events")}</span>
        <span className="small">{e.events?.length ? e.events.join(", ") : t("wh.anyEvent")}</span>
      </div>
      {e.filter && (
        <div className="whrow">
          <span className="small muted">{t("wh.filter")}</span>
          <code className="small">{e.filter}</code>
        </div>
      )}
      <div className="whrow">
        <span className="small muted">{t("wh.targets")}</span>
        <span className="whchips">{e.targets.length ? e.targets.map((tg, i) => <TargetChip key={i} t={tg} />) : <span className="small whwarn">{t("wh.noTargets")}</span>}</span>
      </div>
      <footer className="whcf">
        {last ? (
          <button type="button" className="whlast" onClick={() => setLog(!log)}>
            <StatusPill s={last.status} />
            <span className="small muted">
              {last.event ? last.event + " · " : ""}
              {ago(last.at)}
            </span>
          </button>
        ) : (
          <span className="small muted">{t("wh.noDeliveries")}</span>
        )}
        <span className="sp" />
        <button type="button" className="btn sm ghost" onClick={onEdit}>
          <Pencil size={12} />
          {t("common.edit")}
        </button>
        <button type="button" className="ix dng" aria-label={t("common.delete")} title={t("common.delete")} onClick={() => void remove()}>
          <Trash2 size={13} />
        </button>
      </footer>
      {log && (
        <div className="whcardlog">
          <Log of={e.id} compact />
        </div>
      )}
    </article>
  );
}

const STATUS_TONE: Record<string, string> = {
  accepted: "ok", delivered: "ok", "dry-run": "blue", filtered: "", duplicate: "", cooldown: "amber", limited: "amber", partial: "amber", pending: "blue", rejected: "red", dead: "red",
};
function StatusPill({ s }: { s: string }) {
  const k = `wh.st.${s}` as Key;
  return <span className={"pill sm " + (STATUS_TONE[s] ?? "")}>{t(k) === k ? s : t(k)}</span>;
}

function EndpointEditor({ team, e, providers, onClose, onSaved }: {
  team: string; e: WhEndpoint | null; providers: string[]; onClose: () => void; onSaved: (e: WhEndpoint, secret: string) => void;
}) {
  const [f, setF] = useState<WhEndpointInput>(() =>
    e
      ? { name: e.name, provider: e.provider, events: e.events ?? [], filter: e.filter ?? "", instruction: e.instruction ?? "", targets: e.targets, cooldown: e.cooldown ?? 0, cooldownKey: e.cooldownKey ?? "", rateLimit: e.rateLimit ?? 0, mode: e.mode ?? "sandbox", relay: !!e.relay }
      : { name: "", provider: "github", events: [], filter: "", instruction: "", targets: [], cooldown: 0, cooldownKey: "", rateLimit: 0, mode: "sandbox", relay: false },
  );
  const [secret, setSecret] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const set = <K extends keyof WhEndpointInput>(k: K, v: WhEndpointInput[K]) => (setF((x) => ({ ...x, [k]: v })), setErr(""));
  const providerGenerates = f.provider === "stripe" || f.provider === "sentry" || f.provider === "linear";
  const save = async () => {
    setBusy(true);
    try {
      const r = await API.saveEndpoint(team, e?.id ?? null, { ...f, secret: secret.trim() || undefined });
      onSaved(r.endpoint, r.secret);
      toast(t("wh.saved"));
    } catch (x) {
      setErr(errOf(x));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Layer>
      <div className="scrim" onClick={onClose} />
      <div className="modal wide whdlg" role="dialog" aria-modal="true" aria-labelledby="whEpTitle">
        <div className="mhd">
          <h3 id="whEpTitle">{e ? t("wh.editEndpoint") : t("wh.newEndpoint")}</h3>
          <button className="ix" type="button" aria-label={t("common.close")} onClick={onClose}>
            <X size={14} />
          </button>
        </div>
        <form className="mbd" onSubmit={(x) => (x.preventDefault(), void save())}>
          <div className="whgrid">
            <label className="fl">
              {t("wh.name")}
              <input className="fi sm" value={f.name ?? ""} placeholder={t("wh.namePh")} onChange={(x) => set("name", x.target.value)} />
            </label>
            <label className="fl">
              {t("wh.provider")}
              <span className="ssel">
                <select value={f.provider} onChange={(x) => set("provider", x.target.value)}>
                  {providers.map((p) => (
                    <option key={p} value={p}>
                      {PROVIDER_NAME[p] ?? p}
                    </option>
                  ))}
                </select>
              </span>
            </label>
          </div>
          <label className="fl">
            {t("wh.secret")}
            <input className="fi sm mono" type="password" autoComplete="off" value={secret} onChange={(x) => setSecret(x.target.value)}
              placeholder={e ? t("wh.secretKeep") : providerGenerates ? t("wh.secretPaste", { p: PROVIDER_NAME[f.provider ?? ""] ?? "" }) : t("wh.secretGen")} />
            <span className="small muted">{t("wh.secretHint")}</span>
          </label>
          <label className="fl">
            {t("wh.events")}
            <input className="fi sm mono" value={(f.events ?? []).join(", ")} placeholder={t("wh.eventsPh")} onChange={(x) => set("events", x.target.value.split(",").map((s) => s.trim()).filter(Boolean))} />
            {EVENT_HINTS[f.provider ?? ""] && (
              <span className="whsugg">
                {EVENT_HINTS[f.provider ?? ""].map((ev) => (
                  <button key={ev} type="button" className={"whsug" + ((f.events ?? []).includes(ev) ? " on" : "")}
                    onClick={() => set("events", (f.events ?? []).includes(ev) ? (f.events ?? []).filter((x) => x !== ev) : [...(f.events ?? []), ev])}>
                    {ev}
                  </button>
                ))}
              </span>
            )}
          </label>
          <label className="fl">
            {t("wh.filter")}
            <input className="fi sm mono" value={f.filter ?? ""} placeholder={FILTER_EXAMPLES[f.provider ?? ""] ?? `action == "opened"`} onChange={(x) => set("filter", x.target.value)} />
            <span className="small muted">{t("wh.filterHint")}</span>
          </label>
          <label className="fl">
            {t("wh.instruction")}
            <textarea className="fi" rows={3} value={f.instruction ?? ""} placeholder={t("wh.instructionPh")} onChange={(x) => set("instruction", x.target.value)} />
            <span className="small muted">{t("wh.instructionHint")}</span>
          </label>
          <div className="fl">{t("wh.targets")}</div>
          <TargetsEditor team={team} value={f.targets ?? []} onChange={(v) => set("targets", v)} />
          <div className="whgrid three">
            <label className="fl">
              {t("wh.cooldown")}
              <input className="fi sm" type="number" min={0} value={f.cooldown || ""} placeholder="0" onChange={(x) => set("cooldown", Number(x.target.value) || 0)} />
            </label>
            <label className="fl">
              {t("wh.cooldownKey")}
              <input className="fi sm mono" value={f.cooldownKey ?? ""} placeholder="repository.full_name" onChange={(x) => set("cooldownKey", x.target.value)} />
            </label>
            <label className="fl">
              {t("wh.rateLimit")}
              <input className="fi sm" type="number" min={0} value={f.rateLimit || ""} placeholder="30" onChange={(x) => set("rateLimit", Number(x.target.value) || 0)} />
            </label>
          </div>
          <div className="whgrid top">
            <label className="fl">
              {t("wh.mode")}
              <span className="seg whmode">
                {(["sandbox", "read-only"] as const).map((m) => (
                  <button key={m} type="button" aria-pressed={f.mode === m} onClick={() => set("mode", m)}>
                    {t(`wh.mode.${m}` as Key)}
                  </button>
                ))}
              </span>
            </label>
            <label className="fl whrelay">
              <span className="whrelayh">
                <button className="tg" type="button" role="switch" aria-checked={!!f.relay} aria-label={t("wh.relay")} onClick={() => set("relay", !f.relay)} />
                <span className="whrelayt">{t("wh.relay")}</span>
              </span>
              <span className="small muted">{t("wh.relayHint")}</span>
            </label>
          </div>
          {err && <p className="small errt">{err}</p>}
          <div className="mft">
            <button type="button" className="btn ghost" onClick={onClose}>
              {t("common.cancel")}
            </button>
            <button type="submit" className="btn pri" disabled={busy}>
              {t("common.save")}
            </button>
          </div>
        </form>
      </div>
    </Layer>
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
    <div className="whtargets">
      {value.map((tg, i) => {
        const list: { id: string; name: string }[] =
          tg.kind === "workflow" ? opts.workflows.map((w) => ({ id: w.id, name: w.name })) : tg.kind === "routine" ? opts.routines.map((r) => ({ id: r.id, name: r.name })) : opts.channels.map((c) => ({ id: c.id, name: "#" + c.title }));
        const ch = tg.kind === "channel" ? opts.channels.find((c) => c.id === tg.id) : undefined;
        return (
          <div key={i} className="whtg">
            <span className="seg whkinds">
              {(["channel", "routine", "workflow"] as const).map((k) => (
                <button key={k} type="button" aria-pressed={tg.kind === k} onClick={() => setAt(i, { kind: k, id: "" })}>
                  {t(`wh.kind.${k}` as Key)}
                </button>
              ))}
            </span>
            <span className="ssel">
              <select value={tg.id} onChange={(x) => setAt(i, { ...tg, id: x.target.value, mention: undefined })}>
                <option value="">{t("wh.pick")}</option>
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
              <X size={13} />
            </button>
          </div>
        );
      })}
      <button type="button" className="linkbtn small" onClick={() => onChange([...value, { kind: "channel", id: "" }])}>
        + {t("wh.addTarget")}
      </button>
      {value.length === 0 && <p className="small muted">{t("wh.targetsHint")}</p>}
    </div>
  );
}

// ---------------------------------------------------------------- 出站

function Outbound() {
  const { team, data, reload } = useWebhooks();
  const [edit, setEdit] = useState<WhSubscription | "new" | null>(null);
  const [shown, setShown] = useState<{ id: string; secret: string } | null>(null);
  if (!data) return <Skeleton kind="rows" />;
  return (
    <div className="wh">
      <div className="whbar">
        <p className="small muted">{t("wh.outIntro")}</p>
        <button type="button" className="btn pri sm" onClick={() => setEdit("new")}>
          <Plus size={13} />
          {t("wh.newSub")}
        </button>
      </div>
      {data.subscriptions.length === 0 ? (
        <EmptyState icon={<ArrowUpFromLine size={22} />} title={t("wh.noSubs")} desc={t("wh.noSubsSub")} />
      ) : (
        data.subscriptions.map((x) => <SubCard key={x.id} x={x} team={team} secret={shown?.id === x.id ? shown.secret : ""} onEdit={() => setEdit(x)} onChanged={reload} />)
      )}
      <OutSettings />
      {edit && (
        <SubEditor
          team={team}
          x={edit === "new" ? null : edit}
          events={data.events}
          onClose={() => setEdit(null)}
          onSaved={(x, secret) => {
            setEdit(null);
            if (secret) setShown({ id: x.id, secret });
            reload();
          }}
        />
      )}
    </div>
  );
}

function SubCard({ x, team, secret, onEdit, onChanged }: { x: WhSubscription; team: string; secret: string; onEdit: () => void; onChanged: () => void }) {
  const tick = useStore((s) => s.webhookTick);
  const [recent, setRecent] = useState<WhDelivery[]>([]);
  const [log, setLog] = useState(false);
  useEffect(() => {
    API.deliveries(team, x.id, 20).then((r) => setRecent(r.deliveries), () => {});
  }, [team, x.id, tick]);
  const host = (() => {
    try {
      return new URL(x.url).host;
    } catch {
      return x.url;
    }
  })();
  const ok = recent.filter((d) => d.status === "delivered").length;
  const dead = recent.filter((d) => d.status === "dead").length;
  const pending = recent.filter((d) => d.status === "pending").length;
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
    <article className={"whcard" + (x.enabled ? "" : " off")}>
      <header className="whch">
        <Globe size={14} className="muted" />
        <b>{x.name}</b>
        <span className="small muted mono whhost">{host}</span>
        <span className="sp" />
        <button className="tg" type="button" role="switch" aria-checked={x.enabled} aria-label={t("wh.enabled")} onClick={() => void act(() => API.saveSubscription(team, x.id, { enabled: !x.enabled }))} />
      </header>
      {secret && (
        <div className="whsecret">
          <CircleAlert size={14} />
          <div className="g">
            <b>{t("wh.signSecretOnce")}</b>
            <CopyField value={secret} />
          </div>
        </div>
      )}
      <div className="whrow">
        <span className="small muted">{t("wh.events")}</span>
        <span className="whchips">
          {x.events.map((e) => (
            <span key={e} className="whchip">
              {t(`wh.ev.${e}` as Key) === `wh.ev.${e}` ? e : t(`wh.ev.${e}` as Key)}
            </span>
          ))}
        </span>
      </div>
      {x.filter && (
        <div className="whrow">
          <span className="small muted">{t("wh.filter")}</span>
          <code className="small">{x.filter}</code>
        </div>
      )}
      <footer className="whcf">
        <button type="button" className="whlast" onClick={() => setLog(!log)}>
          {recent.length === 0 ? (
            <span className="small muted">{t("wh.noDeliveries")}</span>
          ) : (
            <span className="small">
              <span className="ok">{t("wh.nDelivered", { n: ok })}</span>
              {pending > 0 && <span className="blue"> · {t("wh.nPending", { n: pending })}</span>}
              {dead > 0 && <span className="errt"> · {t("wh.nDead", { n: dead })}</span>}
            </span>
          )}
        </button>
        <span className="sp" />
        <button type="button" className="btn sm ghost" onClick={() => void act(() => API.testSubscription(team, x.id), t("wh.testQueued"))}>
          <Send size={12} />
          {t("wh.test")}
        </button>
        <button type="button" className="btn sm ghost" onClick={onEdit}>
          <Pencil size={12} />
          {t("common.edit")}
        </button>
        <button type="button" className="ix dng" aria-label={t("common.delete")} title={t("common.delete")}
          onClick={async () => (await confirmDialog({ text: t("wh.deleteSubQ", { name: x.name }), ok: t("common.delete"), danger: true })) && act(() => API.deleteSubscription(team, x.id))}>
          <Trash2 size={13} />
        </button>
      </footer>
      {log && (
        <div className="whcardlog">
          <Log of={x.id} compact />
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
    <Layer>
      <div className="scrim" onClick={onClose} />
      <div className="modal wide whdlg" role="dialog" aria-modal="true" aria-labelledby="whSubTitle">
        <div className="mhd">
          <h3 id="whSubTitle">{x ? t("wh.editSub") : t("wh.newSub")}</h3>
          <button className="ix" type="button" aria-label={t("common.close")} onClick={onClose}>
            <X size={14} />
          </button>
        </div>
        <form className="mbd" onSubmit={(e) => (e.preventDefault(), void save())}>
          <div className="whgrid">
            <label className="fl">
              {t("wh.name")}
              <input className="fi sm" value={name} placeholder={t("wh.subNamePh")} onChange={(e) => setName(e.target.value)} />
            </label>
            <label className="fl">
              URL
              <input className="fi sm mono" value={url} placeholder="https://example.com/hooks/crewbase" onChange={(e) => (setUrl(e.target.value), setErr(""))} />
            </label>
          </div>
          <div className="fl">{t("wh.events")}</div>
          <div className="whevs">
            {events.map((e) => (
              <label key={e} className={"whev" + (evs.includes(e) ? " on" : "")}>
                <input type="checkbox" checked={evs.includes(e)} onChange={() => setEvs(evs.includes(e) ? evs.filter((y) => y !== e) : [...evs, e])} />
                <span className="g">
                  <b>{t(`wh.ev.${e}` as Key)}</b>
                  <code className="small muted">{e}</code>
                </span>
              </label>
            ))}
          </div>
          <label className="fl">
            {t("wh.filter")}
            <input className="fi sm mono" value={filter} placeholder={`status != "quiet"`} onChange={(e) => setFilter(e.target.value)} />
            <span className="small muted">{t("wh.outFilterHint")}</span>
          </label>
          <label className="fl">
            {t("wh.signSecret")}
            <input className="fi sm mono" type="password" autoComplete="off" value={secret} onChange={(e) => setSecret(e.target.value)} placeholder={x ? t("wh.secretKeep") : t("wh.secretGen")} />
            <span className="small muted">{t("wh.signHint")}</span>
          </label>
          {err && <p className="small errt">{err}</p>}
          <div className="mft">
            <button type="button" className="btn ghost" onClick={onClose}>
              {t("common.cancel")}
            </button>
            <button type="submit" className="btn pri" disabled={busy || !url.trim() || evs.length === 0}>
              {t("common.save")}
            </button>
          </div>
        </form>
      </div>
    </Layer>
  );
}

function OutSettings() {
  const [s, setS] = useState<WhSettings | null>(null);
  const [allow, setAllow] = useState("");
  const [busy, setBusy] = useState(false);
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
    <section className="whset">
      <h4>{t("wh.outSettings")}</h4>
      <label className="fl">
        {t("wh.allowPrivate")}
        <input className="fi sm mono" value={allow} placeholder="10.0.0.0/8, 192.168.1.20" onChange={(e) => setAllow(e.target.value)} />
        <span className="small muted">{t("wh.allowPrivateHint")}</span>
      </label>
      <div className="whsetr">
        <label className="fl">
          {t("wh.maxAttempts")}
          <input className="fi sm" type="number" min={1} max={20} value={s.maxAttempts} onChange={(e) => setS({ ...s, maxAttempts: Number(e.target.value) })} />
        </label>
        <span className="sp" />
        <button type="button" className="btn sm" disabled={busy} onClick={() => void save()}>
          {t("common.save")}
        </button>
      </div>
    </section>
  );
}

// ---------------------------------------------------------------- 投递记录

function Log({ of, compact }: { of: string; compact?: boolean }) {
  const team = useTeam();
  const tick = useStore((s) => s.webhookTick);
  const [list, setList] = useState<WhDelivery[] | null>(null);
  const [dir, setDir] = useState<"" | "in" | "out">("");
  const [open, setOpen] = useState<string | null>(null);
  useEffect(() => {
    let live = true;
    API.deliveries(team, of || undefined, compact ? 8 : 200).then(
      (r) => live && setList(r.deliveries),
      () => live && setList([]),
    );
    return () => void (live = false);
  }, [team, of, tick, compact]);
  const shown = useMemo(() => (list ?? []).filter((d) => !dir || d.dir === dir), [list, dir]);
  if (!list) return <Skeleton kind="rows" />;
  return (
    <div className={"whlog" + (compact ? " compact" : "")}>
      {!compact && (
        <div className="whbar">
          <span className="seg">
            {(["", "in", "out"] as const).map((d) => (
              <button key={d || "all"} type="button" aria-pressed={dir === d} onClick={() => setDir(d)}>
                {t(`wh.dir.${d || "all"}` as Key)}
              </button>
            ))}
          </span>
        </div>
      )}
      {shown.length === 0 ? (
        <p className="small muted whnone">{t("wh.noDeliveries")}</p>
      ) : (
        <ul className="whlist">
          {shown.map((d) => (
            <li key={d.id}>
              <button type="button" className={"whli" + (open === d.id ? " on" : "")} onClick={() => setOpen(open === d.id ? null : d.id)}>
                {d.dir === "in" ? <ArrowDownToLine size={13} className="muted" /> : <ArrowUpFromLine size={13} className="muted" />}
                <span className="whlin">{d.name}</span>
                <span className="mono small whlie">{d.event || "—"}</span>
                <span className="sp" />
                {d.code ? <span className="small muted">HTTP {d.code}</span> : null}
                {d.via === "relay" && <RadioTower size={12} className="muted" />}
                <StatusPill s={d.status} />
                <span className="small muted whlit">{ago(d.at)}</span>
              </button>
              {open === d.id && <DeliveryDetail id={d.id} />}
            </li>
          ))}
        </ul>
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
  useEffect(() => {
    API.delivery(team, id).then(setD, (e) => toast(errOf(e)));
  }, [team, id, tick]);
  if (!d) return <div className="whdet"><Skeleton kind="code" /></div>;
  const body = d.body !== undefined ? JSON.stringify(d.body, null, 2) : d.bodyText ?? "";
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
      {d.error && <p className="small whwarn">{d.error}</p>}
      {d.dir === "out" && (
        <p className="small muted">
          {t("wh.attempts", { n: d.attempts ?? 0 })}
          {d.status === "pending" && d.next && <> · {t("wh.nextAt", { at: new Date(d.next).toLocaleTimeString() })}</>}
        </p>
      )}
      {!!d.targets?.length && (
        <div className="whtres">
          {d.targets.map((tr, i) => (
            <div key={i} className={"whtr" + (tr.ok ? " ok" : " bad")}>
              {tr.ok ? <Check size={12} /> : <X size={12} />}
              <TargetChip t={tr} />
              <span className="small muted">{tr.ok ? (tr.kind === "channel" && tr.detail ? t("wh.wentTo", { name: agentName(tr.detail) }) : "") : tr.error}</span>
            </div>
          ))}
        </div>
      )}
      {d.headers && Object.keys(d.headers).length > 0 && (
        <details className="whfold">
          <summary className="small">{t("wh.headers")}</summary>
          <pre className="whpre">{Object.entries(d.headers).map(([k, v]) => `${k}: ${v}`).join("\n")}</pre>
        </details>
      )}
      {body && (
        <details className="whfold" open={d.dir === "out"}>
          <summary className="small">{t("wh.body")}</summary>
          <pre className="whpre">{body}</pre>
        </details>
      )}
      {d.prompt && (
        <details className="whfold">
          <summary className="small">{t("wh.prompt")}</summary>
          <pre className="whpre">{d.prompt}</pre>
        </details>
      )}
      <div className="whdeta">
        {d.dir === "in" && d.status !== "rejected" && (
          <>
            <button type="button" className="btn sm ghost" disabled={busy} onClick={() => void replay(true)}>
              <Play size={12} />
              {t("wh.dryRun")}
            </button>
            <button type="button" className="btn sm" disabled={busy} onClick={() => void replay(false)}>
              <RotateCcw size={12} />
              {t("wh.replay")}
            </button>
          </>
        )}
        {d.dir === "out" && d.status === "dead" && (
          <button type="button" className="btn sm" disabled={busy} onClick={() => void replay(false)}>
            <RotateCcw size={12} />
            {t("wh.resend")}
          </button>
        )}
      </div>
      {dry && (
        <div className="whdry">
          <div>
            <b className="small">{t("wh.dryResult")}</b>
            <StatusPill s={dry.status} />
            {dry.error && <span className="small muted">{dry.error}</span>}
          </div>
          {dry.targets?.map((tr, i) => (
            <div key={i} className="whtr ok">
              <GitBranch size={12} />
              <TargetChip t={tr} />
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
