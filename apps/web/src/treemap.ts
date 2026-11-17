// Squarified treemap layout (Bruls, Huizing, van Wijk 2000) as a pure
// function: no DOM, no dependency. Used by the context map.

export interface Rect {
  x: number;
  y: number;
  w: number;
  h: number;
}

/** Lays values out in r. Returns one rect per value, in input order; areas
 *  are proportional to the values (non-positive values get an empty rect). */
export function squarify(values: number[], r: Rect): Rect[] {
  const out: Rect[] = values.map(() => ({ x: r.x, y: r.y, w: 0, h: 0 }));
  const total = values.reduce((s, v) => s + (v > 0 ? v : 0), 0);
  if (total <= 0 || r.w <= 0 || r.h <= 0) return out;
  const scale = (r.w * r.h) / total;
  const order = values
    .map((v, i) => ({ i, a: v > 0 ? v * scale : 0 }))
    .filter((x) => x.a > 0)
    .sort((a, b) => b.a - a.a);
  let rect = { ...r };
  let row: { i: number; a: number }[] = [];
  let rowSum = 0;

  const worst = (sum: number, max: number, min: number, side: number) => {
    const s2 = sum * sum;
    const w2 = side * side;
    return Math.max((w2 * max) / s2, s2 / (w2 * min));
  };
  const place = (items: { i: number; a: number }[], sum: number, last: boolean) => {
    if (rect.w >= rect.h) {
      // a column on the left
      const w = last ? rect.w : sum / rect.h;
      let y = rect.y;
      items.forEach((it, k) => {
        const h = k === items.length - 1 ? rect.y + rect.h - y : it.a / w;
        out[it.i] = { x: rect.x, y, w, h };
        y += h;
      });
      rect = { x: rect.x + w, y: rect.y, w: rect.w - w, h: rect.h };
    } else {
      // a row on top
      const h = last ? rect.h : sum / rect.w;
      let x = rect.x;
      items.forEach((it, k) => {
        const w = k === items.length - 1 ? rect.x + rect.w - x : it.a / h;
        out[it.i] = { x, y: rect.y, w, h };
        x += w;
      });
      rect = { x: rect.x, y: rect.y + h, w: rect.w, h: rect.h - h };
    }
  };

  for (const it of order) {
    const side = Math.min(rect.w, rect.h);
    if (row.length === 0) {
      row = [it];
      rowSum = it.a;
      continue;
    }
    const max = row[0].a; // sorted descending
    const cur = worst(rowSum, max, row[row.length - 1].a, side);
    const next = worst(rowSum + it.a, max, it.a, side);
    if (next <= cur) {
      row.push(it);
      rowSum += it.a;
    } else {
      place(row, rowSum, false);
      row = [it];
      rowSum = it.a;
    }
  }
  if (row.length) place(row, rowSum, true);
  return out;
}
