// DialogHost draws the dialog openDialog asked for, as a modal <dialog>.

import { useEffect, useRef, useState, useSyncExternalStore, type FormEvent } from "react";

import { closeDialog, dialogStore, type Field, type OpenDialog, type Values } from "../lib/dialog";

export function DialogHost() {
  const open = useSyncExternalStore(dialogStore.subscribe, dialogStore.get);
  const ref = useRef<HTMLDialogElement>(null);

  useEffect(() => {
    const d = ref.current;
    if (!d) return;
    if (open && !d.open) d.showModal();
    if (!open && d.open) d.close();
  }, [open]);

  return (
    <dialog ref={ref} aria-labelledby="dialog-title" onCancel={(e) => {
      e.preventDefault();
      if (open) closeDialog(open.id, null);
    }}>
      {open ? <DialogForm key={open.id} spec={open} /> : null}
    </dialog>
  );
}

function initial(fields: Field[]): Values {
  const v: Values = {};
  for (const f of fields) {
    const first = f.options?.[0];
    v[f.name] = f.value ?? (first === undefined ? "" : typeof first === "string" ? first : first.value);
  }
  return v;
}

function DialogForm({ spec }: { spec: OpenDialog }) {
  const fields = spec.fields ?? [];
  const [values, setValues] = useState<Values>(() => initial(fields));
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const firstRef = useRef<HTMLElement | null>(null);
  const submitRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    (firstRef.current ?? submitRef.current)?.focus();
  }, []);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const result = spec.onSubmit ? await spec.onSubmit(values) : undefined;
      closeDialog(spec.id, { values, result });
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setBusy(false);
    }
  }

  const set = (name: string, value: string) => setValues((v) => ({ ...v, [name]: value }));

  return (
    <form noValidate onSubmit={submit}>
      <h2 id="dialog-title">{spec.title}</h2>
      {spec.text ? <p>{spec.text}</p> : null}
      {spec.extra}
      {fields.map((f, i) => {
        const id = "f-" + f.name;
        const listID = f.suggestions?.length ? id + "-list" : undefined;
        const bind = (el: HTMLElement | null) => {
          if (i === 0) firstRef.current = el;
        };
        let input;
        if (f.options) {
          input = (
            <select id={id} name={f.name} ref={bind} value={values[f.name]} onChange={(e) => set(f.name, e.target.value)}>
              {f.options.map((o) => typeof o === "string"
                ? <option key={o} value={o}>{o}</option>
                : <option key={o.value} value={o.value}>{o.label}</option>)}
            </select>
          );
        } else if (f.multiline) {
          input = <textarea id={id} name={f.name} ref={bind} rows={4} autoComplete="off" spellCheck={false}
            value={values[f.name]} placeholder={f.placeholder} onChange={(e) => set(f.name, e.target.value)} />;
        } else {
          input = <input id={id} name={f.name} ref={bind} type={f.type ?? "text"} autoComplete={f.autocomplete ?? "off"}
            spellCheck={false} list={listID} value={values[f.name]} placeholder={f.placeholder}
            onChange={(e) => set(f.name, e.target.value)} />;
        }
        return (
          <div className="field" key={f.name}>
            <label htmlFor={id}>{f.label}</label>
            {input}
            {listID ? <datalist id={listID}>{f.suggestions!.map((s) => <option key={s} value={s} />)}</datalist> : null}
            {f.hint ? <span className="hint">{f.hint}</span> : null}
          </div>
        );
      })}
      <p className="form-error" role="alert">{error}</p>
      <div className="dialog-actions">
        {spec.cancel === false ? null
          : <button className="btn" type="button" onClick={() => closeDialog(spec.id, null)}>{spec.cancel ?? "Cancel"}</button>}
        <button ref={submitRef} className={spec.danger ? "btn danger" : "btn primary"} type="submit" disabled={busy}>{spec.submit}</button>
      </div>
    </form>
  );
}
