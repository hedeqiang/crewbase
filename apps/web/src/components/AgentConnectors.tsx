// 可用的连接器: which connected services (MCP servers, built-in connectors)
// an agent may use. Unset = every connector its scope allows; turning one
// off saves an explicit list, and only the listed connectors' tools are
// sent to the model. Built-in tools are never affected.
import { useEffect, useState } from "react";
import { Plug } from "lucide-react";
import { API, teamOf, type AgentView, type ConnectorInfo } from "../api";
import { t } from "../i18n";
import { openSettings, setState, toast, useStore } from "../store";

/** Connectors whose scope reaches agent (global, its team or itself). */
export function connectorsFor(all: ConnectorInfo[], agent: AgentView): ConnectorInfo[] {
  const team = "team:" + teamOf(agent);
  return all.filter((c) => !c.scope || c.scope === "global" || c.scope === team || c.scope === agent.id);
}

/** Whether agent may use connector name (unset list = all). */
export const canUse = (agent: AgentView, name: string) => agent.connectors == null || agent.connectors.includes(name);

/** The list to save when agent's access to name changes. */
export function nextConnectors(agent: AgentView, reach: ConnectorInfo[], name: string, on: boolean): string[] {
  const cur = agent.connectors ?? reach.map((c) => c.name);
  return on ? [...new Set([...cur, name])] : cur.filter((n) => n !== name);
}

export function useConnectors(): ConnectorInfo[] | null {
  const tick = useStore((s) => s.mcpTick);
  const [list, setList] = useState<ConnectorInfo[] | null>(null);
  useEffect(() => {
    API.connectors().then(
      (r) => setList(r.connectors ?? []),
      () => setList((x) => x ?? []),
    );
  }, [tick]);
  return list;
}

export function AgentConnectors({ agent }: { agent: AgentView }) {
  const all = useConnectors();
  const [busy, setBusy] = useState(false);
  if (!all) return null;
  const reach = connectorsFor(all, agent);
  const save = async (connectors: string[] | null) => {
    setBusy(true);
    try {
      await API.patchAgent(agent.id, { connectors });
      toast(t("panel.saved"));
    } catch (e: any) {
      toast(e?.message ?? String(e));
    } finally {
      setBusy(false);
    }
  };
  const limited = agent.connectors != null;
  const sent = reach.filter((c) => canUse(agent, c.name)).reduce((n, c) => n + c.tools, 0);
  return (
    <>
      <div className="psec" id="agent-connectors">
        {t("panel.connectors")}
      </div>
      {reach.length === 0 ? (
        <div className="small">
          {t("panel.connectorsNone")}{" "}
          <button
            className="linkbtn"
            type="button"
            onClick={() => {
              openSettings("market");
              setState({ settingsTab: "installed" });
            }}
          >
            {t("panel.connectorsOpen")}
          </button>
        </div>
      ) : (
        <>
          <div className="tglist">
            {reach.map((c) => (
              <div className="tgrow" key={c.name}>
                <Plug size={14} className="muted" />
                <span className="g">
                  <b>{c.label || c.name}</b>
                  <span>{t("ext.toolsN", { n: c.tools })}</span>
                </span>
                <button
                  className="tg"
                  type="button"
                  role="switch"
                  disabled={busy}
                  aria-checked={canUse(agent, c.name)}
                  aria-label={c.label || c.name}
                  onClick={() => save(nextConnectors(agent, reach, c.name, !canUse(agent, c.name)))}
                />
              </div>
            ))}
          </div>
          <div className="small" style={{ marginTop: 6 }}>
            {limited ? t("panel.connectorsLimited", { n: sent }) : t("panel.connectorsAll")}{" "}
            {limited && (
              <button className="linkbtn" type="button" disabled={busy} onClick={() => save(null)}>
                {t("panel.connectorsReset")}
              </button>
            )}
          </div>
        </>
      )}
    </>
  );
}
