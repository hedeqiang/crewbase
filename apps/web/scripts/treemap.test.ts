// Tests the squarified treemap layout. Run: node --experimental-strip-types scripts/treemap.test.ts
import { squarify, type Rect } from "../src/treemap.ts";

let failed = 0;
const check = (name: string, ok: boolean, detail = "") => {
  if (!ok) {
    failed++;
    console.error(`FAIL ${name} ${detail}`);
  }
};
const eps = 1e-6;
const overlap = (a: Rect, b: Rect) => Math.min(a.x + a.w, b.x + b.w) - Math.max(a.x, b.x) > eps && Math.min(a.y + a.h, b.y + b.h) - Math.max(a.y, b.y) > eps;

function verify(name: string, values: number[], r: Rect) {
  const out = squarify(values, r);
  check(`${name}: one rect per value`, out.length === values.length);
  const total = values.reduce((s, v) => s + Math.max(v, 0), 0);
  let area = 0;
  out.forEach((q, i) => {
    check(`${name}: in bounds #${i}`, q.x >= r.x - eps && q.y >= r.y - eps && q.x + q.w <= r.x + r.w + eps && q.y + q.h <= r.y + r.h + eps, JSON.stringify(q));
    check(`${name}: non-negative #${i}`, q.w >= -eps && q.h >= -eps);
    const want = values[i] > 0 ? (values[i] / total) * r.w * r.h : 0;
    const got = q.w * q.h;
    area += got;
    check(`${name}: area of #${i}`, Math.abs(got - want) <= Math.max(1e-6, want * 0.001), `got ${got} want ${want}`);
  });
  check(`${name}: fills the rect`, total === 0 || Math.abs(area - r.w * r.h) < 1e-3, String(area));
  for (let i = 0; i < out.length; i++) for (let j = i + 1; j < out.length; j++) check(`${name}: no overlap ${i}/${j}`, !overlap(out[i], out[j]), JSON.stringify([out[i], out[j]]));
  return out;
}

verify("paper example", [6, 6, 4, 3, 2, 2, 1], { x: 0, y: 0, w: 6, h: 4 });
verify("unsorted with zeros", [1, 0, 50, 3, -2, 20, 0.5], { x: 10, y: 20, w: 300, h: 120 });
verify("single", [5], { x: 0, y: 0, w: 100, h: 50 });
verify("tall", [9, 1, 1, 1, 1, 1, 1, 1, 1, 1], { x: 0, y: 0, w: 40, h: 500 });
let seed = 7;
const rnd = () => ((seed = (seed * 16807) % 2147483647) / 2147483647);
const many = Array.from({ length: 300 }, () => Math.floor(rnd() * rnd() * 20000) + 1);
const out = verify("300 random", many, { x: 0, y: 0, w: 960, h: 380 });
// squarified: the largest blocks are not slivers
const big = out.map((q, i) => ({ q, v: many[i] })).sort((a, b) => b.v - a.v).slice(0, 10);
check("aspect of the largest", big.every(({ q }) => Math.max(q.w / q.h, q.h / q.w) < 4), JSON.stringify(big.map((b) => b.q)));
check("empty", squarify([], { x: 0, y: 0, w: 10, h: 10 }).length === 0);
check("all zero", squarify([0, 0], { x: 0, y: 0, w: 10, h: 10 }).every((q) => q.w === 0));

if (failed) {
  console.error(`${failed} treemap checks failed`);
  process.exit(1);
}
console.log("treemap: all checks passed");
