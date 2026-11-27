import { EmptyState } from "../EmptyState";
import { Skeleton } from "../Skeleton";
import { Fragment, useEffect, useState } from "react";
import { Copy, FileText, KeyRound, MessagesSquare, Plug, Plus } from "lucide-react";
import { ProviderMark } from "../ProviderMark";
import { API, type VaultItem } from "../../api";
import { t, type Key } from "../../i18n";
import { ago, providerName } from "../../format";
import { toast } from "../../store";
import { Head } from "../Settings";
import { confirmDialog } from "../Confirm";

/** Settings › 保管库: every secret Crewbase keeps, by what it is for. Values
 *  are write-only: replace or delete, never read back. */
export function Vault() {
  const [items, setItems] = useState<VaultItem[] | null>(null);
  const [err, setErr] = useState("");
  const [adding, setAdding] = useState(false);
  const load = () =>
    API.vault().then(
      (r) => {
        setItems(r.items ?? []);
        setErr("");
      },
      (e) => setErr(e?.message ?? String(e)),
    );
  useEffect(() => {
    void load();
  }, []);

  const groups: { kind: VaultItem["kind"][]; title: Key }[] = [
    { kind: ["provider", "model"], title: "vlt.g.models" },
    { kind: ["app"], title: "vlt.g.apps" },
    { kind: ["mcp"], title: "vlt.g.mcp" },
    { kind: ["mcp-login"], title: "vlt.g.logins" },
    { kind: ["im"], title: "vlt.g.im" },
    { kind: ["secret"], title: "vlt.g.secrets" },
  ];
  return (
    <>
      <Head
        title={t("set.vault")}
        sub={t("vlt.sub")}
        right={
          <button className="btn sm" type="button" onClick={() => setAdding(true)} disabled={adding}>
            <Plus size={13} />
            {t("vlt.add")}
          </button>
        }
      />
      <div className="vb">
        {adding && <AddSecret onDone={() => (setAdding(false), void load())} onCancel={() => setAdding(false)} />}
        {err && <p className="small errt">{err}</p>}
        {!items && !err && <Skeleton />}
        {items && items.length === 0 && (
          <EmptyState icon={<KeyRound size={22} />} title={t("es.vault")} desc={t("vlt.empty")} />
        )}
        {items &&
          groups.map((g) => {
            const rows = items.filter((x) => g.kind.includes(x.kind));
            if (!rows.length) return null;
            return (
              <Fragment key={g.title}>
                <div className="vsec">{t(g.title)}</div>
                <div className="sbox">
                  {rows.map((it) => (
                    <SecretRow key={it.id} it={it} onChange={() => void load()} />
                  ))}
                </div>
              </Fragment>
            );
          })}
        <p className="small muted vltnote">{t("vlt.note")}</p>
      </div>
    </>
  );
}

function label(it: VaultItem): { name: string; what: string; sub?: string } {
  if (it.kind === "app" && it.app === "agentdocs") {
    const r = it.role ?? "";
    if (r === "admin") return { name: "Open Agent Docs", what: t("vlt.ad.admin") };
    if (r === "session") return { name: "Open Agent Docs", what: t("vlt.ad.session") };
    if (r.startsWith("agent:")) return { name: "Open Agent Docs", what: t("vlt.ad.agent", { name: r.slice(6) }) };
  }
  switch (it.kind) {
    case "provider":
    case "model":
      return { name: providerName(it.target ?? "") || it.target || it.id, what: t("vlt.k.apiKey") };
    case "mcp":
      return { name: it.target ?? it.id, what: it.key ? t("vlt.k.var", { key: it.key.replace(/-/g, "_").toUpperCase() }) : t("vlt.k.token") };
    case "mcp-login":
      return { name: it.target ?? it.id, what: t("vlt.k.login") };
    case "im":
      return { name: it.target ?? it.id, what: t("vlt.k.bot") };
  }
  return { name: it.target ?? it.id, what: t("vlt.k.secret"), sub: it.ref };
}

function SecretRow({ it, onChange }: { it: VaultItem; onChange: () => void }) {
  const [replacing, setReplacing] = useState(false);
  const [value, setValue] = useState("");
  const [busy, setBusy] = useState(false);
  const l = label(it);
  const save = async () => {
    if (!value.trim()) return;
    setBusy(true);
    try {
      await API.replaceSecret(it.id, value);
      toast(t("vlt.replaced", { name: l.name }));
      setReplacing(false);
      setValue("");
      onChange();
    } catch (e: any) {
      toast(e?.message ?? String(e));
    } finally {
      setBusy(false);
    }
  };
  const del = async () => {
    // one in use: say what stops working
    const text = it.inUse ? (it.kind === "app" ? t("vlt.deleteAppConfirm", { name: l.name, what: l.what }) : t("vlt.deleteInUseConfirm", { name: l.name, what: l.what })) : t("vlt.deleteConfirm", { name: l.name, what: l.what });
    if (!(await confirmDialog({ text, danger: true, ok: t("vlt.delete") }))) return;
    try {
      await API.deleteSecret(it.id);
      toast(t("vlt.deleted", { name: l.name }));
      onChange();
    } catch (e: any) {
      toast(e?.message ?? String(e));
    }
  };
  return (
    <div className="srow vltrow">
      <span className="vlic" aria-hidden>
        {it.kind === "provider" || it.kind === "model" ? <ProviderMark provider={it.target ?? ""} size={18} /> : it.kind === "app" ? <FileText size={16} /> : it.kind === "im" ? <MessagesSquare size={16} /> : it.kind === "secret" ? <KeyRound size={16} /> : <Plug size={16} />}
      </span>
      <span className="g">
        <b>
          {l.name}
          <span className="vlwhat">{l.what}</span>
          {it.inUse && <span className="pill sm ok">{t("vlt.inUse")}</span>}
        </b>
        <span>
          <span className="mono">{it.masked || "•••"}</span> · {t("vlt.updated", { when: ago(it.updated) })}
          {l.sub && <> · <span className="mono">{l.sub}</span></>}
        </span>
      </span>
      {replacing ? (
        <span className="c vltedit">
          <input
            className="fi sm mono"
            type="password"
            autoFocus
            value={value}
            placeholder={t("vlt.newValue")}
            onChange={(e) => setValue(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") void save();
              if (e.key === "Escape") setReplacing(false);
            }}
          />
          <button className="btn sm pri" type="button" disabled={busy || !value.trim()} onClick={() => void save()}>
            {t("vlt.save")}
          </button>
          <button className="btn sm ghost" type="button" onClick={() => setReplacing(false)}>
            {t("common.cancel")}
          </button>
        </span>
      ) : (
        <span className="c">
          <button
            className="ix"
            type="button"
            title={t("vlt.copyRef", { ref: it.ref })}
            aria-label={t("vlt.copyRef", { ref: it.ref })}
            onClick={() => void navigator.clipboard.writeText(it.ref).then(() => toast(t("vlt.copied", { ref: it.ref })))}
          >
            <Copy size={13} />
          </button>
          {it.kind !== "app" && (
            <button className="btn sm" type="button" onClick={() => setReplacing(true)}>
              {t("vlt.replace")}
            </button>
          )}
          <button className="btn sm dng" type="button" onClick={() => void del()}>
            {t("vlt.delete")}
          </button>
        </span>
      )}
    </div>
  );
}

function AddSecret({ onDone, onCancel }: { onDone: () => void; onCancel: () => void }) {
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const [busy, setBusy] = useState(false);
  const ok = /^[A-Za-z0-9._@-]{1,64}$/.test(name) && value.trim() !== "";
  const save = async () => {
    if (!ok) return;
    setBusy(true);
    try {
      const it = await API.addSecret(name, value);
      toast(t("vlt.added", { ref: it.ref }));
      onDone();
    } catch (e: any) {
      toast(e?.message ?? String(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="sbox vltadd">
      <div className="srow">
        <span className="g">
          <b>{t("vlt.addTitle")}</b>
          <span>{t("vlt.addDesc", { ref: "vault:crewbase/" + (name || "…") })}</span>
        </span>
      </div>
      <div className="srow vltform">
        <input className="fi sm mono" value={name} placeholder={t("vlt.namePh")} autoFocus onChange={(e) => setName(e.target.value.trim())} />
        <input
          className="fi sm mono"
          type="password"
          value={value}
          placeholder={t("vlt.valuePh")}
          onChange={(e) => setValue(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && void save()}
        />
        <button className="btn sm pri" type="button" disabled={!ok || busy} onClick={() => void save()}>
          {t("vlt.save")}
        </button>
        <button className="btn sm ghost" type="button" onClick={onCancel}>
          {t("common.cancel")}
        </button>
      </div>
    </div>
  );
}
