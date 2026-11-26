// What the daemon's English error messages mean, said in the UI's
// language. The daemon keeps its messages for logs and tools; the pages
// that show them to people run them through here. Unknown messages come
// back unchanged (they are often a vendor's own words, still useful).

import { t, type Key } from "./i18n";

// a classified provider error: "<provider>: <kind>: <message>"
const PROVIDER = /^[\w.-]+: (auth|rate_limited|quota_exhausted|unavailable|bad_request|context_overflow|model_not_found|model_unavailable): /;

const RULES: [RegExp, Key][] = [
  // proxy settings (features/proxy)
  [/invalid proxy setting: port/i, "err.proxyPort"],
  [/invalid proxy setting: host/i, "err.proxyHost"],
  [/invalid proxy setting: a password needs a username/i, "err.proxyUser"],
  [/invalid proxy setting: .*needs host:port/i, "err.proxyHostPort"],
  [/invalid proxy setting/i, "err.proxy"],
  // paste install (features/catalog)
  [/paste an MCP config, a command, a server URL/i, "err.pasteUnknown"],
  // the network
  [/no such host|dial tcp|connection refused|i\/o timeout|context deadline exceeded|network is unreachable/i, "err.network"],
  [/x509|certificate/i, "err.tls"],
];

const KIND: Record<string, Key> = {
  auth: "err.auth",
  rate_limited: "err.rateLimited",
  quota_exhausted: "err.quota",
  unavailable: "err.unavailable",
  model_not_found: "err.modelNotFound",
  model_unavailable: "err.modelUnavailable",
};

/** The message for people; detail: the daemon's own words when they add
 *  something (shown smaller, under it). */
export function explain(msg: string): { text: string; detail?: string } {
  const m = (msg ?? "").trim();
  const k = PROVIDER.exec(m);
  if (k && KIND[k[1]]) return { text: t(KIND[k[1]]), detail: m.slice(k[0].length) };
  for (const [re, key] of RULES) if (re.test(m)) return { text: t(key), detail: m };
  return { text: m };
}

/** explain(), as one line. */
export const errText = (e: unknown): string => explain((e as any)?.message ?? String(e)).text;
