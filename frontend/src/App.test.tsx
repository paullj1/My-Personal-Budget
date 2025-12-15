import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import App from './App';

vi.mock('./api/client', () => {
  const budgetsResponse = { data: [], meta: { count: 0 } };
  const txnsResponse = { data: [], meta: { count: 0, offset: 0, nextOffset: 0, hasMore: false } };
  return {
    apiBase: '',
    clearToken: vi.fn(),
    persistToken: vi.fn(),
    request: vi.fn((path: string) => {
      if (path.includes('/budgets') && path.includes('/transactions')) {
        return Promise.resolve(txnsResponse);
      }
      if (path.includes('/budgets')) {
        return Promise.resolve(budgetsResponse);
      }
      return Promise.resolve({});
    })
  };
});

const renderApp = () => {
  const client = new QueryClient();
  const routerFuture = { v7_startTransition: true, v7_relativeSplatPath: true };
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/dashboard']} future={routerFuture}>
        <App />
      </MemoryRouter>
    </QueryClientProvider>
  );
};

describe('App bootstrap', () => {
  it('renders the dashboard shell without crashing', async () => {
    renderApp();
    expect(await screen.findByText(/Budgets/i)).toBeInTheDocument();
  });
});
