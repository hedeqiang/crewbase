// Crewbase mark — original design ("Lookout").
// A pair of crew eyes above a small base line: the same eye the agent avatars
// wear (white sclera, dark pupil, highlight), glancing toward the work. Mint
// tile, ink pupils; geometry is on a 32-unit grid; see docs/brand/README.md.

export const LOGO_TILE = "#7FD8B4";
const INK = "#1A1A1E";

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
      <circle cx="10.6" cy="15" r="5.1" fill="#fff" />
      <circle cx="21.4" cy="15" r="5.1" fill="#fff" />
      <circle cx="11.9" cy="15.7" r="2.5" fill={INK} />
      <circle cx="22.7" cy="15.7" r="2.5" fill={INK} />
      <circle cx="12.7" cy="14.8" r="0.8" fill="#fff" />
      <circle cx="23.5" cy="14.8" r="0.8" fill="#fff" />
      <rect x="11" y="23.2" width="10" height="2.4" rx="1.2" fill={INK} />
    </svg>
  );
}
