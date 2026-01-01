import { clearToken, hasAuthToken, persistToken, request } from './client';

describe('api client', () => {
  afterEach(() => {
    clearToken();
    localStorage.clear();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it('normalizes and stores tokens', () => {
    persistToken('  "token-123" ');
    expect(localStorage.getItem('mpb_token')).toBe('token-123');
    expect(hasAuthToken()).toBe(true);
  });

  it('sends auth headers and parses JSON', async () => {
    persistToken('token-abc');
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => '{"ok":true}'
    } as Response);
    vi.stubGlobal('fetch', fetchMock);

    const data = await request<{ ok: boolean }>('/api/test');
    expect(data.ok).toBe(true);

    const options = fetchMock.mock.calls[0][1] as RequestInit;
    const headers = options.headers as Record<string, string>;
    expect(headers.Authorization).toBe('Bearer token-abc');
  });

  it('clears token and redirects on 401', async () => {
    persistToken('token-abc');
    const unauthorizedSpy = vi.fn();
    window.addEventListener('mpb-unauthorized', unauthorizedSpy);
    const fetchMock = vi.fn().mockResolvedValue({
      ok: false,
      status: 401,
      statusText: 'Unauthorized',
      text: async () => 'unauthorized'
    } as Response);
    vi.stubGlobal('fetch', fetchMock);

    await expect(request('/api/test')).rejects.toThrow('unauthorized');
    expect(localStorage.getItem('mpb_token')).toBeNull();
    expect(unauthorizedSpy).toHaveBeenCalledTimes(1);

    window.removeEventListener('mpb-unauthorized', unauthorizedSpy);
  });
});
