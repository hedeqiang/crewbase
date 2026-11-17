import { Loader2, Pencil, Plus, Power, RefreshCw, Trash2, X } from "lucide-react";
import { useState } from "react";
import { API, type AgentView, type Subscription } from "../../api";
import { t } from "../../i18n";
import { refreshModels, toast } from "../../store";
import { confirmDialog } from "../Confirm";

/** Splits a command line into argv: whitespace separates, '…' and "…"
 *  quote, a backslash escapes the next character. */
export function splitCommand(s: string): string[] {
  const out: string[] = [];
  let cur = "";
  let has = false;
  let q: string | null = null;
  for (let i = 0; i < s.length; i++) {
    const c = s[i];
    if (q) {
      if (c === q) q = null;
      else if (c === "\\" && q === '"' && i + 1 < s.length) cur += s[++i];
      else cur += c;
    } else if (c === "'" || c === '"') {
      q = c;
      has = true;
    } else if (c === "\\" && i + 1 < s.length) {
      cur += s[++i];
      has = true;
    } else if (/\s/.test(c)) {
      if (has) out.push(cur);
      cur = "";
      has = false;
    } else {
      cur += c;
      has = true;
    }
  }
  if (has) out.push(cur);
  return out;
}

/** argv as one line (arguments with spaces or quotes are quoted). */
export function joinCommand(argv: string[] | null | undefined): string {
  return (argv ?? []).map((a) => (a === "" || /[\s'"\\]/.test(a) ? `'${a.replace(/'/g, `'"'"'`)}'` : a)).join(" ");
}

/** What the subscription's state line says. */
export function subState(s: Subscription): string {
  if (!s.enabled) return t("sub.disabledState");
  if (s.reason === "not_installed") return t("sub.notInstalled", { cli: s.cli });
  if (s.reason === "no_entrypoint") return t("sub.noEntry");
  return "";
}

/** The facts and actions of a subscription CLI in the provider detail
 *  pane: 刷新模型 / 编辑 / 停用·启用 / 删除 (custom only). */
export function SubscriptionPanel({ sub, users, onChanged }: { sub: Subscription; users: AgentView[]; onChanged: () => void }) {
  const [busy, setBusy] = useState<"" | "refresh" | "toggle" | "delete">("");
  const [editing, setEditing] = useState(false);
  const fail = (e: any) => toast(t("ap.failed", { error: e?.message ?? String(e) }));
  const done = async () => {
    onChanged();
    await refreshModels();
  };
  const refresh = async () => {
    setBusy("refresh");
    try {
      const r = await API.refreshSubscription(sub.id);
      if (r.error) toast(t("ap.failed", { error: r.error }));
      else toast(t("sub.refreshed", { name: r.name, n: r.models }));
      await done();
    } catch (e) {
      fail(e);
    } finally {
      setBusy("");
    }
  };
  const toggle = async () => {
    if (sub.enabled) {
      const text = users.length ? t("sub.disableQUsers", { name: sub.name, n: users.length }) : t("sub.disableQ", { name: sub.name });
      if (!(await confirmDialog({ text, ok: t("sub.disable"), danger: true }))) return;
    }
    setBusy("toggle");
    try {
      await API.updateSubscription(sub.id, { enabled: !sub.enabled });
      toast(t(sub.enabled ? "sub.disabledToast" : "sub.enabledToast", { name: sub.name }));
      await done();
    } catch (e) {
      fail(e);
    } finally {
      setBusy("");
    }
  };
  const remove = async () => {
    const text = users.length ? t("sub.deleteQUsers", { name: sub.name, n: users.length }) : t("sub.deleteQ", { name: sub.name });
    if (!(await confirmDialog(text))) return;
    setBusy("delete");
    try {
      await API.removeSubscription(sub.id);
      toast(t("sub.deletedToast", { name: sub.name }));
      await done();
    } catch (e) {
      fail(e);
    } finally {
      setBusy("");
    }
  };
  const cmd = joinCommand(sub.acpCommand);
  const state = subState(sub);
  return (
    <>
      <div className="subact">
        <button className="btn sm" type="button" disabled={!!busy || !sub.enabled} onClick={() => void refresh()} title={t("sub.refreshTitle")}>
          {busy === "refresh" ? <Loader2 size={13} className="spin" /> : <RefreshCw size={13} />}
          {busy === "refresh" ? t("sub.refreshing") : t("sub.refresh")}
        </button>
        <button className="btn sm" type="button" disabled={!!busy} onClick={() => setEditing(true)}>
          <Pencil size={13} />
          {t("ap.editBtn")}
        </button>
        <button className={"btn sm" + (sub.enabled ? "" : " pri")} type="button" disabled={!!busy} onClick={() => void toggle()}>
          {busy === "toggle" ? <Loader2 size={13} className="spin" /> : <Power size={13} />}
          {sub.enabled ? t("sub.disable") : t("sub.enable")}
        </button>
        {sub.removable && (
          <button className="btn dng sm" type="button" disabled={!!busy} onClick={() => void remove()}>
            {busy === "delete" ? <Loader2 size={13} className="spin" /> : <Trash2 size={13} />}
            {t("sub.delete")}
          </button>
        )}
      </div>
      {!sub.enabled && <div className="confirmbar subnote">{t("sub.disabledNote")}</div>}
      <div className="box subfacts">
        <div>
          <span>{t("sub.kind")}</span>
          <b>{sub.kind === "custom" ? t("sub.kindCustom") : t("sub.kindBuiltin")}</b>
        </div>
        <div>
          <span>{t("sub.cli")}</span>
          <b className="mono">
            {sub.installed ? sub.path : t("sub.notInstalled", { cli: sub.cli })}
            {sub.version ? ` · ${sub.version}` : ""}
          </b>
        </div>
        <div>
          <span>{t("sub.command")}</span>
          <b className="mono">{cmd || t("sub.noEntry")}</b>
          {sub.command.length === 0 && cmd && <i className="small">{t("sub.auto")}</i>}
        </div>
        {sub.env.length > 0 && (
          <div>
            <span>{t("sub.env")}</span>
            <b className="mono">{sub.env.map((e) => e.key).join(" · ")}</b>
          </div>
        )}
        {sub.loggedIn === "no" && sub.installed && sub.loginCommand && (
          <div>
            <span>{t("sub.login")}</span>
            <b className="mono">{sub.loginCommand}</b>
          </div>
        )}
        {state && sub.enabled && (
          <div>
            <span>{t("sub.state")}</span>
            <b>{state}</b>
          </div>
        )}
      </div>
      {editing && (
        <SubscriptionDialog
          edit={sub}
          onClose={() => setEditing(false)}
          onSaved={() => {
            setEditing(false);
            void done();
          }}
        />
      )}
    </>
  );
}

type EnvRow = { key: string; value: string; stored?: string };

/** Adds a custom subscription CLI (a command that speaks ACP on stdio), or
 *  with `edit` changes a CLI's name, command and environment. Stored
 *  environment values are not sent back: a blank value keeps them. */
export function SubscriptionDialog({ edit, existing, onClose, onSaved }: { edit?: Subscription; existing?: string[]; onClose: () => void; onSaved: (s: Subscription) => void }) {
  const [id, setId] = useState(edit?.id ?? "");
  const [name, setName] = useState(edit?.name ?? "");
  const [cmd, setCmd] = useState(joinCommand(edit?.command));
  const [env, setEnv] = useState<EnvRow[]>(edit?.env.map((e) => ({ key: e.key, value: "", stored: e.masked })) ?? []);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const idOk = /^[a-z0-9][a-z0-9-]{0,31}$/.test(id);
  const taken = !edit && (existing ?? []).includes(id);
  const argv = splitCommand(cmd);
  const keys = env.map((r) => r.key.trim()).filter(Boolean);
  const envBad = env.some((r) => r.key.trim() && !/^[A-Za-z_][A-Za-z0-9_]*$/.test(r.key.trim())) || new Set(keys).size !== keys.length;
  const canSave = !busy && !envBad && (edit ? true : idOk && !taken && argv.length > 0);
  const auto = edit && edit.kind === "builtin" && edit.command.length === 0 ? joinCommand(edit.acpCommand) : "";
  const setRow = (i: number, p: Partial<EnvRow>) => setEnv((rs) => rs.map((r, j) => (j === i ? { ...r, ...p } : r)));

  const save = async () => {
    setBusy(true);
    setErr("");
    try {
      let r: Subscription;
      if (edit) {
        const e: Record<string, string | null> = {};
        for (const row of env) {
          const k = row.key.trim();
          if (!k) continue;
          // a stored variable left blank keeps its value
          e[k] = row.stored !== undefined && row.value === "" ? null : row.value;
        }
        r = await API.updateSubscription(edit.id, { name: name.trim(), command: argv, env: e });
        toast(t("sub.savedToast", { name: r.name }));
      } else {
        const e: Record<string, string> = {};
        for (const row of env) if (row.key.trim()) e[row.key.trim()] = row.value;
        r = await API.addSubscription({ id, name: name.trim(), command: argv, env: e });
        toast(r.error ? t("sub.addedWarn", { name: r.name, error: r.error }) : t("sub.addedToast", { name: r.name, n: r.models }));
      }
      onSaved(r);
    } catch (e: any) {
      setErr(e?.message ?? String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <div className="ovscrim show" onMouseDown={onClose} />
      <div className="modal subdlg" role="dialog" aria-modal="true" aria-label={edit ? t("sub.editTitle", { name: edit.name }) : t("sub.addTitle")}>
        <div className="mhd">
          <h3>{edit ? t("sub.editTitle", { name: edit.name }) : t("sub.addTitle")}</h3>
          <button className="ib" type="button" aria-label={t("common.close")} onClick={onClose}>
            <X size={16} />
          </button>
        </div>
        <div className="mbd">
          {!edit && <p className="small subintro">{t("sub.addIntro")}</p>}
          <div className="ap-row2">
            <label className="fl">
              <span>{t("ap.id")}</span>
              <input className="fi sm mono" value={id} disabled={!!edit} autoFocus={!edit} placeholder="my-agent" onChange={(e) => setId(e.target.value.toLowerCase())} />
            </label>
            <label className="fl">
              <span>{t("ap.name")}</span>
              <input className="fi sm" value={name} autoFocus={!!edit} placeholder={edit?.name || "My Agent"} onChange={(e) => setName(e.target.value)} />
            </label>
          </div>
          {!edit && id && !idOk && <p className="small dngt">{t("sub.idBad")}</p>}
          {taken && <p className="small dngt">{t("ap.idTaken", { id })}</p>}
          <label className="fl">
            <span>{t("sub.command")}</span>
            <input className="fi sm mono" value={cmd} placeholder={auto || "my-agent acp"} spellCheck={false} onChange={(e) => setCmd(e.target.value)} />
            <span className="small">{auto ? t("sub.commandAuto") : t("sub.commandHint")}</span>
          </label>
          <div className="fl">
            <span className="flh">
              {t("sub.env")}
              <button className="btn sm" type="button" onClick={() => setEnv((rs) => [...rs, { key: "", value: "" }])}>
                <Plus size={13} />
                {t("sub.envAdd")}
              </button>
            </span>
            {env.map((r, i) => (
              <div className="subenv" key={i}>
                <input
                  className="fi sm mono"
                  value={r.key}
                  placeholder="NAME"
                  aria-label={t("sub.envKey")}
                  spellCheck={false}
                  disabled={r.stored !== undefined}
                  onChange={(e) => setRow(i, { key: e.target.value })}
                />
                <input
                  className="fi sm mono"
                  type="password"
                  autoComplete="off"
                  value={r.value}
                  placeholder={r.stored !== undefined ? t("sub.envKeep", { masked: r.stored }) : t("sub.envValue")}
                  aria-label={t("sub.envValue")}
                  onChange={(e) => setRow(i, { value: e.target.value })}
                />
                <button className="ib" type="button" aria-label={t("sub.envRemove")} title={t("sub.envRemove")} onClick={() => setEnv((rs) => rs.filter((_, j) => j !== i))}>
                  <X size={14} />
                </button>
              </div>
            ))}
            <span className="small">{t("sub.envNote")}</span>
          </div>
          {envBad && <p className="small dngt">{t("sub.envBad")}</p>}
          {err && <p className="small dngt">{err}</p>}
          <div className="mft">
            <button className="btn" type="button" onClick={onClose}>
              {t("common.cancel")}
            </button>
            <button className="btn pri" type="button" disabled={!canSave} onClick={() => void save()}>
              {busy ? <Loader2 size={14} className="spin" /> : null}
              {edit ? t("sub.save") : busy ? t("sub.adding") : t("sub.add")}
            </button>
          </div>
        </div>
      </div>
    </>
  );
}
