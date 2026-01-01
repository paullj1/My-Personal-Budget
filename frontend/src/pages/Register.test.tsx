import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';

import Register from './Register';
import { request } from '../api/client';

vi.mock('../api/client', () => ({
  request: vi.fn(),
  persistToken: vi.fn()
}));

describe('Register', () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('shows an error when registration options are missing', async () => {
    vi.mocked(request).mockResolvedValueOnce({
      publicKey: {
        user: { id: 'abc' }
      }
    });

    const client = new QueryClient({
      defaultOptions: { mutations: { retry: false } }
    });
    const routerFuture = { v7_startTransition: true, v7_relativeSplatPath: true };
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter future={routerFuture}>
          <Register />
        </MemoryRouter>
      </QueryClientProvider>
    );

    fireEvent.change(screen.getByLabelText(/email/i), { target: { value: 'test@example.com' } });
    fireEvent.click(screen.getByRole('button', { name: /register passkey/i }));

    expect(await screen.findByText(/Registration options missing challenge or user id/i)).toBeInTheDocument();
  });
});
