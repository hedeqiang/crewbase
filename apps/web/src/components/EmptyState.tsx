import type { ReactNode } from "react";

/** One way to say "nothing here yet": an icon, what it is, what to do,
 *  and at most one action. */
export function EmptyState({ icon, title, desc, action, compact }: { icon: ReactNode; title: string; desc?: string; action?: ReactNode; compact?: boolean }) {
  return (
    <div className={"estate" + (compact ? " compact" : "")}>
      <span className="esic" aria-hidden>
        {icon}
      </span>
      <b>{title}</b>
      {desc && <p>{desc}</p>}
      {action && <div className="esact">{action}</div>}
    </div>
  );
}
