// The cisid logo: the mark, two eyes behind crossed lenses, and the
// wordmark with its orange d. Drawn inline so their colours follow the
// theme (--brand and --brand-ink); the drawings are those of the logo files.

import { useId } from "react";

// MarkShapes draws the mark in the logo files' own coordinates (x 14 to
// 186, y 4 to 104). The mask leaves a gap where the lenses cross, so one
// line passes over the other.
function MarkShapes() {
  const mask = "logo-mask" + useId().replace(/[^a-zA-Z0-9_-]/g, "");
  return (
    <>
      <defs>
        <mask id={mask} maskUnits="userSpaceOnUse" x="0" y="0" width="200" height="120">
          <rect className="logo-mask-keep" x="0" y="0" width="200" height="120" />
          <line className="logo-mask-cut" x1="93.34" y1="70.74" x2="106.66" y2="49.26" />
        </mask>
      </defs>
      <path
        className="logo-line" mask={`url(#${mask})`}
        d="M88.9 77.9 L111.1 42.1 A34 34 0 0 1 129.66 27.61 L166 16 L173.36 53.46 A34 34 0 0 1 111.1 77.9 L88.9 42.1 A34 34 0 0 0 70.34 27.61 L34 16 L26.64 53.46 A34 34 0 0 0 88.9 77.9 Z"
      />
      <line className="logo-line" x1="88.9" y1="77.9" x2="111.1" y2="42.1" />
      <g className="logo-eye"><g className="logo-blink">
        <path className="logo-ink" d="M66.16 48.77 L68.75 50.97 A4.76 4.76 0 1 0 75.75 56.91 L79.73 60.3 A10 10 0 1 1 66.16 48.77 Z" />
      </g></g>
      <g className="logo-eye"><g className="logo-blink">
        <path className="logo-ink" d="M133.84 48.77 L131.25 50.97 A4.76 4.76 0 1 1 124.25 56.91 L120.27 60.3 A10 10 0 1 0 133.84 48.77 Z" />
      </g></g>
    </>
  );
}

// LogoMark is the mark alone, for the sidebar. size is its width.
export function LogoMark({ size = 32 }: { size?: number }) {
  return (
    <svg className="logo" viewBox="14 4 172 100" width={size} height={Math.round((size * 100) / 172)} aria-hidden="true">
      <MarkShapes />
    </svg>
  );
}

// Wordmark is the mark with the name, for the introduction, the sign-in
// pages and the footers. height is its height; it says "cisid" to readers.
export function Wordmark({ height = 24 }: { height?: number }) {
  return (
    <svg className="logo" viewBox="0 0 845 217" width={Math.round((height * 845) / 217)} height={height} role="img" aria-label="cisid">
      <g transform="translate(0 37) scale(1.8) translate(-14 -4)">
        <MarkShapes />
      </g>
      <g transform="translate(374 0) scale(1.92308)">
        <path className="logo-letter" d="M54.39 54.57 A24 24 0 1 0 54.39 85.43" />
        <line className="logo-letter" x1="83" y1="40" x2="83" y2="100" />
        <path className="logo-letter" d="M130.83 51.12 A12 12 0 1 0 121 70 A12 12 0 1 1 111.17 88.88" />
        <line className="logo-letter" x1="159" y1="40" x2="159" y2="100" />
        <circle className="logo-letter logo-d" cx="209" cy="70" r="24" />
        <line className="logo-letter logo-d" x1="233" y1="5" x2="233" y2="100" />
        <circle className="logo-ink" cx="83" cy="24" r="7" />
        <circle className="logo-ink" cx="159" cy="24" r="7" />
      </g>
    </svg>
  );
}
