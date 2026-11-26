import { t, type Key } from "./i18n";

/** A tool group's name and line in the interface language (built-in groups
 *  are translated; an MCP server keeps its name). */
export function groupLabel(g: { name: string; summary: string }): { name: string; desc: string } {
  const k = `tg.${g.name}` as Key;
  const v = t(k);
  if (v !== k) {
    const [name, desc] = v.split("|");
    return { name, desc: desc ?? g.summary };
  }
  const mcp = /^MCP server tools, e\.g\. (.*)$/.exec(g.summary);
  if (mcp) return { name: g.name, desc: t("tg.mcp", { names: mcp[1] }) };
  const plain = /^tools, e\.g\. (.*)$/.exec(g.summary);
  return { name: g.name, desc: plain ? t("tg.tools", { names: plain[1] }) : g.summary };
}

