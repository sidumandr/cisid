// Line icons drawn with the text colour.

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
  close: ["M6 6l12 12M18 6L6 18"],
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


// ProviderMark is the mark of a forge one signs in with: GitHub's and
// GitLab's in the text colour; the other forges get a plain person.
export function ProviderMark({ provider }: { provider: string }) {
  switch (provider) {
    case "github":
      return (
        <svg className="brand" viewBox="0 0 16 16" width="18" height="18" aria-hidden="true">
          <path className="fill" d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.013 8.013 0 0 0 16 8c0-4.42-3.58-8-8-8z" />
        </svg>
      );
    case "gitlab":
      return (
        <svg className="brand" viewBox="0 0 24 24" width="18" height="18" aria-hidden="true">
          <path className="fill" d="m23.6 9.593-.033-.086L20.3.98a.851.851 0 0 0-.336-.405.875.875 0 0 0-1 .054.875.875 0 0 0-.29.44l-2.205 6.748H7.538L5.332 1.07a.857.857 0 0 0-.29-.441.875.875 0 0 0-1-.054.859.859 0 0 0-.336.405L.433 9.502l-.032.086a6.066 6.066 0 0 0 2.012 7.01l.011.009.03.021 4.976 3.727 2.462 1.863 1.5 1.132a1.009 1.009 0 0 0 1.22 0l1.499-1.132 2.462-1.863 5.006-3.749.012-.01a6.068 6.068 0 0 0 2.01-7.003z" />
        </svg>
      );
    default:
      return <Icon name="account" size={18} />;
  }
}
