import { type ReactNode } from "react";

// The heading and row of a settings page, on their own: pages reachable
// from the conversation (automation) use them without loading Settings.

export function Head({ title, sub, right }: { title: string; sub?: string; right?: ReactNode }) {
  return (
    <div className="vh">
      <h2>{title}</h2>
      {sub && <span className="sub">{sub}</span>}
      {right && <div className="r">{right}</div>}
    </div>
  );
}

export function Row({ title, desc, children, dim }: { title: string; desc?: string; children: ReactNode; dim?: boolean }) {
  return (
    <div className={"srow" + (dim ? " dim" : "")}>
      <div className="g">
        <b>{title}</b>
        {desc && <span>{desc}</span>}
      </div>
      <div className="c">{children}</div>
    </div>
  );
}
