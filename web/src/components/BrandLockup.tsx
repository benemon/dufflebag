/**
 * The wordmark is drawn letterforms on the mark's own grid - one SVG so the
 * shared baseline is structural (BRAND.md par.8). The seams carry their own
 * stroke: they are the only Packer blue. Ink follows currentColor so the
 * lockup is correct under either theme without a second asset.
 *
 * The viewBox crops to the true ink bounds - path extremes (y 6.5..46.5) plus
 * half the stroke and its round caps - so flex-centering aligns the lockup's
 * visual mass without clipping a stroke.
 */
export function BrandLockup({
  width,
  height,
  className,
}: {
  width: number
  height: number
  className?: string
}) {
  return (
    <svg
      className={className}
      width={width}
      height={height}
      viewBox="0 5.2 246 42.6"
      fill="none"
      stroke="currentColor"
      strokeWidth="3"
      strokeLinecap="round"
      strokeLinejoin="round"
      role="img"
      aria-label="dufflebag"
    >
      <rect x="5" y="18" width="38" height="22" rx="8" />
      <path d="M16.5 18C16.5 9.5 20 6.5 24 6.5C28 6.5 31.5 9.5 31.5 18" />
      <path className="db-lockup__seams" d="M17 18.5V39.5M31 18.5V39.5" stroke="var(--db-seam)" />
      <g transform="translate(57,0)">
        <ellipse cx="13" cy="29" rx="10" ry="11" />
        <path d="M23 6.5V40" />
        <path d="M27 18V30C27 36 31.5 40 37 40C42.5 40 47 36 47 30V18" />
        <path d="M57 40V12C57 8.5 59.5 6.5 63 6.5" />
        <path d="M71 40V12C71 8.5 73.5 6.5 77 6.5" />
        <path d="M52 18H78" />
        <path d="M82 6.5V40" />
        <path d="M88 29H108" />
        <path d="M108 29V26.5C108 21 103.5 18 98 18C92.5 18 88 21.5 88 27V31C88 36.5 92.5 40 98 40C102 40 105.5 38 107.5 35" />
        <path d="M112 6.5V40" />
        <ellipse cx="122" cy="29" rx="10" ry="11" />
        <ellipse cx="146" cy="29" rx="10" ry="11" />
        <path d="M156 18V40" />
        <ellipse cx="170" cy="29" rx="10" ry="11" />
        <path d="M180 18V39C180 44 176 46.5 171 46.5C167 46.5 164 45.5 162 43.5" />
      </g>
    </svg>
  )
}
