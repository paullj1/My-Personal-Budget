// The /vitest entry point registers the matchers and their types, so
// expect(...).toBeInTheDocument() typechecks instead of only working at runtime.
import '@testing-library/jest-dom/vitest';

// Restore Web Storage under vitest.
//
// Node now declares `localStorage` and `sessionStorage` on globalThis but leaves
// them undefined unless started with --localstorage-file. vitest's jsdom
// environment only copies a window property onto globalThis if the name is either
// absent from globalThis or on its internal allowlist, and Web Storage is on
// neither -- so it sees Node's placeholder, declines to overwrite it, and every
// test that touches storage gets `undefined`.
//
// This surfaced when CI moved to Node 24: the same tests passed on older Node,
// where no such global existed and jsdom's implementation was copied over.
//
// Prefer jsdom's own Storage, which vitest exposes through the `jsdom` handle, so
// tests keep real browser semantics (per-origin, quota, storage events). The
// in-memory fallback only exists so a future vitest that stops exposing that
// handle degrades to skipped assertions rather than a wall of TypeErrors.
function memoryStorage(): Storage {
  const data = new Map<string, string>();
  return {
    get length() {
      return data.size;
    },
    clear: () => data.clear(),
    getItem: (key: string) => (data.has(String(key)) ? data.get(String(key))! : null),
    key: (index: number) => Array.from(data.keys())[index] ?? null,
    removeItem: (key: string) => void data.delete(String(key)),
    setItem: (key: string, value: string) => void data.set(String(key), String(value))
  } as Storage;
}

for (const name of ['localStorage', 'sessionStorage'] as const) {
  if (typeof (globalThis as Record<string, unknown>)[name] !== 'undefined') {
    continue;
  }
  const fromJsdom = (globalThis as { jsdom?: { window?: Record<string, unknown> } }).jsdom?.window?.[name];
  Object.defineProperty(globalThis, name, {
    configurable: true,
    writable: true,
    value: (fromJsdom as Storage | undefined) ?? memoryStorage()
  });
}

// Provide a basic matchMedia mock for components that read it.
if (typeof window !== 'undefined' && !window.matchMedia) {
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    value: (query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false
    })
  });
}
