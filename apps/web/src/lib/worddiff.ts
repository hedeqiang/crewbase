// Word-level changes inside a changed line: a removed line and the line
// that replaced it are compared word by word, and the words that differ
// are marked inside their (already highlighted) HTML.

/** Words, runs of spaces, single CJK characters and single symbols. */
function tokens(s: string): string[] {
  return s.match(/\s+|[\p{L}\p{N}_]+|./gu) ?? [];
}

export type Range = [number, number];

/** The character ranges of a and b that are not shared (null when the
 *  lines have too little in common to be the same line edited). */
export function changedRanges(a: string, b: string): { a: Range[]; b: Range[] } | null {
  if (a === b) return { a: [], b: [] };
  const ta = tokens(a);
  const tb = tokens(b);
  const n = ta.length;
  const m = tb.length;
  if (!n || !m || n * m > 60_000) return null;
  // longest common subsequence of tokens
  const dp: Uint16Array[] = Array.from({ length: n + 1 }, () => new Uint16Array(m + 1));
  for (let i = n - 1; i >= 0; i--)
    for (let j = m - 1; j >= 0; j--) dp[i][j] = ta[i] === tb[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
  const keepA = new Array<boolean>(n).fill(false);
  const keepB = new Array<boolean>(m).fill(false);
  let shared = 0;
  for (let i = 0, j = 0; i < n && j < m; ) {
    if (ta[i] === tb[j]) {
      keepA[i++] = keepB[j++] = true;
      shared += ta[i - 1].trim() ? ta[i - 1].length : 0;
    } else if (dp[i + 1][j] >= dp[i][j + 1]) i++;
    else j++;
  }
  // mostly different: a rewritten line, not an edited one
  const visible = (s: string) => s.replace(/\s+/g, "").length || 1;
  if (shared / Math.max(visible(a), visible(b)) < 0.35) return null;
  const ranges = (ts: string[], keep: boolean[]) => {
    const out: Range[] = [];
    let at = 0;
    ts.forEach((tok, i) => {
      if (!keep[i]) {
        const last = out[out.length - 1];
        if (last && last[1] === at) last[1] = at + tok.length;
        else out.push([at, at + tok.length]);
      }
      at += tok.length;
    });
    return out;
  };
  return { a: ranges(ta, keepA), b: ranges(tb, keepB) };
}

/** html with the text in ranges wrapped in <mark class="wd">, closed
 *  around tags so the highlighting spans stay whole. */
export function markRanges(html: string, ranges: Range[]): string {
  if (!ranges.length) return html;
  let out = "";
  let pos = 0; // index in the text the HTML shows
  let open = false;
  let r = 0;
  const inRange = (i: number) => {
    while (r < ranges.length && ranges[r][1] <= i) r++;
    return r < ranges.length && ranges[r][0] <= i;
  };
  const re = /(<[^>]+>)|(&[#\w]+;)|([\s\S])/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(html))) {
    if (m[1]) {
      if (open) {
        out += "</mark>";
        open = false;
      }
      out += m[1];
      continue;
    }
    const want = inRange(pos);
    if (want && !open) {
      out += '<mark class="wd">';
      open = true;
    } else if (!want && open) {
      out += "</mark>";
      open = false;
    }
    out += m[2] ?? m[3];
    pos++;
  }
  if (open) out += "</mark>";
  return out;
}
