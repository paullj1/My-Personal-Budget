import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';

import Connections from './Connections';
import { request } from '../api/client';

vi.mock('../api/client', () => ({
  request: vi.fn()
}));

const mockRequest = request as unknown as ReturnType<typeof vi.fn>;

const connection = {
  id: 4,
  client_id: 'mpbc_abc',
  client_name: 'Claude',
  scope: 'budgets:read budgets:write',
  created_at: '2026-08-01T12:00:00Z',
  last_used_at: '2026-08-20T09:00:00Z',
  expires_at: null
};

const apiKey = {
  id: 1,
  user_id: 1,
  email: 'me@example.com',
  name: 'CLI',
  prefix: 'mpb_abcd1234',
  created_at: '2026-07-01T12:00:00Z',
  last_used_at: null
};

// routeResponses answers each endpoint the page calls, so a test only has to say
// what is different about its own case.
const setupRequests = (overrides: { oauth?: boolean; connections?: unknown[] } = {}) => {
  const { oauth = true, connections = [connection] } = overrides;
  mockRequest.mockImplementation((path: string) => {
    if (path === '/api/v1/') {
      return Promise.resolve({
        features: { oauth, receipt_scan: true },
        mcp_url: 'https://budget.example/mcp'
      });
    }
    if (path === '/api/v1/api-keys') {
      return Promise.resolve({ data: [apiKey], meta: { count: 1 } });
    }
    if (path === '/api/v1/connections') {
      return Promise.resolve({ data: connections, meta: { count: connections.length } });
    }
    return Promise.resolve({});
  });
};

const renderPage = () => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } }
  });
  const routerFuture = { v7_startTransition: true, v7_relativeSplatPath: true };
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter future={routerFuture}>
        <Connections />
      </MemoryRouter>
    </QueryClientProvider>
  );
};

describe('Connections', () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  it('lists connected apps alongside API keys', async () => {
    setupRequests();
    renderPage();

    expect(await screen.findByRole('heading', { name: 'Connections' })).toBeInTheDocument();
    expect(await screen.findByText('Claude')).toBeInTheDocument();
    // Both halves stay on one screen; keys were not replaced by OAuth.
    expect(await screen.findByText('CLI')).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Connected apps' })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Active keys' })).toBeInTheDocument();
  });

  it('shows a connection with no expiry as never expiring', async () => {
    setupRequests();
    renderPage();

    expect(await screen.findByText('Expires: Never')).toBeInTheDocument();
  });

  it('shows the expiry date when one is set', async () => {
    setupRequests({ connections: [{ ...connection, expires_at: '2026-12-25T00:00:00Z' }] });
    renderPage();

    await screen.findByText('Claude');
    expect(screen.getByText(/^Expires: (?!Never)/)).toBeInTheDocument();
  });

  it('disconnects an app', async () => {
    setupRequests();
    renderPage();

    fireEvent.click(await screen.findByRole('button', { name: 'Disconnect' }));

    await waitFor(() => {
      expect(mockRequest).toHaveBeenCalledWith('/api/v1/connections/4', { method: 'DELETE' });
    });
  });

  it('sets an expiry on a connection', async () => {
    setupRequests();
    renderPage();

    await screen.findByText('Claude');
    fireEvent.change(screen.getByRole('combobox'), { target: { value: '30 days' } });

    await waitFor(() => {
      const call = mockRequest.mock.calls.find(([path]) => path === '/api/v1/connections/4');
      expect(call).toBeDefined();
      expect(call?.[1].method).toBe('PATCH');
      expect(typeof call?.[1].body.expires_at).toBe('string');
    });
  });

  it('clears an expiry by choosing Never', async () => {
    setupRequests({ connections: [{ ...connection, expires_at: '2026-12-25T00:00:00Z' }] });
    renderPage();

    await screen.findByText('Claude');
    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'Never' } });

    await waitFor(() => {
      const call = mockRequest.mock.calls.find(([path]) => path === '/api/v1/connections/4');
      expect(call?.[1].body).toEqual({ expires_at: null });
    });
  });

  // Without a public origin the server cannot run an authorization server, so
  // there is nothing to show and nothing to ask for.
  it('hides connected apps when OAuth is not configured', async () => {
    setupRequests({ oauth: false });
    renderPage();

    expect(await screen.findByText('CLI')).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: 'Connected apps' })).not.toBeInTheDocument();
    expect(mockRequest).not.toHaveBeenCalledWith('/api/v1/connections');
  });

  it('still creates API keys', async () => {
    setupRequests();
    mockRequest.mockImplementation((path: string, options?: { method?: string }) => {
      if (path === '/api/v1/api-keys' && options?.method === 'POST') {
        return Promise.resolve({ ...apiKey, id: 2, token: 'mpb_secret' });
      }
      if (path === '/api/v1/') return Promise.resolve({ features: { oauth: true } });
      if (path === '/api/v1/api-keys') return Promise.resolve({ data: [apiKey], meta: { count: 1 } });
      if (path === '/api/v1/connections') return Promise.resolve({ data: [], meta: { count: 0 } });
      return Promise.resolve({});
    });
    renderPage();

    fireEvent.click(await screen.findByRole('button', { name: 'Create API key' }));

    // The plaintext key is shown once, right after it is minted.
    expect(await screen.findByText('mpb_secret')).toBeInTheDocument();
  });
});
