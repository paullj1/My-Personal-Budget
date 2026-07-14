import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';

import Login from './Login';
import { request } from '../api/client';

vi.mock('../api/client', () => ({
  request: vi.fn(),
  persistToken: vi.fn()
}));

describe('Login', () => {
  beforeEach(() => {
    Object.defineProperty(navigator, 'credentials', {
      configurable: true,
      value: {
        get: vi.fn().mockResolvedValue(null)
      }
    });
  });

  afterEach(() => {
    vi.restoreAllMocks();
    delete (window as any).PublicKeyCredential;
  });

  const renderLogin = () => {
    const client = new QueryClient({
      defaultOptions: { mutations: { retry: false } }
    });
    const routerFuture = { v7_startTransition: true, v7_relativeSplatPath: true };
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter future={routerFuture}>
          <Login />
        </MemoryRouter>
      </QueryClientProvider>
    );
  };

  const mockLoginBegin = () => {
    vi.mocked(request).mockResolvedValueOnce({
      session_id: 'session-1',
      publicKey: {
        challenge: 'YWJj',
        rpId: 'localhost',
        allowCredentials: [],
        timeout: 60000
      }
    });
  };

  it('prompts for the passkey automatically on load', async () => {
    (window as any).PublicKeyCredential = function PublicKeyCredential() {};
    mockLoginBegin();

    renderLogin();

    expect(await screen.findByText(/Passkey login cancelled/i)).toBeInTheDocument();
    expect(navigator.credentials.get).toHaveBeenCalledTimes(1);
  });

  it('shows an error when passkey login is cancelled after a manual attempt', async () => {
    mockLoginBegin();

    renderLogin();

    expect(navigator.credentials.get).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: /use passkey/i }));

    expect(await screen.findByText(/Passkey login cancelled/i)).toBeInTheDocument();
  });
});
