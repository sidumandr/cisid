// A tiny external store, for state that code outside React changes too
// (who is signed in, the open dialog).

type Listener = () => void;

export interface Store<T> {
  get(): T;
  set(value: T): void;
  subscribe(listener: Listener): () => void;
}

export function createStore<T>(initial: T): Store<T> {
  let value = initial;
  const listeners = new Set<Listener>();
  return {
    get: () => value,
    set(next) {
      value = next;
      for (const l of listeners) l();
    },
    subscribe(listener) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
  };
}
