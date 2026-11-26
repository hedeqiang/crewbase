import { t, type Key } from "../../i18n";

// On its own so the conversation's approval cards name a category without
// loading Settings.

export function catLabel(c: string): string {
  // computer:<bundle id> is operating that app (features/computertools)
  if (c.startsWith("computer:")) return t("cat.computer", { app: c.slice("computer:".length) });
  const k = `cat.${c}` as Key;
  const v = t(k);
  return v === k ? c : v;
}
