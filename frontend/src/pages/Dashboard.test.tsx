import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';

import Dashboard from './Dashboard';
import { request } from '../api/client';

vi.mock('../api/client', () => ({
  request: vi.fn(),
  apiBase: ''
}));

const mockedRequest = vi.mocked(request);

// Routes the handful of calls Dashboard makes on mount. receiptScan drives the
// capability flag the server reports from GET /api/v1/.
function stubApi({ receiptScan }: { receiptScan: boolean }) {
  mockedRequest.mockImplementation(((path: string) => {
    if (path === '/api/v1/') {
      return Promise.resolve({ features: { receipt_scan: receiptScan } });
    }
    if (path.includes('/transactions')) {
      return Promise.resolve({ data: [], meta: { count: 0, offset: 0, nextOffset: 0, hasMore: false } });
    }
    if (path.startsWith('/api/v1/budgets')) {
      return Promise.resolve({
        data: [
          { id: 1, name: 'Groceries', payroll: 0, balance: 0, credits: 0, debits: 0 },
          { id: 2, name: 'Misc', payroll: 0, balance: 0, credits: 0, debits: 0 }
        ],
        meta: { count: 2 }
      });
    }
    return Promise.resolve({});
  }) as unknown as typeof request);
}

function renderDashboard() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <Dashboard />
    </QueryClientProvider>
  );
}

// Opens the itemize wizard, which is the only place scanning is offered.
// fireEvent rather than user-event: a single click needs no extra dependency.
async function openItemizeWizard() {
  const opener = await screen.findByRole('button', { name: /itemize receipt/i });
  // The opener is disabled until budgets load.
  await waitFor(() => expect(opener).toBeEnabled());
  fireEvent.click(opener);
  await screen.findByRole('heading', { name: /itemize receipt/i });
}

afterEach(() => {
  vi.clearAllMocks();
});

describe('Dashboard receipt scanning', () => {
  it('offers both a new photo and an existing one when scanning is available', async () => {
    stubApi({ receiptScan: true });
    renderDashboard();
    await openItemizeWizard();

    expect(screen.getByRole('button', { name: /take photo/i })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /choose photo/i })).toBeInTheDocument();
    expect(screen.getByText(/take or choose a photo/i)).toBeInTheDocument();
  });

  it('only the camera input asks for the camera', async () => {
    stubApi({ receiptScan: true });
    renderDashboard();
    await openItemizeWizard();

    const camera = screen.getByTestId('receipt-camera-input');
    const library = screen.getByTestId('receipt-library-input');

    // `capture` is what opens the camera directly -- and on iOS it also removes
    // "Photo Library" from the action sheet, so the library input must not set it.
    expect(camera).toHaveAttribute('capture', 'environment');
    expect(library).not.toHaveAttribute('capture');

    // Both accept any image so an iPhone's HEIC library photos are selectable;
    // the canvas step re-encodes to JPEG before upload.
    expect(camera).toHaveAttribute('accept', 'image/*');
    expect(library).toHaveAttribute('accept', 'image/*');
  });

  it('scans a picked file from the library', async () => {
    stubApi({ receiptScan: true });
    renderDashboard();
    await openItemizeWizard();

    const library = screen.getByTestId('receipt-library-input') as HTMLInputElement;
    const file = new File(['not-a-real-jpeg'], 'IMG_2608.jpeg', { type: 'image/jpeg' });
    fireEvent.change(library, { target: { files: [file] } });

    // Normalization fails on a fake JPEG in jsdom, which is the point: the error
    // surfaces and manual entry stays usable rather than the wizard hanging.
    await waitFor(() =>
      expect(screen.getByRole('button', { name: /save itemized receipt/i })).toBeInTheDocument()
    );
  });

  it('hides scanning entirely when the endpoint is not configured', async () => {
    // RECEIPT_OCR_URL unset on the server, so it reports receipt_scan: false.
    stubApi({ receiptScan: false });
    renderDashboard();
    await openItemizeWizard();

    // No button, and no copy advertising a feature that is not there.
    expect(screen.queryByRole('button', { name: /take photo/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /choose photo/i })).not.toBeInTheDocument();
    expect(screen.queryByTestId('receipt-camera-input')).not.toBeInTheDocument();
    expect(screen.queryByTestId('receipt-library-input')).not.toBeInTheDocument();
    expect(screen.getByText(/start with the receipt total/i)).toBeInTheDocument();

    // Manual itemizing is untouched: the fields and the save action remain.
    expect(screen.getByLabelText(/receipt total/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/catch-all budget/i)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /save itemized receipt/i })).toBeInTheDocument();
  });

  it('hides scanning when the capability call fails', async () => {
    // A failed or unreachable capability check must fail closed, not render a
    // button whose endpoint returns 503.
    mockedRequest.mockImplementation(((path: string) => {
      if (path === '/api/v1/') {
        return Promise.reject(new Error('boom'));
      }
      if (path.includes('/transactions')) {
        return Promise.resolve({ data: [], meta: { count: 0, offset: 0, nextOffset: 0, hasMore: false } });
      }
      if (path.startsWith('/api/v1/budgets')) {
        return Promise.resolve({
          data: [{ id: 1, name: 'Groceries', payroll: 0, balance: 0, credits: 0, debits: 0 }],
          meta: { count: 1 }
        });
      }
      return Promise.resolve({});
    }) as unknown as typeof request);

    renderDashboard();
    await openItemizeWizard();

    expect(screen.queryByRole('button', { name: /take photo/i })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: /save itemized receipt/i })).toBeInTheDocument();
  });

  it('never calls the scan endpoint when scanning is disabled', async () => {
    stubApi({ receiptScan: false });
    renderDashboard();
    await openItemizeWizard();

    const scanCalls = mockedRequest.mock.calls.filter(([path]) => String(path).includes('/receipts/scan'));
    expect(scanCalls).toHaveLength(0);
  });
});

describe('Dashboard with an empty account', () => {
  it('renders when the budget list comes back null', async () => {
    // The API sends `data: null` for an empty list, which is what a brand-new
    // account looks like. That used to allocate a fresh [] on every render and
    // re-trigger the effects keyed on it, looping until the tab locked up. If the
    // loop returns, this test times out rather than passing.
    mockedRequest.mockImplementation(((path: string) => {
      if (path === '/api/v1/') {
        return Promise.resolve({ features: { receipt_scan: true } });
      }
      if (path.includes('/transactions')) {
        return Promise.resolve({ data: null, meta: { count: 0, offset: 0, nextOffset: 0, hasMore: false } });
      }
      if (path.startsWith('/api/v1/budgets')) {
        return Promise.resolve({ data: null, meta: { count: 0 } });
      }
      return Promise.resolve({});
    }) as unknown as typeof request);

    renderDashboard();

    expect(await screen.findByRole('heading', { name: /budgets/i })).toBeInTheDocument();
    // With no budgets there is nothing to itemize, so the opener stays disabled.
    await waitFor(() =>
      expect(screen.getByRole('button', { name: /itemize receipt/i })).toBeDisabled()
    );
  });
});

describe('scan progress', () => {
  it('shows a progress bar with elapsed and remaining time while scanning', async () => {
    // A scan that never settles, so the in-flight state can be inspected.
    let release: (() => void) | undefined;
    mockedRequest.mockImplementation(((path: string) => {
      if (path === '/api/v1/') {
        return Promise.resolve({ features: { receipt_scan: true } });
      }
      if (path.includes('/receipts/scan')) {
        return new Promise(() => {
          release = () => undefined;
        });
      }
      if (path.includes('/transactions')) {
        return Promise.resolve({ data: [], meta: { count: 0, offset: 0, nextOffset: 0, hasMore: false } });
      }
      if (path.startsWith('/api/v1/budgets')) {
        return Promise.resolve({
          data: [{ id: 1, name: 'Groceries', payroll: 0, balance: 0, credits: 0, debits: 0 }],
          meta: { count: 1 }
        });
      }
      return Promise.resolve({});
    }) as unknown as typeof request);

    renderDashboard();
    await openItemizeWizard();

    const library = screen.getByTestId('receipt-library-input') as HTMLInputElement;
    // A real JPEG header, so normalization gets as far as the request.
    const file = new File([new Uint8Array([0xff, 0xd8, 0xff, 0xd9])], 'r.jpg', { type: 'image/jpeg' });
    fireEvent.change(library, { target: { files: [file] } });

    const bar = await screen.findByRole('progressbar');
    expect(bar).toBeInTheDocument();
    expect(bar).toHaveAttribute('max', '1');
    // An estimate, but a bounded one: never a full bar on an open request.
    const value = Number((bar as HTMLProgressElement).value);
    expect(value).toBeGreaterThanOrEqual(0);
    expect(value).toBeLessThan(1);

    expect(await screen.findByText(/elapsed/i)).toBeInTheDocument();
    // Cancelling must be possible while it runs.
    expect(screen.getByRole('button', { name: /cancel scan/i })).toBeInTheDocument();
    expect(release).toBeUndefined();
  });

  it('shows no progress bar when idle', async () => {
    stubApi({ receiptScan: true });
    renderDashboard();
    await openItemizeWizard();
    expect(screen.queryByRole('progressbar')).not.toBeInTheDocument();
  });
});
