// Line icons drawn with the text colour, and the cisid mark: three steps,
// the last one running.

const ICONS = {
  search: ["M10.5 17a6.5 6.5 0 1 0 0-13 6.5 6.5 0 0 0 0 13zM20 20l-4.6-4.6"],
  copy: ["M9 9h10v11H9zM5 15V4h10"],
  check: ["M5 12.5l4.5 4.5L19 7.5"],
  download: ["M12 4v11M7.5 10.5L12 15l4.5-4.5M5 19h14"],
  jobs: ["M4 6h16M4 12h16M4 18h10"],
  repos: ["M6 3.5h12v17H6A1.5 1.5 0 0 1 4.5 19V5A1.5 1.5 0 0 1 6 3.5zM4.5 17.5A1.5 1.5 0 0 1 6 16h12"],
  users: ["M2.5 19.5a6.5 6.5 0 0 1 13 0M16 4.8a3.5 3.5 0 0 1 0 6.4M18.5 14a6.5 6.5 0 0 1 3 5.5", "M9 4.5a3.5 3.5 0 1 1 0 7 3.5 3.5 0 0 1 0-7z"],
  account: ["M12 4a4 4 0 1 1 0 8 4 4 0 0 1 0-8zM4 20.5a8 8 0 0 1 16 0"],
  arrow: ["M5 12h14M13 6l6 6-6 6"],
} as const;

export type IconName = keyof typeof ICONS;

export function Icon({ name, size = 16 }: { name: IconName; size?: number }) {
  return (
    <svg viewBox="0 0 24 24" width={size} height={size} aria-hidden="true">
      {ICONS[name].map((d) => <path key={d} d={d} />)}
    </svg>
  );
}

export function ThemeIcon() {
  return (
    <svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true">
      <circle cx="12" cy="12" r="7.5" />
      <path className="fill" d="M12 4.5a7.5 7.5 0 0 1 0 15z" />
    </svg>
  );
}

export function Mark({ size = 24 }: { size?: number }) {
  return (
    <svg viewBox="0 0 24 24" width={size} height={size} aria-hidden="true">
      <rect className="mark-step" x="2" y="9" width="6" height="6" />
      <rect className="mark-step" x="9" y="9" width="6" height="6" />
      <rect className="mark-run" x="16" y="9" width="6" height="6" />
    </svg>
  );
}
