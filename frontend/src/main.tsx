import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { BrowserRouter } from 'react-router-dom';

import App from './App';
import './styles.css';

const queryClient = new QueryClient();
const routerFuture = { v7_startTransition: true, v7_relativeSplatPath: true };
const root = document.getElementById('root');

if (!root) {
  throw new Error('Root element not found');
}

createRoot(root).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter future={routerFuture}>
        <App />
      </BrowserRouter>
    </QueryClientProvider>
  </StrictMode>
);
