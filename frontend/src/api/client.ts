type HTTPMethod = 'GET' | 'POST' | 'PUT' | 'DELETE' | 'PATCH';

export const apiBase =
  import.meta.env.VITE_API_BASE?.toString() || (typeof window !== 'undefined' ? window.location.origin : '');

const envToken = import.meta.env.VITE_API_TOKEN?.toString();
const storageKey = 'mpb_token';

function authToken(): string | undefined {
  if (typeof window === 'undefined') return envToken;
  return localStorage.getItem(storageKey) || envToken;
}

export function persistToken(token: string) {
  if (typeof window === 'undefined') return;
  localStorage.setItem(storageKey, token);
}

export function clearToken() {
  if (typeof window === 'undefined') return;
  localStorage.removeItem(storageKey);
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
    const message = await res.text();
    throw new Error(message || res.statusText);
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
