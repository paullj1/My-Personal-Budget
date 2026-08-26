import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';

import OAuthConsent from './OAuthConsent';
import { hasAuthToken, request } from '../api/client';

const navigate = vi.fn();
vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual<typeof import('react-router-dom')>('react-router-dom');
  return { ...actual, useNavigate: () => navigate };
});

vi.mock('../api/client', () => ({
  request: vi.fn(),
  hasAuthToken: vi.fn(() => true)
}));

const mockRequest = request as unknown as ReturnType<typeof vi.fn>;
const mockHasToken = hasAuthToken as unknown as ReturnType<typeof vi.fn>;

const view = {
  request_id: 'req-1',
  client_name: 'Claude',
  redirect_to: 'claude.ai',
  scopes: ['budgets:read', 'budgets:write']
};

const renderConsent = (search = '?request_id=req-1') => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } }
  });
  const routerFuture = { v7_startTransition: true, v7_relativeSplatPath: true };
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[`/oauth/consent${search}`]} future={routerFuture}>
        <OAuthConsent />
      </MemoryRouter>
    </QueryClientProvider>
  );
};

describe('OAuthConsent', () => {
  beforeEach(() => {
    mockHasToken.mockReturnValue(true);
    mockRequest.mockResolvedValue(view);
    // The page hands off to the client's redirect URI, which jsdom cannot follow.
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: { replace: vi.fn(), pathname: '/oauth/consent', search: '?request_id=req-1' }
    });
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it('names the app and what it will be able to do', async () => {
    renderConsent();

    expect(await screen.findByRole('heading', { name: 'Connect Claude?' })).toBeInTheDocument();
    expect(screen.getByText(/See your budgets, balances and transactions/)).toBeInTheDocument();
    expect(screen.getByText(/Add transactions and itemize receipts/)).toBeInTheDocument();
    expect(screen.getByText('claude.ai')).toBeInTheDocument();
  });

  it('approves and follows the returned redirect', async () => {
    renderConsent();
    await screen.findByRole('heading', { name: 'Connect Claude?' });

    mockRequest.mockResolvedValueOnce({ redirect_to: 'https://claude.ai/cb?code=abc' });
    fireEvent.click(screen.getByRole('button', { name: 'Allow' }));

    await waitFor(() => {
      expect(mockRequest).toHaveBeenCalledWith('/api/v1/oauth/consent', {
        method: 'POST',
        body: { request_id: 'req-1', approve: true }
      });
      expect(window.location.replace).toHaveBeenCalledWith('https://claude.ai/cb?code=abc');
    });
  });

  it('sends a denial back to the client too', async () => {
    renderConsent();
    await screen.findByRole('heading', { name: 'Connect Claude?' });

    mockRequest.mockResolvedValueOnce({ redirect_to: 'https://claude.ai/cb?error=access_denied' });
    fireEvent.click(screen.getByRole('button', { name: 'Deny' }));

    await waitFor(() => {
      expect(mockRequest).toHaveBeenCalledWith('/api/v1/oauth/consent', {
        method: 'POST',
        body: { request_id: 'req-1', approve: false }
      });
    });
  });

  // Losing the request id to a login redirect would strand the client, so it
  // travels as ?next.
  it('sends a logged-out user to login carrying the request', async () => {
    mockHasToken.mockReturnValue(false);
    renderConsent();

    await waitFor(() => {
      expect(navigate).toHaveBeenCalledWith(
        `/login?next=${encodeURIComponent('/oauth/consent?request_id=req-1')}`,
        { replace: true }
      );
    });
  });

  it('explains a link with no authorization request', async () => {
    renderConsent('');

    expect(await screen.findByText(/missing its authorization request/i)).toBeInTheDocument();
    expect(mockRequest).not.toHaveBeenCalled();
  });
});
