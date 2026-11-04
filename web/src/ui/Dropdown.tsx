// Dropdowns drawn by the UI instead of the browser, so their lists sit on
// the same blurred glass as the navigation: Select stands in for <select>,
// Combo for an input with a <datalist>.

import { useEffect, useId, useLayoutEffect, useRef, useState, type CSSProperties, type KeyboardEvent, type RefObject } from "react";

export interface Choice {
  value: string;
  label: string;
}

// Placement keeps a list next to its control. The list is fixed to the
// viewport, so a dialog's scrolling box does not cut it off; it opens
// upward when there is more room above.
type Placement = { top?: number; bottom?: number; left: number; width: number; maxHeight: number };

function place(anchor: HTMLElement): Placement {
  const r = anchor.getBoundingClientRect();
  const gap = 6;
  const below = window.innerHeight - r.bottom - gap - 8;
  const above = r.top - gap - 8;
  const maxHeight = Math.min(280, Math.max(below, above));
  return below >= Math.min(200, above)
    ? { top: r.bottom + gap, left: r.left, width: r.width, maxHeight }
    : { bottom: window.innerHeight - r.top + gap, left: r.left, width: r.width, maxHeight };
}

const placementStyle = (p: Placement): CSSProperties => ({
  top: p.top, bottom: p.bottom, left: p.left, width: p.width, maxHeight: p.maxHeight,
});

// useDismiss closes an open list on a press outside it and its control,
// and on scrolling or resizing anywhere but inside the list.
function useDismiss(open: boolean, refs: RefObject<HTMLElement | null>[], close: () => void) {
  useEffect(() => {
    if (!open) return;
    const inside = (t: EventTarget | null) => refs.some((r) => r.current && t instanceof Node && r.current.contains(t));
    const press = (e: PointerEvent) => { if (!inside(e.target)) close(); };
    const scroll = (e: Event) => { if (!inside(e.target)) close(); };
    document.addEventListener("pointerdown", press, true);
    window.addEventListener("scroll", scroll, true);
    window.addEventListener("resize", close);
    return () => {
      document.removeEventListener("pointerdown", press, true);
      window.removeEventListener("scroll", scroll, true);
      window.removeEventListener("resize", close);
    };
  }, [open, refs, close]);
}

// keepInView scrolls the list so its active option shows.
function keepInView(list: HTMLElement | null, index: number) {
  const el = list?.children[index];
  if (el instanceof HTMLElement) el.scrollIntoView({ block: "nearest" });
}

export function Select({ id, value, choices, onChange, buttonRef }: {
  id: string;
  value: string;
  choices: Choice[];
  onChange: (value: string) => void;
  buttonRef?: (el: HTMLButtonElement | null) => void;
}) {
  const listID = useId();
  const button = useRef<HTMLButtonElement | null>(null);
  const list = useRef<HTMLUListElement | null>(null);
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(0);
  const [at, setAt] = useState<Placement | null>(null);
  const refs = useRef([button, list]).current;
  const close = useRef(() => setOpen(false)).current;
  useDismiss(open, refs, close);

  const selected = Math.max(0, choices.findIndex((c) => c.value === value));
  const show = () => {
    if (!button.current) return;
    setAt(place(button.current));
    setActive(selected);
    setOpen(true);
  };
  useLayoutEffect(() => { if (open) keepInView(list.current, active); }, [open, active]);
  useEffect(() => { if (open) list.current?.focus(); }, [open]);

  const pick = (i: number) => {
    const c = choices[i];
    if (c) onChange(c.value);
    setOpen(false);
    button.current?.focus();
  };

  const onButtonKey = (e: KeyboardEvent) => {
    if (["ArrowDown", "ArrowUp", "Enter", " "].includes(e.key)) {
      e.preventDefault();
      show();
    }
  };
  const onListKey = (e: KeyboardEvent) => {
    const last = choices.length - 1;
    switch (e.key) {
      case "ArrowDown": setActive((i) => Math.min(last, i + 1)); break;
      case "ArrowUp": setActive((i) => Math.max(0, i - 1)); break;
      case "Home": setActive(0); break;
      case "End": setActive(last); break;
      case "Enter": case " ": pick(active); break;
      case "Escape":
        // Closes the list, not the dialog around it.
        e.stopPropagation();
        setOpen(false);
        button.current?.focus();
        break;
      case "Tab": setOpen(false); return;
      default: return;
    }
    e.preventDefault();
  };

  return (
    <div className="dropdown">
      <button
        id={id} type="button" className="select" aria-haspopup="listbox" aria-expanded={open} aria-controls={open ? listID : undefined}
        ref={(el) => { button.current = el; buttonRef?.(el); }}
        onClick={() => (open ? setOpen(false) : show())} onKeyDown={onButtonKey}
      >
        <span>{choices[selected]?.label ?? value}</span>
        <svg viewBox="0 0 24 24" width="16" height="16" aria-hidden="true"><path d="M7 10l5 5 5-5" /></svg>
      </button>
      {open && at ? (
        <ul
          id={listID} ref={list} role="listbox" tabIndex={-1} className="dropdown-list glass"
          aria-labelledby={id} aria-activedescendant={`${listID}-${active}`} style={placementStyle(at)} onKeyDown={onListKey}
        >
          {choices.map((c, i) => (
            <li
              key={c.value} id={`${listID}-${i}`} role="option" aria-selected={i === selected}
              data-active={i === active || undefined} onPointerEnter={() => setActive(i)} onClick={() => pick(i)}
            >
              {c.label}
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}

export function Combo({ id, value, suggestions, onChange, placeholder, inputRef }: {
  id: string;
  value: string;
  suggestions: string[];
  onChange: (value: string) => void;
  placeholder?: string;
  inputRef?: (el: HTMLInputElement | null) => void;
}) {
  const listID = useId();
  const input = useRef<HTMLInputElement | null>(null);
  const list = useRef<HTMLUListElement | null>(null);
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(-1);
  const [at, setAt] = useState<Placement | null>(null);
  const refs = useRef([input, list]).current;
  const close = useRef(() => setOpen(false)).current;
  useDismiss(open, refs, close);

  const q = value.trim().toLowerCase();
  const shown = suggestions.filter((s) => s.toLowerCase().includes(q) && s !== value).slice(0, 50);
  const visible = open && at !== null && shown.length > 0;
  const show = () => {
    if (!input.current) return;
    setAt(place(input.current));
    setOpen(true);
  };
  useLayoutEffect(() => { if (visible && active >= 0) keepInView(list.current, active); }, [visible, active]);

  const pick = (s: string) => {
    onChange(s);
    setOpen(false);
    setActive(-1);
  };

  const onKey = (e: KeyboardEvent) => {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      if (!visible) {
        show();
        return;
      }
      const last = shown.length - 1;
      setActive((i) => (e.key === "ArrowDown" ? Math.min(last, i + 1) : Math.max(0, i - 1)));
    } else if (e.key === "Enter" && visible && active >= 0) {
      e.preventDefault(); // picks the suggestion instead of sending the form
      pick(shown[active]!);
    } else if (e.key === "Escape" && visible) {
      e.stopPropagation(); // closes the list, not the dialog around it
      e.preventDefault();
      setOpen(false);
    } else if (e.key === "Tab") {
      setOpen(false);
    }
  };

  return (
    <div className="dropdown">
      <input
        id={id} type="text" role="combobox" autoComplete="off" spellCheck={false} value={value} placeholder={placeholder}
        aria-autocomplete="list" aria-expanded={visible} aria-controls={visible ? listID : undefined}
        aria-activedescendant={visible && active >= 0 ? `${listID}-${active}` : undefined}
        ref={(el) => { input.current = el; inputRef?.(el); }}
        onChange={(e) => { onChange(e.target.value); setActive(-1); show(); }}
        onFocus={show} onClick={show} onKeyDown={onKey}
      />
      {visible ? (
        <ul id={listID} ref={list} role="listbox" className="dropdown-list glass" aria-labelledby={id} style={placementStyle(at)}>
          {shown.map((s, i) => (
            <li
              key={s} id={`${listID}-${i}`} role="option" aria-selected={i === active}
              data-active={i === active || undefined} onPointerEnter={() => setActive(i)}
              onPointerDown={(e) => e.preventDefault()} onClick={() => pick(s)}
            >
              {s}
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}
