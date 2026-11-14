import { useEffect, useMemo, useState } from "react";
import { ArrowDown, ArrowUp, Plus, RefreshCw, X } from "lucide-react";
import { API, parseRef, refString, type TaskRoute } from "../../api";
import { t, type Key } from "../../i18n";
import { allModels, modelLabel, until } from "../../format";
import { refreshModels, toast, useStore } from "../../store";
import { ProviderMark } from "../ProviderMark";
import { useSettingsData } from "../Settings";

/** 模型与订阅 › 路由与切换. */
export function Routing() {
  const models = useStore((s) => s.models);
  const all = useMemo(() => allModels(models), [models]);
  return (
    <div className="vb">
      <DefaultOrder />
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

function ModelSelect({ value, onChange, placeholder }: { value: string; onChange: (v: string) => void; placeholder?: string }) {
  const models = useStore((s) => s.models);
  const all = useMemo(() => allModels(models), [models]);
  return (
    <span className="ssel">
      <select value={value} onChange={(e) => onChange(e.target.value)}>
        {placeholder !== undefined && <option value="">{placeholder}</option>}
        {!all.some((m) => refString(m.ref) === value) && value && <option value={value}>{value}</option>}
        {all.map((m) => (
          <option key={refString(m.ref)} value={refString(m.ref)}>
            {m.info.name || m.info.id} · {m.provider.id}
          </option>
        ))}
      </select>
    </span>
  );
}

function DefaultOrder() {
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
        {order.map((m, i) => (
          <div className="rt" key={m + i}>
            <span className="n">{i + 1}</span>
            <ModelName ref_={m} />
            <span className="pill">{i === 0 ? t("comp.modelPrimary") : t("comp.modelBackup")}</span>
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
        ))}
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
const REASONS: Record<string, true> = { "chat.reason.quota_exhausted": true, "chat.reason.rate_limited": true, "chat.reason.unavailable": true };

function Routes() {
  const [routes, setRoutes] = useState<TaskRoute[] | null>(null);
  const [tasks, setTasks] = useState<string[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [dirty, setDirty] = useState(false);
  useEffect(() => {
    API.routes().then(
      (r) => {
        setRoutes(r.routes ?? []);
        setTasks(r.tasks ?? []);
      },
      (e) => setErr(e?.message ?? String(e)),
    );
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
          {routes.length === 0 && (
            <div className="srow">
              <span className="small">{t("rt.routesNone")}</span>
            </div>
          )}
          {routes.map((r, i) => (
            <div className="srow routerow" key={i}>
              <span className="ssel">
                <select value={r.task} onChange={(e) => edit(routes.map((x, j) => (j === i ? { ...x, task: e.target.value } : x)))}>
                  {[...new Set([...tasks, r.task])].filter(Boolean).map((k) => (
                    <option key={k} value={k}>
                      {taskLabel(k)}
                    </option>
                  ))}
                </select>
              </span>
              <span className="arrow">→</span>
              <ModelSelect value={r.model} onChange={(v) => edit(routes.map((x, j) => (j === i ? { ...x, model: v } : x)))} placeholder={t("rt.pickModel")} />
              <button className="ix" type="button" aria-label={t("panel.remove")} onClick={() => edit(routes.filter((_, j) => j !== i))}>
                <X size={13} />
              </button>
            </div>
          ))}
        </div>
      )}
      {routes && (
        <div className="acts">
          <button
            className="btn"
            type="button"
            disabled={tasks.length === 0}
            onClick={() => edit([...routes, { task: tasks.find((k) => !routes.some((r) => r.task === k)) ?? tasks[0], model: "" }])}
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
