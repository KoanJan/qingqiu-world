const STORAGE_PREFIX = 'qingqiu-world';

function key(name: string): string {
  return `${STORAGE_PREFIX}-${name}`;
}

/** Typed wrapper around localStorage with JSON serialization. */
export const storage = {
  get<T = string>(name: string): T | null {
    try {
      const raw = localStorage.getItem(key(name));
      return raw ? (JSON.parse(raw) as T) : null;
    } catch {
      return null;
    }
  },

  set(name: string, value: unknown): void {
    try {
      localStorage.setItem(key(name), JSON.stringify(value));
    } catch {
      // Storage full or disabled — silently ignore (non-critical)
    }
  },

  getRaw(name: string): string | null {
    return localStorage.getItem(key(name));
  },

  setRaw(name: string, value: string): void {
    try {
      localStorage.setItem(key(name), value);
    } catch {
      // ignore
    }
  },

  remove(name: string): void {
    localStorage.removeItem(key(name));
  },
};
