// Dialogs as promises: openDialog shows a form and resolves with what was
// entered (and what onSubmit returned), or null when it is dismissed.
// <DialogHost> draws the one open dialog.

import type { ReactNode } from "react";

import { createStore } from "./store";

export interface Option {
  value: string;
  label: string;
}

export interface Field {
  name: string;
  label: string;
  type?: "text" | "password" | "email" | "url";
  value?: string;
  placeholder?: string;
  hint?: string;
  options?: (string | Option)[];
  multiline?: boolean;
  autocomplete?: string;
  // suggestions offers values to pick from while typing.
  suggestions?: string[];
}

export type Values = Record<string, string>;

export interface DialogSpec {
  title: string;
  text?: string;
  fields?: Field[];
  submit: string;
  // cancel names the button that closes the dialog without submitting, or
  // is false for a dialog that only tells something. Default "Cancel".
  cancel?: string | false;
  danger?: boolean;
  extra?: ReactNode;
  // onSubmit may throw to keep the dialog open with the error's message.
  onSubmit?: (values: Values) => unknown;
}

export interface DialogResult<R = unknown> {
  values: Values;
  result: R;
}

export interface OpenDialog extends DialogSpec {
  id: number;
  resolve: (r: DialogResult | null) => void;
}

export const dialogStore = createStore<OpenDialog | null>(null);
let nextID = 1;

export function openDialog<R = unknown>(spec: DialogSpec): Promise<DialogResult<R> | null> {
  // A dialog asked for while another is open waits for it.
  const previous = dialogStore.get();
  if (previous) previous.resolve(null);
  return new Promise((resolve) => {
    dialogStore.set({ ...spec, id: nextID++, resolve: resolve as (r: DialogResult | null) => void });
  });
}

export function closeDialog(id: number, result: DialogResult | null) {
  const open = dialogStore.get();
  if (!open || open.id !== id) return;
  dialogStore.set(null);
  open.resolve(result);
}
