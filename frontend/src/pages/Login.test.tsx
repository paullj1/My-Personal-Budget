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
  });

  it('shows an error when passkey login is cancelled', async () => {
    vi.mocked(request).mockResolvedValueOnce({
      session_id: 'session-1',
      publicKey: {
        challenge: 'YWJj',
        rpId: 'localhost',
        allowCredentials: [],
        timeout: 60000
      }
    });

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

    fireEvent.click(screen.getByRole('button', { name: /use passkey/i }));

    expect(await screen.findByText(/Passkey login cancelled/i)).toBeInTheDocument();
  });
});
