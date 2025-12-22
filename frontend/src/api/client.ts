type HTTPMethod = 'GET' | 'POST' | 'PUT' | 'DELETE' | 'PATCH';

export const apiBase =
  import.meta.env.VITE_API_BASE?.toString() || (typeof window !== 'undefined' ? window.location.origin : '');

const envToken = import.meta.env.VITE_API_TOKEN?.toString();
const storageKey = 'mpb_token';

function normalizeToken(value?: string | null): string | undefined {
  if (!value) return undefined;
  const trimmed = value.trim();
  if (!trimmed || trimmed === 'null' || trimmed === 'undefined') return undefined;
  if (
    (trimmed.startsWith('"') && trimmed.endsWith('"')) ||
    (trimmed.startsWith("'") && trimmed.endsWith("'"))
  ) {
    return normalizeToken(trimmed.slice(1, -1));
  }
  return trimmed;
}

function storedToken(): string | undefined {
  if (typeof window === 'undefined') return undefined;
  const raw = localStorage.getItem(storageKey);
  const normalized = normalizeToken(raw);
  if (!normalized && raw) {
    localStorage.removeItem(storageKey);
  }
  return normalized;
}

function authToken(): string | undefined {
  if (typeof window === 'undefined') return normalizeToken(envToken);
  return storedToken() || normalizeToken(envToken);
}

export function persistToken(token: string) {
  if (typeof window === 'undefined') return;
  const normalized = normalizeToken(token);
  if (!normalized) {
    localStorage.removeItem(storageKey);
    return;
  }
  localStorage.setItem(storageKey, normalized);
}

export function clearToken() {
  if (typeof window === 'undefined') return;
  localStorage.removeItem(storageKey);
}

export function hasAuthToken(): boolean {
  return !!authToken();
}

export async function request<T>(path: string, options: { method?: HTTPMethod; body?: unknown } = {}): Promise<T> {
  const { method = 'GET', body } = options;
  const headers: Record<string, string> = { 'Content-Type': 'application/json' };
  const token = authToken();
  if (token) {
    headers.Authorization = `Bearer ${token}`;
  }

  const res = await fetch(`${apiBase}${path}`, {
    method,
    headers,
    body: body ? JSON.stringify(body) : undefined
  });

  if (!res.ok) {
    if (res.status === 401) {
      clearToken();
      if (typeof window !== 'undefined') {
        window.dispatchEvent(new CustomEvent('mpb-unauthorized'));
        const path = window.location.pathname;
        if (path !== '/login' && path !== '/register') {
          window.location.replace('/login');
        }
      }
    }
    const message = await res.text();
    const error = new Error(message || res.statusText);
    (error as { status?: number }).status = res.status;
    throw error;
  }

  const text = await res.text();
  if (!text.trim()) {
    return {} as T;
  }
  try {
    return JSON.parse(text) as T;
  } catch (err) {
    throw new Error('Invalid response from server.');
  }
}
