import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import App from './App';

vi.mock('./api/client', () => {
  const budgetsResponse = { data: [], meta: { count: 0 } };
  const txnsResponse = { data: [], meta: { count: 0, offset: 0, nextOffset: 0, hasMore: false } };
  return {
    apiBase: '',
    clearToken: vi.fn(),
    hasAuthToken: vi.fn(() => true),
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

vi.mock('./pages/Dashboard', () => ({
  default: () => <div>Dashboard stub</div>
}));

const renderApp = () => {
  localStorage.setItem('mpb_token', 'test-token');
  const routerFuture = { v7_startTransition: true, v7_relativeSplatPath: true };
  return render(
    <MemoryRouter initialEntries={['/dashboard']} future={routerFuture}>
      <App />
    </MemoryRouter>
  );
};

describe('App bootstrap', () => {
  it('renders the dashboard shell without crashing', async () => {
    renderApp();
    expect(await screen.findByText(/Dashboard stub/i)).toBeInTheDocument();
  });
});
