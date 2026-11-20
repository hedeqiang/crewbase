import { useEffect, useMemo, useState } from "react";
import { ArrowDown, ArrowUp, Plus, RefreshCw, X } from "lucide-react";
import { API, parseRef, refString, type ModelKind, type TaskRoute } from "../../api";
import { t, type Key } from "../../i18n";
import { allModels, findProvider, modelLabel, modelMeta, modelOff, providerName, until } from "../../format";
import { refreshModels, toast, useStore } from "../../store";
import { ProviderMark } from "../ProviderMark";
import { useSettingsData } from "../Settings";

/** 模型 › 路由与切换. */
export function Routing() {
  const models = useStore((s) => s.models);
  const all = useMemo(() => allModels(models), [models]);
  return (
    <div className="vb">
      <DefaultOrder />
      <Background />
      <Exhausted />
      <Routes />
      {models?.switches !== undefined && (
        <p className="small" style={{ marginTop: 16 }}>
          {t("set.switches", { n: models.switches })}
        </p>
      )}
      {all.length === 0 && <p className="small">{t("set.noProviders")}</p>}
    </div>
  );
}

function ModelName({ ref_ }: { ref_: string }) {
  const models = useStore((s) => s.models);
  const r = parseRef(ref_);
  if (!r) return <span className="mono">{ref_}</span>;
  return (
    <>
      <ProviderMark provider={r.provider} model={r.model} size={16} />
      <span className="rtn">{modelLabel(models, r)}</span>
    </>
  );
}

/** Image / video tasks pick image / video models; every other task a chat model. */
const taskKind = (task: string): ModelKind => (task === "image" || task === "video" ? task : "chat");

function ModelSelect({ value, onChange, placeholder, kind = "chat" }: { value: string; onChange: (v: string) => void; placeholder?: string; kind?: ModelKind }) {
  const models = useStore((s) => s.models);
  const all = useMemo(() => allModels(models, kind), [models, kind]);
  return (
    <span className="ssel">
      <select value={value} onChange={(e) => onChange(e.target.value)}>
        {placeholder !== undefined && <option value="">{placeholder}</option>}
        {!all.some((m) => refString(m.ref) === value) && value && <option value={value}>{value}</option>}
        {all.map((m) => (
          <option key={refString(m.ref)} value={refString(m.ref)}>
            {m.info.name || m.info.id} · {m.provider.id}
            {modelMeta(m.info) ? ` · ${modelMeta(m.info)}` : ""}
          </option>
        ))}
      </select>
    </span>
  );
}

/** The jobs Crewbase runs in the background with a model, and what each
 *  does without one. */
const BACKGROUND: { task: string; what: Key; without: Key }[] = [
  { task: "title", what: "bg.title", without: "bg.titleOff" },
  { task: "summarize", what: "bg.summarize", without: "bg.summarizeOff" },
  { task: "brief", what: "bg.brief", without: "bg.briefOff" },
];

const isBackground = (task: string) => BACKGROUND.some((b) => b.task === task);

/** 后台任务用的模型: titles, summaries, briefs — one model each, said plainly
 *  (the same rules as 按任务分流 below, the first one of each task). */
function Background() {
  const [data, setData] = useState<{ routes: TaskRoute[]; inUse: Record<string, string> } | null>(null);
  const load = () => API.routes().then((r) => setData({ routes: r.routes ?? [], inUse: r.inUse ?? {} }), () => setData({ routes: [], inUse: {} }));
  useEffect(() => {
    void load();
  }, []);
  if (!data) return null;
  const set = async (task: string, model: string) => {
    // the chosen model becomes the task's first rule; "" removes its rules
    const rest = data.routes.filter((r) => r.task !== task);
    const mine = data.routes.filter((r) => r.task === task && r.model !== model);
    const next = model ? [{ task, model }, ...mine, ...rest] : rest;
    try {
      await API.setRoutes(next);
      await load();
      toast(t("panel.saved"));
      window.dispatchEvent(new Event("crewbase:routes"));
    } catch (e: any) {
      toast(e?.message ?? String(e));
    }
  };
  return (
    <>
      <div className="vsec">{t("bg.title_")}</div>
      <p className="small" style={{ margin: "0 0 8px" }}>
        {t("bg.desc")}
      </p>
      <div className="sbox">
        {BACKGROUND.map((b) => {
          const using = data.inUse[b.task] ?? "";
          return (
            <div className="srow bgrow" key={b.task}>
              <div className="bgw">
                <b>{t(`rt.task.${b.task}` as Key)}</b>
                <span className="small muted">{t(b.what)}</span>
                {!using && <span className="small amber-t">{t(b.without)}</span>}
              </div>
              <ModelSelect value={using} onChange={(v) => void set(b.task, v)} placeholder={t("bg.none")} />
            </div>
          );
        })}
      </div>
    </>
  );
}

function DefaultOrder() {
  const models = useStore((s) => s.models);
  const [st, patch] = useSettingsData();
  const [adding, setAdding] = useState("");
  const order = st?.defaultModels ?? [];
  const save = (next: string[]) => void patch({ defaultModels: next });
  const move = (i: number, j: number) => {
    if (j < 0 || j >= order.length) return;
    const next = [...order];
    const [x] = next.splice(i, 1);
    next.splice(j, 0, x);
    save(next);
  };
  return (
    <>
      <div className="vsec">{t("rt.defaultOrder")}</div>
      <p className="small" style={{ margin: "0 0 8px" }}>
        {t("rt.defaultOrderDesc")}
      </p>
      <div className="route wide">
        {!st && <div className="small">{t("common.loading")}</div>}
        {order.map((m, i) => {
          const r = parseRef(m);
          const off = !!r && modelOff(models, r);
          return (
          <div className={"rt" + (off ? " off" : "")} key={m + i}>
            <span className="n">{i + 1}</span>
            <ModelName ref_={m} />
            {off ? (
              <span className="pill" title={t("set.modelOffHint")}>{t("set.modelOffSkipped")}</span>
            ) : (
              <span className="pill">{i === 0 ? t("comp.modelPrimary") : t("comp.modelBackup")}</span>
            )}
            <button className="ix" type="button" aria-label={t("panel.up")} disabled={i === 0} onClick={() => move(i, i - 1)}>
              <ArrowUp size={13} />
            </button>
            <button className="ix" type="button" aria-label={t("panel.down")} disabled={i === order.length - 1} onClick={() => move(i, i + 1)}>
              <ArrowDown size={13} />
            </button>
            <button className="ix" type="button" aria-label={t("panel.remove")} disabled={order.length === 1} onClick={() => save(order.filter((_, j) => j !== i))}>
              <X size={13} />
            </button>
          </div>
          );
        })}
        {st && (
          <div className="addrow">
            <ModelSelect value={adding} onChange={setAdding} placeholder={t("panel.addModel")} />
            <button
              className="btn"
              type="button"
              disabled={!adding || order.includes(adding)}
              onClick={() => {
                save([...order, adding]);
                setAdding("");
              }}
            >
              <Plus size={13} />
              {t("rt.add")}
            </button>
          </div>
        )}
      </div>
    </>
  );
}

function Exhausted() {
  const models = useStore((s) => s.models);
  const ex = models?.exhausted ?? [];
  const [busy, setBusy] = useState<string | null>(null);
  const reset = async (m: string) => {
    setBusy(m);
    try {
      await API.resetModel(m);
      await refreshModels();
      toast(t("rt.resetDone", { model: m }));
    } catch (e: any) {
      toast(e?.message ?? String(e));
    } finally {
      setBusy(null);
    }
  };
  return (
    <>
      <div className="vsec">{t("rt.exhausted")}</div>
      {ex.length === 0 ? (
        <p className="small" style={{ margin: 0 }}>
          {t("rt.exhaustedNone")}
        </p>
      ) : (
        <div className="sbox">
          {ex.map((x) => (
            <div className="srow" key={x.model}>
              <div className="g">
                <b className="mono">{x.model}</b>
                <span>
                  {t((`chat.reason.${x.reason}` as Key) in REASONS ? (`chat.reason.${x.reason}` as Key) : "chat.reason.other")} ·{" "}
                  {t("rt.until", { time: until(x.until) })}
                </span>
              </div>
              <div className="c">
                <button className="btn" type="button" disabled={busy === x.model} onClick={() => reset(x.model)}>
                  <RefreshCw size={13} />
                  {t("rt.recheck")}
                </button>
              </div>
            </div>
          ))}
        </div>
      )}
    </>
  );
}
const REASONS: Record<string, true> = {
  "chat.reason.quota_exhausted": true,
  "chat.reason.rate_limited": true,
  "chat.reason.unavailable": true,
  "chat.reason.model_unavailable": true,
};

/** The model part of "provider/model" ("magpie/deepseek/deepseek-flash" and
 *  "deepseek/deepseek-flash" are both DeepSeek Flash). */
const modelPart = (ref: string) => {
  const i = ref.indexOf("/");
  const p = ref.slice(0, i);
  return p === "magpie" || p === "openrouter" ? ref.slice(i + 1) : ref;
};
const sameModel = (a: string, b: string) => modelPart(a) === modelPart(b);
const viaOf = (ref: string) => {
  const p = ref.slice(0, ref.indexOf("/"));
  return p === "magpie" || p === "openrouter" ? t("rt.via", { name: providerName(p) }) : t("rt.direct");
};

function Routes() {
  const models = useStore((s) => s.models);
  // a rule whose provider is not connected is skipped by the router: say so
  const offline = (ref: string) => {
    if (!ref || !models) return false;
    const i = ref.indexOf("/");
    const p = findProvider(models, ref.slice(0, i));
    return !p || (p.models.length > 0 && !p.models.some((m) => m.id === ref.slice(i + 1) && m.enabled !== false));
  };
  const [routes, setRoutes] = useState<TaskRoute[] | null>(null);
  const [tasks, setTasks] = useState<string[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [dirty, setDirty] = useState(false);
  useEffect(() => {
    const load = () =>
      API.routes().then(
        (r) => {
          setRoutes(r.routes ?? []);
          setTasks(r.tasks ?? []);
          setDirty(false);
        },
        (e) => setErr(e?.message ?? String(e)),
      );
    load();
    window.addEventListener("crewbase:routes", load);
    return () => window.removeEventListener("crewbase:routes", load);
  }, []);
  const edit = (next: TaskRoute[]) => {
    setRoutes(next);
    setDirty(true);
  };
  const save = async () => {
    if (!routes) return;
    try {
      const r = await API.setRoutes(routes.filter((x) => x.task && x.model));
      setRoutes(r.routes ?? []);
      setDirty(false);
      toast(t("panel.saved"));
    } catch (e: any) {
      toast(e?.message ?? String(e));
    }
  };
  // The same model reached another way (directly vs through a gateway) is
  // a spare route: while it is not connected and the other one is, it
  // stays in the rules but is not shown (removing the row removes both).
  const hidden = (i: number) => {
    const r = routes?.[i];
    if (!routes || !r?.model || !offline(r.model)) return false;
    return routes.some((x, j) => j !== i && x.task === r.task && sameModel(x.model, r.model) && !offline(x.model));
  };
  const taskLabel = (k: string) => {
    const key = `rt.task.${k}` as Key;
    return t(key) === key ? k : t(key);
  };
  return (
    <>
      <div className="vsec">{t("rt.routes")}</div>
      <p className="small" style={{ margin: "0 0 8px" }}>
        {t("rt.routesDesc")}
      </p>
      {err && <p className="small errt">{err}</p>}
      {routes && (
        <div className="sbox">
          {routes.every((r) => isBackground(r.task)) && (
            <div className="srow">
              <span className="small">{t("rt.routesNone")}</span>
            </div>
          )}
          {routes.map((r, i) => (hidden(i) || isBackground(r.task) ? null : (
            <div className="srow routerow" key={i}>
              <span className="ssel">
                <select
                  value={r.task}
                  onChange={(e) => {
                    const task = e.target.value;
                    // a model of the wrong kind cannot stay on the row
                    edit(routes.map((x, j) => (j === i ? { task, model: taskKind(task) === taskKind(x.task) ? x.model : "" } : x)));
                  }}
                >
                  {[...new Set([...tasks.filter((k) => !isBackground(k)), r.task])].filter(Boolean).map((k) => (
                    <option key={k} value={k}>
                      {taskLabel(k)}
                    </option>
                  ))}
                </select>
              </span>
              <span className="arrow">→</span>
              <ModelSelect
                kind={taskKind(r.task)}
                value={r.model}
                onChange={(v) => edit(routes.map((x, j) => (j === i ? { ...x, model: v } : x)))}
                placeholder={taskKind(r.task) === "chat" ? t("rt.pickModel") : t(taskKind(r.task) === "image" ? "rt.pickImageModel" : "rt.pickVideoModel")}
              />
              {/* the same model reached two ways (directly, through a gateway): say which this row is */}
              {r.model && routes.some((x, j) => j !== i && !hidden(j) && x.task === r.task && sameModel(x.model, r.model)) && (
                <span className="pill sm">{viaOf(r.model)}</span>
              )}
              {offline(r.model) && (
                <span className="pill sm amber" title={t("rt.offlineTip")}>
                  {t("rt.offline")}
                </span>
              )}
              <button className="ix" type="button" aria-label={t("panel.remove")} onClick={() => edit(routes.filter((_, j) => j !== i && !(hidden(j) && routes[j].task === r.task && sameModel(routes[j].model, r.model))))}>
                <X size={13} />
              </button>
            </div>
          )))}
        </div>
      )}
      {routes && (
        <div className="acts">
          <button
            className="btn"
            type="button"
            disabled={tasks.length === 0}
            onClick={() => {
              const free = tasks.filter((k) => !isBackground(k));
              edit([...routes, { task: free.find((k) => !routes.some((r) => r.task === k)) ?? free[0], model: "" }]);
            }}
          >
            <Plus size={13} />
            {t("rt.addRoute")}
          </button>
          <button className="btn pri" type="button" disabled={!dirty} onClick={save}>
            {t("common.save")}
          </button>
        </div>
      )}
    </>
  );
}
