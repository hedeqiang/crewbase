import { t } from "../i18n";

/** Placeholder while something loads: soft bars in its shape that shimmer,
 *  shown after a moment (a fast load never flashes them). Screen readers
 *  hear 加载中. */
export function Skeleton({ kind = "lines", n }: { kind?: "lines" | "rows" | "code" | "chat"; n?: number }) {
  const count = n ?? (kind === "rows" ? 4 : kind === "code" ? 6 : kind === "chat" ? 3 : 3);
  return (
    <div className={"skel skel-" + kind} role="status" aria-busy="true" aria-label={t("common.loading")}>
      {Array.from({ length: count }, (_, i) =>
        kind === "rows" ? (
          <div className="skr" key={i}>
            <i className="skb sq" />
            <span>
              <i className="skb" style={{ width: `${55 - ((i * 13) % 25)}%` }} />
              <i className="skb thin" style={{ width: `${80 - ((i * 17) % 30)}%` }} />
            </span>
          </div>
        ) : kind === "chat" ? (
          <div className={"skm" + (i % 2 ? " me" : "")} key={i}>
            {i % 2 === 0 && <i className="skb av" />}
            <span>
              <i className="skb" style={{ width: i % 2 ? "38%" : `${70 - i * 6}%` }} />
              {i % 2 === 0 && <i className="skb" style={{ width: `${52 - i * 4}%` }} />}
            </span>
          </div>
        ) : (
          <i className="skb" key={i} style={{ width: kind === "code" ? `${30 + ((i * 37) % 60)}%` : i === count - 1 ? "60%" : `${92 - i * 6}%` }} />
        ),
      )}
    </div>
  );
}
