// Fails when a UI string has no English, or English and Chinese disagree on
// placeholders ({name}). Run by `pnpm build` (and so by CI).
import { readFileSync } from "node:fs";

const src = readFileSync(new URL("../src/i18n.ts", import.meta.url), "utf8");
const block = (start) => {
  const i = src.indexOf(start);
  const j = src.indexOf("\n};", i);
  return src.slice(i, j);
};
const entries = (b) => new Map([...b.matchAll(/\n {2}"([^"]+)": "((?:[^"\\]|\\.)*)"/g)].map((m) => [m[1], m[2]]));
const zh = entries(block("const zh = {"));
const en = entries(block("const en: Partial<Record<Key, string>> = {"));
const ph = (s) => [...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort().join(",");
const missing = [...zh.keys()].filter((k) => !en.has(k));
const extra = [...en.keys()].filter((k) => !zh.has(k));
const mismatch = [...zh.keys()].filter((k) => en.has(k) && ph(zh.get(k)) !== ph(en.get(k)));
const cjk = [...en.entries()].filter(([, v]) => /[一-鿿]/.test(v.replace(/简体中文|飞书/g, ""))).map(([k]) => k);
let bad = false;
const report = (title, xs) => {
  if (!xs.length) return;
  bad = true;
  console.error(`${title} (${xs.length}): ${xs.slice(0, 30).join(", ")}${xs.length > 30 ? ", …" : ""}`);
};
report("i18n: no English for", missing);
report("i18n: English for unknown keys", extra);
report("i18n: placeholders differ", mismatch);
report("i18n: Chinese left in English", cjk);
if (bad) process.exit(1);
console.log(`i18n: ${zh.size} strings, all translated`);
