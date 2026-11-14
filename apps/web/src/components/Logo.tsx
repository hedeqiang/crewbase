// Crewbase mark — original design ("Relay").
// Three members in a rising arc hand work along to the next, above the base
// line they share. Indigo tile, white marks; the opacity steps read as a
// handover in progress. Geometry is on a 32-unit grid; see docs/brand.md.

export const LOGO_TILE = "#3B32C4";

export function Logo({ size = 20, title }: { size?: number; title?: string }) {
  return (
    <svg
      className="logo"
      width={size}
      height={size}
      viewBox="0 0 32 32"
      role={title ? "img" : undefined}
      aria-label={title}
      aria-hidden={title ? undefined : true}
    >
      <rect width="32" height="32" rx="9" fill={LOGO_TILE} />
      <circle cx="9.5" cy="17.5" r="3.2" fill="#fff" opacity="0.5" />
      <circle cx="16" cy="12" r="3.2" fill="#fff" opacity="0.75" />
      <circle cx="22.5" cy="17.5" r="3.2" fill="#fff" />
      <rect x="8" y="22.6" width="16" height="2.6" rx="1.3" fill="#fff" />
    </svg>
  );
}
