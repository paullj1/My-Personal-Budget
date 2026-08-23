import { FocusEvent, FormEvent, ReactNode, useEffect, useMemo, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient
} from '@tanstack/react-query';

import { request } from '../api/client';
import { normalizeReceiptImage } from '../utils/receiptImage';
import {
  DEFAULT_ESTIMATE_MS,
  estimateMs,
  progressFraction,
  progressLabel,
  recordSample
} from '../utils/scanProgress';
import {
  ItemizedLine,
  buildReceiptCommit,
  fromCents,
  lineTotalCents,
  toCents,
  toDateInput
} from '../utils/receiptLines';

type Budget = {
  id: number;
  name: string;
  payroll: number;
  balance: number;
  credits: number;
  debits: number;
};

type Transaction = {
  id: number;
  description: string;
  credit: boolean;
  amount: number;
  created_at: string;
};

type AutoBalanceSource = {
  source_budget_id: number;
  weight: number;
};

type AutoBalanceConfig = {
  enabled: boolean;
  sources: AutoBalanceSource[];
};

type BudgetsResponse = {
  data: Budget[];
  meta: { count: number };
};

type TransactionsResponse = {
  data: Transaction[];
  meta: { count: number; offset: number; nextOffset: number; hasMore: boolean };
};

type Share = { id: number; email: string };

type NewTxnState = {
  description: string;
  credit: boolean;
  amount: number;
  transfer: boolean;
  transferBudgetId: number | null;
};

type Reconciliation = {
  ok: boolean;
  items_sum_cents: number;
  subtotal_cents: number;
  items_delta_cents: number;
  computed_total_cents: number;
  printed_total_cents: number;
  total_delta_cents: number;
  message?: string;
};

type ScanItem = {
  position: number;
  line_text: string;
  norm_key: string;
  description: string;
  marker?: string;
  taxable: boolean | null;
  amount_cents: number;
  tax_cents: number;
  adjust_cents: number;
  total_cents: number;
  suggested_budget_id: number | null;
  suggestion_source?: string;
};

type ScanResponse = {
  merchant: string;
  purchased_at: string | null;
  subtotal_cents: number;
  tax_cents: number;
  total_cents: number;
  tax_evidence: string;
  tax_basis: string;
  reconciliation: Reconciliation;
  items: ScanItem[];
  elapsed_ms: number;
};

type FeaturesResponse = { features?: { receipt_scan?: boolean } };

const INITIAL_TXN: NewTxnState = {
  description: '',
  credit: false,
  amount: 0,
  transfer: false,
  transferBudgetId: null
};

const PAGE_SIZE = 20;
const round2 = (value: number) => Math.round(value * 100) / 100;
const numberFormatter = new Intl.NumberFormat('en-US', {
  minimumFractionDigits: 2,
  maximumFractionDigits: 2
});
const formatNumber = (value: number) => numberFormatter.format(value);
const formatHeadingBalance = (value: number) =>
  value < 0 ? `(${formatNumber(Math.abs(value))})` : formatNumber(value);
const newLine = (): ItemizedLine => ({
  id: Math.random().toString(36).slice(2, 10),
  budgetId: null,
  description: '',
  amount: 0,
  taxCents: 0,
  adjustCents: 0
});
const splitEvenly = (total: number, buckets: number) => {
  if (buckets <= 0) return [];
  const cents = Math.round(total * 100);
  const base = Math.floor(cents / buckets);
  let remainder = cents - base * buckets;
  const allocations: number[] = [];
  for (let i = 0; i < buckets; i += 1) {
    const extra = remainder > 0 ? 1 : 0;
    allocations.push((base + extra) / 100);
    if (remainder > 0) remainder -= 1;
  }
  return allocations;
};

const ModalPortal = ({ children }: { children: ReactNode }) => {
  if (typeof document === 'undefined') {
    return <>{children}</>;
  }
  return createPortal(children, document.body);
};

const selectOnFocus = (event: FocusEvent<HTMLInputElement>) => {
  event.currentTarget.select();
};

const Dashboard = () => {
  const queryClient = useQueryClient();
  const [expanded, setExpanded] = useState<number | null>(null);
  const [search, setSearch] = useState('');
  const [newTxnBudget, setNewTxnBudget] = useState<number | null>(null);
  const [newTxn, setNewTxn] = useState<NewTxnState>({ ...INITIAL_TXN });
  const [newBudgetOpen, setNewBudgetOpen] = useState(false);
  const [settingsBudget, setSettingsBudget] = useState<number | null>(null);
  const [editingTxnId, setEditingTxnId] = useState<number | null>(null);
  const [editingTxn, setEditingTxn] = useState<{ description: string; credit: boolean; amount: number } | null>(
    null
  );
  const [editingBudgetName, setEditingBudgetName] = useState('');
  const [payrollEdit, setPayrollEdit] = useState<number | null>(null);
  const [autoBalanceEnabled, setAutoBalanceEnabled] = useState(false);
  const [autoBalanceSelections, setAutoBalanceSelections] = useState<Record<number, boolean>>({});
  const [runPayrollResult, setRunPayrollResult] = useState<number | null>(null);
  const [balanceWizardOpen, setBalanceWizardOpen] = useState(false);
  const [selectedNegatives, setSelectedNegatives] = useState<number[]>([]);
  const [selectedPositives, setSelectedPositives] = useState<number[]>([]);
  const [itemizeWizardOpen, setItemizeWizardOpen] = useState(false);
  const [receiptTotal, setReceiptTotal] = useState<number>(0);
  const [receiptDescription, setReceiptDescription] = useState('');
  const [catchAllBudgetId, setCatchAllBudgetId] = useState<number | null>(null);
  const [itemizedLines, setItemizedLines] = useState<ItemizedLine[]>([newLine()]);
  const [receiptDate, setReceiptDate] = useState('');
  const [scanning, setScanning] = useState(false);
  const [scanElapsedMs, setScanElapsedMs] = useState(0);
  const [scanEstimateMs, setScanEstimateMs] = useState(DEFAULT_ESTIMATE_MS);
  const [scanError, setScanError] = useState<string | null>(null);
  const [scanMeta, setScanMeta] = useState<{
    reconciliation: Reconciliation;
    taxCents: number;
    elapsedMs: number;
  } | null>(null);
  const scanAbort = useRef<AbortController | null>(null);
  // Two inputs rather than one. `capture` is what makes a file input open the
  // camera directly, but on iOS it also removes "Photo Library" from the action
  // sheet, so a single capture input can only ever take a new photo. Platforms
  // disagree about what they offer when it is absent, so each source gets its own
  // input and the choice is explicit.
  const cameraInputRef = useRef<HTMLInputElement | null>(null);
  const libraryInputRef = useRef<HTMLInputElement | null>(null);
  const [shareEmail, setShareEmail] = useState('');
  const [showFilter, setShowFilter] = useState(false);
  const [showToolbarFilterToggle, setShowToolbarFilterToggle] = useState(false);
  const modalOpen =
    Boolean(newTxnBudget) || newBudgetOpen || Boolean(settingsBudget) || balanceWizardOpen || itemizeWizardOpen;

  useEffect(() => {
    const handler = () => setNewBudgetOpen(true);
    window.addEventListener('open-new-budget', handler);
    return () => window.removeEventListener('open-new-budget', handler);
  }, []);
  // The scan is one synchronous request with nothing to report progress, so the
  // bar runs off a local clock against a learned estimate. Ticking four times a
  // second is smooth enough to read and cheap.
  useEffect(() => {
    if (!scanning) return;
    const startedAt = Date.now();
    setScanElapsedMs(0);
    const id = window.setInterval(() => setScanElapsedMs(Date.now() - startedAt), 250);
    return () => window.clearInterval(id);
  }, [scanning]);
  useEffect(() => {
    document.body.classList.toggle('modal-open', modalOpen);
    document.documentElement.classList.toggle('modal-open', modalOpen);
    if (modalOpen) {
      setExpanded(null);
    }
    return () => {
      document.body.classList.remove('modal-open');
      document.documentElement.classList.remove('modal-open');
    };
  }, [modalOpen]);

  const budgetsQuery = useQuery({
    queryKey: ['budgets'],
    queryFn: () => request<BudgetsResponse>('/api/v1/budgets')
  });
  // The API reports whether an inference endpoint is configured; without one the
  // scan button stays hidden and manual itemizing behaves exactly as before.
  const featuresQuery = useQuery({
    queryKey: ['features'],
    staleTime: 5 * 60 * 1000,
    queryFn: () => request<FeaturesResponse>('/api/v1/')
  });
  const scanEnabled = featuresQuery.data?.features?.receipt_scan === true;
  const autoBalanceQuery = useQuery({
    enabled: !!settingsBudget,
    queryKey: ['auto-balance', settingsBudget],
    queryFn: () => request<AutoBalanceConfig>(`/api/v1/budgets/${settingsBudget}/auto-balance`)
  });
  // Memoized so the identity is stable across renders. Without this, an empty
  // list -- which the API sends as `data: null` -- produced a fresh [] on every
  // render, and the effects keyed on `budgets` re-ran forever. A user with no
  // budgets yet hung the dashboard.
  const budgets = useMemo(() => budgetsQuery.data?.data ?? [], [budgetsQuery.data]);
  useEffect(() => {
    if (!settingsBudget) {
      setEditingBudgetName('');
      setPayrollEdit(null);
      setAutoBalanceEnabled(false);
      setAutoBalanceSelections({});
      setRunPayrollResult(null);
      return;
    }
    const target = budgets.find((b) => b.id === settingsBudget);
    setEditingBudgetName(target?.name ?? '');
    setPayrollEdit(target?.payroll ?? null);
  }, [settingsBudget, budgets]);
  useEffect(() => {
    if (!settingsBudget || !autoBalanceQuery.data) {
      return;
    }
    setAutoBalanceEnabled(autoBalanceQuery.data.enabled);
    const selections: Record<number, boolean> = {};
    (autoBalanceQuery.data.sources || []).forEach((source) => {
      selections[source.source_budget_id] = source.weight > 0;
    });
    setAutoBalanceSelections(selections);
  }, [settingsBudget, autoBalanceQuery.data]);
  useEffect(() => {
    if (!settingsBudget) return;
    setAutoBalanceSelections((prev) => {
      const next: Record<number, boolean> = {};
      budgets.forEach((budget) => {
        if (budget.id === settingsBudget) return;
        if (prev[budget.id] !== undefined) {
          next[budget.id] = prev[budget.id];
        }
      });
      return next;
    });
  }, [budgets, settingsBudget]);
  useEffect(() => {
    if (budgets.length === 0) {
      setCatchAllBudgetId(null);
      return;
    }
    setItemizedLines((prev) => {
      const next = prev.map((line) => {
        if (!line.budgetId) return line;
        return budgets.some((b) => b.id === line.budgetId) ? line : { ...line, budgetId: null };
      });
      // map() always allocates; only take the new array if a line really changed.
      return next.some((line, idx) => line !== prev[idx]) ? next : prev;
    });
    if (catchAllBudgetId && budgets.some((b) => b.id === catchAllBudgetId)) return;
    setCatchAllBudgetId(budgets[0].id);
  }, [budgets, catchAllBudgetId]);
  const expandedBudget = useMemo(() => budgets.find((b) => b.id === expanded) || null, [budgets, expanded]);
  const filteredBudgets = useMemo(() => {
    if (expanded) return budgets;
    if (!search.trim()) return budgets;
    const term = search.toLowerCase();
    return budgets.filter((b) => `${b.name} ${b.balance} ${b.payroll}`.toLowerCase().includes(term));
  }, [budgets, expanded, search]);
  const negativeBudgets = useMemo(() => budgets.filter((b) => b.balance < 0), [budgets]);
  const positiveBudgets = useMemo(() => budgets.filter((b) => b.balance > 0), [budgets]);
  const autoBalanceOptions = useMemo(
    () => budgets.filter((b) => (settingsBudget ? b.id !== settingsBudget : true)),
    [budgets, settingsBudget]
  );
  const autoBalanceSelectionCount = useMemo(
    () => autoBalanceOptions.reduce((sum, b) => sum + (autoBalanceSelections[b.id] ? 1 : 0), 0),
    [autoBalanceOptions, autoBalanceSelections]
  );
  const totalDeficit = useMemo(
    () =>
      round2(
        selectedNegatives.reduce((sum, id) => {
          const target = budgets.find((b) => b.id === id);
          if (target && target.balance < 0) {
            return sum + Math.abs(target.balance);
          }
          return sum;
        }, 0)
      ),
    [selectedNegatives, budgets]
  );
  const positiveCoverage = useMemo(
    () =>
      round2(
        selectedPositives.reduce((sum, id) => {
          const target = budgets.find((b) => b.id === id);
          if (target && target.balance > 0) {
            return sum + target.balance;
          }
          return sum;
        }, 0)
      ),
    [selectedPositives, budgets]
  );
  const positiveAllocation = useMemo(
    () => splitEvenly(totalDeficit, selectedPositives.length),
    [totalDeficit, selectedPositives.length]
  );
  const wizardReady =
    selectedNegatives.length > 0 && selectedPositives.length > 0 && totalDeficit > 0 && positiveAllocation.length > 0;
  const coverageShortfall = wizardReady && positiveCoverage + 1e-6 < totalDeficit;
  // Must include each line's allocated tax, or a scanned receipt looks
  // under-allocated by exactly its tax and double-counts it into the catch-all.
  const allocatedTotal = useMemo(
    () => round2(fromCents(itemizedLines.reduce((sum, line) => sum + lineTotalCents(line), 0))),
    [itemizedLines]
  );
  const itemizeRemainder = useMemo(() => round2(receiptTotal - allocatedTotal), [receiptTotal, allocatedTotal]);
  const overAllocated = itemizeRemainder < -0.009;
  // An unassigned line is still committable -- it falls to the catch-all -- so
  // readiness depends on having an amount, not on having picked a budget. Requiring
  // a budget here meant a scan with no history suggestions could not be committed
  // until every row was set by hand.
  const activeItemLines = useMemo(
    () => itemizedLines.filter((line) => line.amount > 0),
    [itemizedLines]
  );
  const catchAllBudget = useMemo(
    () => budgets.find((b) => b.id === catchAllBudgetId) || null,
    [budgets, catchAllBudgetId]
  );
  const itemizeReady = receiptTotal > 0 && catchAllBudgetId !== null && activeItemLines.length > 0 && !overAllocated;

  useEffect(() => {
    // filter() always allocates, so returning it unconditionally changed state
    // identity on every run and scheduled another render. Keep the previous array
    // when nothing was actually dropped.
    const keepUnchanged = (prev: number[], next: number[]) =>
      next.length === prev.length ? prev : next;
    setSelectedNegatives((prev) =>
      keepUnchanged(prev, prev.filter((id) => budgets.some((b) => b.id === id && b.balance < 0)))
    );
    setSelectedPositives((prev) =>
      keepUnchanged(prev, prev.filter((id) => budgets.some((b) => b.id === id && b.balance > 0)))
    );
  }, [budgets]);
  const transferOptions = useMemo(() => budgets.filter((b) => b.id !== newTxnBudget), [budgets, newTxnBudget]);
  const transferDisabled = transferOptions.length === 0;
  const transferReady =
    !newTxn.transfer || (!!newTxn.transferBudgetId && newTxn.transferBudgetId !== newTxnBudget);

  useEffect(() => {
    if (!newTxn.transfer) return;
    const fallback = transferOptions.find((b) => b.id !== newTxnBudget);

    if (newTxnBudget && newTxn.transferBudgetId === newTxnBudget) {
      setNewTxn((prev) => ({ ...prev, transferBudgetId: fallback?.id ?? null }));
      return;
    }
    if (newTxn.transferBudgetId && !transferOptions.some((b) => b.id === newTxn.transferBudgetId)) {
      setNewTxn((prev) => ({ ...prev, transferBudgetId: fallback?.id ?? null }));
    }
  }, [newTxn.transfer, newTxn.transferBudgetId, newTxnBudget, transferOptions]);

  const transactionsQuery = useInfiniteQuery({
    enabled: !!expanded,
    queryKey: ['transactions', expanded, search],
    initialPageParam: 0,
    queryFn: ({ pageParam, queryKey }) => {
      const budgetId = queryKey[1] as number | null;
      if (!budgetId) {
        return Promise.resolve({
          data: [],
          meta: { count: 0, offset: 0, nextOffset: 0, hasMore: false }
        } as TransactionsResponse);
      }
      return request<TransactionsResponse>(
        `/api/v1/budgets/${budgetId}/transactions?limit=${PAGE_SIZE}&offset=${pageParam}${
          search ? `&q=${encodeURIComponent(search)}` : ''
        }`
      );
    },
    getNextPageParam: (lastPage) => (lastPage.meta.hasMore ? lastPage.meta.nextOffset : undefined)
  });

  const sharesQuery = useQuery({
    enabled: !!settingsBudget,
    queryKey: ['shares', settingsBudget],
    queryFn: () => request<{ data: Share[] }>(`/api/v1/budgets/${settingsBudget}/shares`)
  });

  const createBudget = useMutation({
    mutationFn: (payload: { name: string; payroll: number }) =>
      request<Budget>('/api/v1/budgets', { method: 'POST', body: payload }),
    onSuccess: (created) => {
      setExpanded(created.id);
      queryClient.invalidateQueries({ queryKey: ['budgets'] });
    }
  });

  const createTransaction = useMutation({
    mutationFn: async (payload: { sourceBudgetId: number; txn: NewTxnState }) => {
      const { sourceBudgetId, txn } = payload;
      const isTransfer = txn.transfer && txn.transferBudgetId && txn.transferBudgetId !== sourceBudgetId;

      if (isTransfer) {
        await request(`/api/v1/budgets/${sourceBudgetId}/transactions`, {
          method: 'POST',
          body: { description: txn.description, credit: false, amount: txn.amount }
        });
        await request(`/api/v1/budgets/${txn.transferBudgetId}/transactions`, {
          method: 'POST',
          body: { description: txn.description, credit: true, amount: txn.amount }
        });
        return { transferTargetId: txn.transferBudgetId };
      }

      await request(`/api/v1/budgets/${sourceBudgetId}/transactions`, {
        method: 'POST',
        body: { description: txn.description, credit: txn.credit, amount: txn.amount }
      });
      return { transferTargetId: null };
    },
    onSuccess: (result, payload) => {
      setNewTxn({ ...INITIAL_TXN });
      setNewTxnBudget(null);
      queryClient.invalidateQueries({ queryKey: ['transactions', payload.sourceBudgetId] });
      if (result?.transferTargetId) {
        queryClient.invalidateQueries({ queryKey: ['transactions', result.transferTargetId] });
      }
      queryClient.invalidateQueries({ queryKey: ['budgets'] });
    }
  });

  const itemizeReceipt = useMutation({
    mutationFn: async (payload: {
      description: string;
      date: string;
      total: number;
      catchAllBudgetId: number;
      lines: ItemizedLine[];
      reconciled: boolean;
    }) => {
      const body = buildReceiptCommit({
        description: payload.description,
        date: payload.date,
        total: payload.total,
        catchAllBudgetId: payload.catchAllBudgetId,
        lines: payload.lines,
        reconciled: payload.reconciled
      });

      // One request, one database transaction. The previous implementation
      // looped POSTs and could leave a receipt half-committed.
      const result = await request<{ budget_ids: number[] }>('/api/v1/receipts', {
        method: 'POST',
        body
      });
      return { budgetIds: result.budget_ids || [] };
    },
    onSuccess: (result) => {
      resetItemizeWizard(false);
      queryClient.invalidateQueries({ queryKey: ['budgets'] });
      result.budgetIds.forEach((id) => queryClient.invalidateQueries({ queryKey: ['transactions', id] }));
      itemizeReceipt.reset();
    }
  });

  const updateBudgetSettings = useMutation({
    mutationFn: async (payload: {
      budgetId: number;
      name: string;
      payroll: number;
      autoBalanceEnabled: boolean;
      autoBalanceSelections: Record<number, boolean>;
    }) => {
      const sources = Object.entries(payload.autoBalanceSelections)
        .filter(([, enabled]) => enabled)
        .map(([id]) => ({ source_budget_id: Number(id), weight: 1 }));
      const budget = await request<Budget>(`/api/v1/budgets/${payload.budgetId}`, {
        method: 'PUT',
        body: { name: payload.name, payroll: payload.payroll }
      });
      await request(`/api/v1/budgets/${payload.budgetId}/auto-balance`, {
        method: 'PUT',
        body: { enabled: payload.autoBalanceEnabled, sources: payload.autoBalanceEnabled ? sources : [] }
      });
      return budget;
    },
    onSuccess: (_, payload) => {
      queryClient.invalidateQueries({ queryKey: ['budgets'] });
      queryClient.invalidateQueries({ queryKey: ['auto-balance', payload.budgetId] });
      setPayrollEdit(null);
      setSettingsBudget(null);
      setEditingBudgetName('');
    }
  });

  const deleteBudget = useMutation({
    mutationFn: (budgetId: number) => request(`/api/v1/budgets/${budgetId}`, { method: 'DELETE' }),
    onMutate: async (budgetId) => {
      setExpanded(null);
      setSettingsBudget(null);
      await queryClient.cancelQueries({ queryKey: ['budgets'] });
      const previous = queryClient.getQueryData<BudgetsResponse>(['budgets']);
      if (previous?.data) {
        queryClient.setQueryData<BudgetsResponse>(['budgets'], {
          ...previous,
          data: previous.data.filter((budget) => budget.id !== budgetId),
          meta: {
            ...previous.meta,
            count: Math.max(0, previous.meta.count - 1)
          }
        });
      }
      return { previous };
    },
    onError: (_error, _budgetId, context) => {
      if (context?.previous) {
        queryClient.setQueryData(['budgets'], context.previous);
      }
    },
    onSuccess: () => {
      setExpanded(null);
      setSettingsBudget(null);
    },
    onSettled: () => {
      queryClient.invalidateQueries({ queryKey: ['budgets'] });
    }
  });

  const runPayroll = useMutation({
    mutationFn: (budgetId: number) =>
      request<{ count: number }>(`/api/v1/budgets/${budgetId}/payroll/run`, { method: 'POST' }),
    onSuccess: (result) => {
      queryClient.invalidateQueries({ queryKey: ['budgets'] });
      queryClient.invalidateQueries({ queryKey: ['transactions'] });
      setRunPayrollResult(result.count);
    }
  });

  const addShare = useMutation({
    mutationFn: (payload: { budgetId: number; email: string }) =>
      request(`/api/v1/budgets/${payload.budgetId}/shares`, {
        method: 'POST',
        body: { email: payload.email }
      }),
    onSuccess: () => {
      setShareEmail('');
      queryClient.invalidateQueries({ queryKey: ['shares', settingsBudget] });
    }
  });

  const removeShare = useMutation({
    mutationFn: (payload: { budgetId: number; email: string }) =>
      request(`/api/v1/budgets/${payload.budgetId}/shares`, {
        method: 'DELETE',
        body: { email: payload.email }
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['shares', settingsBudget] });
    }
  });

  const updateTransaction = useMutation({
    mutationFn: (payload: { budgetId: number; txnId: number; description: string; credit: boolean; amount: number }) =>
      request<Transaction>(`/api/v1/budgets/${payload.budgetId}/transactions/${payload.txnId}`, {
        method: 'PUT',
        body: {
          description: payload.description,
          credit: payload.credit,
          amount: payload.amount
        }
      }),
    onSuccess: (_, payload) => {
      setEditingTxnId(null);
      setEditingTxn(null);
      queryClient.invalidateQueries({ queryKey: ['transactions', payload.budgetId] });
      queryClient.invalidateQueries({ queryKey: ['budgets'] });
    }
  });

  const deleteTransaction = useMutation({
    mutationFn: (payload: { budgetId: number; txnId: number }) =>
      request(`/api/v1/budgets/${payload.budgetId}/transactions/${payload.txnId}`, {
        method: 'DELETE'
      }),
    onSuccess: (_, payload) => {
      setEditingTxnId(null);
      setEditingTxn(null);
      queryClient.invalidateQueries({ queryKey: ['transactions', payload.budgetId] });
      queryClient.invalidateQueries({ queryKey: ['budgets'] });
    }
  });

  const balanceBudgets = useMutation({
    mutationFn: async (payload: { negativeIds: number[]; positiveIds: number[] }) => {
      const negatives = budgets.filter((b) => payload.negativeIds.includes(b.id) && b.balance < 0);
      const positives = budgets.filter((b) => payload.positiveIds.includes(b.id) && b.balance > 0);
      if (!negatives.length || !positives.length) {
        throw new Error('Select at least one negative and one positive budget.');
      }
      const total = round2(
        negatives.reduce((sum, b) => {
          if (b.balance < 0) return sum + Math.abs(b.balance);
          return sum;
        }, 0)
      );
      if (total <= 0) {
        throw new Error('Nothing to balance.');
      }
      const allocations = splitEvenly(total, positives.length);
      const description = `Balance wizard ${new Date().toLocaleDateString()}`;
      for (let i = 0; i < positives.length; i += 1) {
        const amount = allocations[i];
        if (amount <= 0) continue;
        await request(`/api/v1/budgets/${positives[i].id}/transactions`, {
          method: 'POST',
          body: { description, credit: false, amount }
        });
      }
      for (const budget of negatives) {
        const amount = round2(Math.abs(budget.balance));
        if (amount <= 0) continue;
        await request(`/api/v1/budgets/${budget.id}/transactions`, {
          method: 'POST',
          body: { description, credit: true, amount }
        });
      }
      return {
        negatives: negatives.map((b) => b.id),
        positives: positives.map((b) => b.id)
      };
    },
    onSuccess: (result) => {
      setBalanceWizardOpen(false);
      setSelectedNegatives([]);
      setSelectedPositives([]);
      queryClient.invalidateQueries({ queryKey: ['budgets'] });
      [...result.negatives, ...result.positives].forEach((id) =>
        queryClient.invalidateQueries({ queryKey: ['transactions', id] })
      );
    }
  });

  const addItemLine = () => setItemizedLines((prev) => [...prev, newLine()]);
  const updateItemLine = (id: string, updates: Partial<ItemizedLine>) =>
    setItemizedLines((prev) => prev.map((line) => (line.id === id ? { ...line, ...updates } : line)));
  const removeItemLine = (id: string) =>
    setItemizedLines((prev) => (prev.length <= 1 ? prev : prev.filter((line) => line.id !== id)));
  const resetItemizeWizard = (resetMutation = true) => {
    scanAbort.current?.abort();
    scanAbort.current = null;
    setItemizeWizardOpen(false);
    setReceiptTotal(0);
    setReceiptDescription('');
    setReceiptDate('');
    setItemizedLines([newLine()]);
    setScanning(false);
    setScanError(null);
    setScanMeta(null);
    // Clear both, or re-picking the same file fires no change event.
    if (cameraInputRef.current) {
      cameraInputRef.current.value = '';
    }
    if (libraryInputRef.current) {
      libraryInputRef.current.value = '';
    }
    if (resetMutation) {
      itemizeReceipt.reset();
    }
  };

  const cancelScan = () => {
    scanAbort.current?.abort();
    scanAbort.current = null;
    setScanning(false);
  };

  const handleScanFile = async (file: File | null | undefined) => {
    if (!file) return;
    setScanError(null);
    setScanMeta(null);
    // Clear before the request, not after it succeeds. Assigning only on success
    // left a failed or cancelled rescan showing the previous receipt's merchant,
    // total, date and lines -- all still committable as if they were this
    // receipt's. Starting from a clean slate means a failure shows an empty form
    // and an error, which is recoverable; committing receipt A's total against
    // receipt B's items is not.
    setReceiptDescription('');
    setReceiptTotal(0);
    setReceiptDate('');
    setItemizedLines([newLine()]);
    setScanEstimateMs(estimateMs());
    setScanning(true);
    const startedAt = Date.now();
    const controller = new AbortController();
    scanAbort.current = controller;
    try {
      // Applies EXIF orientation and caps the upload size. The server does the
      // document detection, crop and deskew that make extraction reliable.
      const normalized = await normalizeReceiptImage(file);
      const form = new FormData();
      form.append('image', normalized.blob, 'receipt.jpg');
      const scan = await request<ScanResponse>('/api/v1/receipts/scan', {
        method: 'POST',
        body: form,
        signal: controller.signal
      });

      const scanned: ItemizedLine[] = scan.items.map((item) => ({
        id: Math.random().toString(36).slice(2, 10),
        budgetId: item.suggested_budget_id ?? null,
        description: item.description,
        amount: fromCents(item.amount_cents),
        lineText: item.line_text,
        normKey: item.norm_key,
        marker: item.marker,
        taxable: item.taxable,
        taxCents: item.tax_cents,
        adjustCents: item.adjust_cents,
        suggested: item.suggested_budget_id !== null
      }));

      setReceiptDescription(scan.merchant || '');
      setReceiptTotal(scan.total_cents > 0 ? fromCents(scan.total_cents) : 0);
      setReceiptDate(toDateInput(scan.purchased_at));
      setItemizedLines(scanned.length ? scanned : [newLine()]);
      setScanMeta({
        reconciliation: scan.reconciliation,
        taxCents: scan.tax_cents,
        elapsedMs: scan.elapsed_ms
      });
      // Record the wall clock the user actually waited -- upload and image work
      // included -- since that is what the bar has to predict next time.
      recordSample(Date.now() - startedAt);
    } catch (err) {
      if ((err as Error).name === 'AbortError') {
        return;
      }
      // Never a dead end: whatever was captured stays editable by hand.
      setScanError((err as Error).message || 'Could not read that receipt.');
    } finally {
      setScanning(false);
      scanAbort.current = null;
      if (cameraInputRef.current) {
        cameraInputRef.current.value = '';
      }
      if (libraryInputRef.current) {
        libraryInputRef.current.value = '';
      }
    }
  };

  const startNewBudget = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const form = new FormData(e.currentTarget);
    const name = String(form.get('name') || '').trim();
    const payroll = Number(form.get('payroll') || 0);
    if (!name) return;
    createBudget.mutate({ name, payroll });
    setNewBudgetOpen(false);
  };

  const openTransactions = (budgetId: number) => {
    setExpanded((prev) => (prev === budgetId ? null : budgetId));
    setSearch('');
    setEditingTxnId(null);
    setEditingTxn(null);
    queryClient.removeQueries({ queryKey: ['transactions', budgetId], exact: true });
  };

  const confirmDeleteBudget = () => {
    const target = budgets.find((b) => b.id === settingsBudget);
    if (!target) return;
    const confirmed = window.confirm(`Delete budget "${target.name}"? This cannot be undone.`);
    if (confirmed) {
      setExpanded(null);
      setSettingsBudget(null);
      deleteBudget.mutate(target.id);
    }
  };

  return (
    <section className="card">
      <header className="card__header">
        <div>
          <p className="eyebrow">Overview</p>
          <h1>Budgets</h1>
        </div>
        <div className="actions">
          <button type="button" className="ghost" onClick={() => setItemizeWizardOpen(true)} disabled={!budgets.length}>
            🧾 Itemize receipt
          </button>
          <button type="button" className="ghost" onClick={() => setBalanceWizardOpen(true)}>
            ⚖ Balance wizard
          </button>
          <button
            type="button"
            className="ghost icon"
            aria-expanded={showFilter}
            aria-label={showFilter ? 'Hide filters' : 'Show filters'}
            onClick={() => setShowFilter((v) => !v)}
          >
            <svg
              aria-hidden
              xmlns="http://www.w3.org/2000/svg"
              width="18"
              height="18"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              strokeWidth="2"
              strokeLinecap="round"
              strokeLinejoin="round"
            >
              <polygon points="4 4 20 4 14 12 14 20 10 22 10 12 4 4" />
            </svg>
          </button>
        </div>
      </header>

      {budgetsQuery.isLoading && <p>Loading budgets...</p>}
      {budgetsQuery.error && <p className="error">Failed to load: {(budgetsQuery.error as Error).message}</p>}

      {showFilter && (
        <div className="panel" style={{ marginTop: 12 }}>
          <div className="card__header" style={{ marginBottom: 8 }}>
            <div>
              <p className="eyebrow">Search</p>
              <h2>Filter transactions</h2>
            </div>
          </div>
          <div className="grid" style={{ gridTemplateColumns: '2fr 1fr', gap: 12 }}>
            <input
              placeholder="Search description, amount, or budget"
              value={search}
              onChange={(e) => {
                setSearch(e.target.value);
                if (expanded) setExpanded(null);
              }}
            />
            <select value={expanded || ''} onChange={(e) => setExpanded(Number(e.target.value) || null)}>
              <option value="">Select budget</option>
              {budgets.map((b) => (
                <option key={b.id} value={b.id}>
                  {b.name}
                </option>
              ))}
            </select>
          </div>
        </div>
      )}

      <div className="accordion">
        {filteredBudgets.map((budget) => {
          const active = expanded === budget.id;
          const negative = budget.balance < 0;
          return (
            <div
              key={budget.id}
              className={`panel ${active ? 'panel--active' : ''} ${negative ? 'panel--negative' : ''}`}
              onClick={() => openTransactions(budget.id)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' || e.key === ' ') {
                  e.preventDefault();
                  openTransactions(budget.id);
                }
              }}
              role="button"
              tabIndex={0}
              aria-expanded={active}
            >
              <div className="accordion__header">
                <div className="accordion__title">
                  <h2>{budget.name}</h2>
                  <p className={`muted ${negative ? 'negative' : ''}`}>{formatHeadingBalance(budget.balance)}</p>
                  {active && (
                    <p className="muted">
                      Net {formatNumber(budget.balance)} · Payroll {formatNumber(budget.payroll)} · Credits{' '}
                      {formatNumber(budget.credits)} · Debits {formatNumber(budget.debits)}
                    </p>
                  )}
                </div>
                <div className="actions">
                  <button
                    type="button"
                    className="icon ghost"
                    aria-label={`Open settings for ${budget.name}`}
                    onClick={(e) => {
                      e.stopPropagation();
                      setSettingsBudget(budget.id);
                    }}
                  >
                    ⚙
                  </button>
                  <button
                    type="button"
                    className="icon"
                    aria-label={`Add transaction to ${budget.name}`}
                    onClick={(e) => {
                      e.stopPropagation();
                      setNewTxnBudget(budget.id);
                    }}
                  >
                    +
                  </button>
                </div>
              </div>
              {active && (
                <div className="txn-list" style={{ marginTop: 12 }}>
                  {transactionsQuery.isLoading && <p>Loading transactions...</p>}
                  {transactionsQuery.error && (
                    <p className="error">Failed to load: {(transactionsQuery.error as Error).message}</p>
                  )}
                  {(() => {
                    const term = search.trim().toLowerCase();
                    const flattened = (transactionsQuery.data?.pages ?? [])
                      .flatMap((page) => page?.data ?? [])
                      .filter((txn): txn is Transaction => Boolean(txn));
                    const filtered = term
                      ? flattened.filter((txn) => {
                          if (txn.description.toLowerCase().includes(term)) return true;
                          const amountTerm = term.replace(/[^0-9.-]/g, '');
                          if (!amountTerm) return false;
                          const formatted = formatNumber(txn.amount);
                          return (
                            formatted.includes(amountTerm) ||
                            formatted.replace(/,/g, '').includes(amountTerm)
                          );
                        })
                      : flattened;
                    if (filtered.length === 0 && !transactionsQuery.isLoading && !transactionsQuery.error) {
                      return <p className="muted">No transactions.</p>;
                    }
                    return filtered.map((txn) => {
                      const isEditing = editingTxnId === txn.id;
                      return (
                        <div
                          key={txn.id}
                          className={`txn ${isEditing ? 'txn--editing' : ''} ${txn.credit ? 'txn--credit' : 'txn--debit'}`}
                          onClick={(e) => {
                            e.stopPropagation();
                            setEditingTxnId(txn.id);
                            setEditingTxn({ description: txn.description, credit: txn.credit, amount: txn.amount });
                          }}
                        >
                          {isEditing && editingTxn ? (
                            <>
                              <div className="txn__fields">
                                <label>
                                  Description
                                  <input
                                    value={editingTxn.description}
                                    onChange={(e) =>
                                      setEditingTxn((prev) =>
                                        prev ? { ...prev, description: e.target.value } : prev
                                      )
                                    }
                                  />
                                </label>
                                <div className="grid" style={{ gridTemplateColumns: '1fr 1fr', marginTop: 8 }}>
                                  <label className="inline">
                                    <input
                                      type="checkbox"
                                      checked={editingTxn.credit}
                                      onChange={(e) =>
                                        setEditingTxn((prev) => (prev ? { ...prev, credit: e.target.checked } : prev))
                                      }
                                    />
                                    Credit
                                  </label>
                                  <label>
                                    Amount
                                    <input
                                      type="number"
                                      inputMode="decimal"
                                      step="0.01"
                                      min={0.01}
                                      value={editingTxn.amount}
                                      onFocus={selectOnFocus}
                                      onChange={(e) =>
                                        setEditingTxn((prev) =>
                                          prev ? { ...prev, amount: Number(e.target.value) } : prev
                                        )
                                      }
                                    />
                                  </label>
                                </div>
                              </div>
                              <div className="txn__actions">
                                <button
                                  type="button"
                                  className="secondary button--sm"
                                  aria-label="Cancel edit"
                                  onClick={(e) => {
                                    e.stopPropagation();
                                    setEditingTxn(null);
                                    setEditingTxnId(null);
                                  }}
                                >
                                  Cancel
                                </button>
                                <button
                                  type="button"
                                  className="button--sm"
                                  aria-label="Save transaction"
                                  disabled={updateTransaction.isPending}
                                  onClick={(e) => {
                                    e.stopPropagation();
                                    if (!editingTxn) return;
                                    updateTransaction.mutate({
                                      budgetId: budget.id,
                                      txnId: txn.id,
                                      description: editingTxn.description,
                                      credit: editingTxn.credit,
                                      amount: editingTxn.amount
                                    });
                                  }}
                                >
                                  {updateTransaction.isPending ? 'Saving…' : 'Save'}
                                </button>
                                <button
                                  type="button"
                                  className="danger button--sm"
                                  aria-label="Delete transaction"
                                  disabled={deleteTransaction.isPending}
                                  onClick={(e) => {
                                    e.stopPropagation();
                                    deleteTransaction.mutate({ budgetId: budget.id, txnId: txn.id });
                                  }}
                                >
                                  {deleteTransaction.isPending ? 'Deleting…' : 'Delete'}
                                </button>
                              </div>
                              {updateTransaction.error && (
                                <p className="error">{(updateTransaction.error as Error).message}</p>
                              )}
                              {deleteTransaction.error && (
                                <p className="error">{(deleteTransaction.error as Error).message}</p>
                              )}
                            </>
                          ) : (
                            <>
                              <div>
                                <p className="eyebrow">{txn.credit ? 'Credit' : 'Debit'}</p>
                                <p>{txn.description}</p>
                                <p className="muted">{new Date(txn.created_at).toLocaleString()}</p>
                              </div>
                              <div className={`amount ${txn.credit ? 'positive' : 'negative'}`}>
                                {txn.credit ? '+' : '-'}
                                {formatNumber(txn.amount)}
                              </div>
                            </>
                          )}
                        </div>
                      );
                    });
                  })()}
                  {transactionsQuery.hasNextPage && (
                    <button
                      type="button"
                      className="secondary"
                      onClick={(e) => {
                        e.stopPropagation();
                        transactionsQuery.fetchNextPage();
                      }}
                      disabled={transactionsQuery.isFetchingNextPage}
                    >
                      {transactionsQuery.isFetchingNextPage ? 'Loading…' : 'Load more'}
                    </button>
                  )}
                </div>
              )}
            </div>
          );
        })}
      </div>

      {itemizeWizardOpen && (
        <ModalPortal>
          <div className="modal">
            <div className="modal__content modal__content--wide">
            <div className="card__header">
              <div>
                <p className="eyebrow">Receipt helper</p>
                <h2>Itemize receipt</h2>
                <p className="muted">
                  {/* Do not advertise scanning when the server has no inference
                      endpoint configured; there would be no button to press. */}
                  {scanEnabled
                    ? 'Take or choose a photo of a receipt to fill this in, or start with the total and assign the line items you know. Anything left over drops into a catch-all budget.'
                    : 'Start with the receipt total and assign the line items you know. Anything left over drops into a catch-all budget.'}
                </p>
              </div>
              <div className="actions" style={{ gap: 8, alignItems: 'center' }}>
                {scanEnabled && (
                  <>
                    <input
                      ref={cameraInputRef}
                      type="file"
                      accept="image/*"
                      capture="environment"
                      style={{ display: 'none' }}
                      data-testid="receipt-camera-input"
                      onChange={(e) => handleScanFile(e.target.files?.[0])}
                    />
                    {/* No capture attribute, so this opens the photo library or
                        file picker instead of the camera. accept="image/*" still
                        matches HEIC, which the canvas step re-encodes to JPEG
                        before upload -- library photos on an iPhone are usually
                        HEIC, which the server cannot decode on its own. */}
                    <input
                      ref={libraryInputRef}
                      type="file"
                      accept="image/*"
                      style={{ display: 'none' }}
                      data-testid="receipt-library-input"
                      onChange={(e) => handleScanFile(e.target.files?.[0])}
                    />
                    {scanning ? (
                      <button type="button" className="secondary button--sm" onClick={cancelScan}>
                        Cancel scan
                      </button>
                    ) : (
                      <>
                        <button
                          type="button"
                          className="secondary button--sm"
                          onClick={() => cameraInputRef.current?.click()}
                          disabled={!budgets.length}
                        >
                          📸 Take photo
                        </button>
                        <button
                          type="button"
                          className="secondary button--sm"
                          onClick={() => libraryInputRef.current?.click()}
                          disabled={!budgets.length}
                        >
                          🖼 Choose photo
                        </button>
                      </>
                    )}
                  </>
                )}
                <button
                  type="button"
                  className="icon ghost"
                  aria-label="Close itemize wizard"
                  onClick={() => resetItemizeWizard()}
                >
                  ✖
                </button>
              </div>
            </div>

            {!budgets.length ? (
              <p className="error">Create a budget first to itemize a receipt.</p>
            ) : (
              <>
                {scanning && (
                  <div className="panel" style={{ marginBottom: 12, padding: 12 }}>
                    <p className="eyebrow" style={{ marginTop: 0 }}>
                      Reading the receipt
                    </p>
                    <progress
                      value={progressFraction(scanElapsedMs, scanEstimateMs)}
                      max={1}
                      aria-label="Receipt scan progress"
                      style={{ width: '100%' }}
                    />
                    <p className="muted" style={{ marginBottom: 0 }} aria-live="polite">
                      {progressLabel(scanElapsedMs, scanEstimateMs)}
                    </p>
                  </div>
                )}
                {scanError && (
                  <p className="error">
                    {scanError} You can still enter the items by hand below.
                  </p>
                )}
                {scanMeta && !scanMeta.reconciliation.ok && (
                  <p className="error">
                    {scanMeta.reconciliation.message || 'The scanned amounts do not add up.'} Check the highlighted
                    amounts before saving.
                  </p>
                )}
                {scanMeta && scanMeta.reconciliation.ok && (
                  <div className="badge" style={{ marginBottom: 12 }}>
                    Scanned and balanced — {itemizedLines.length} item
                    {itemizedLines.length === 1 ? '' : 's'}, {formatNumber(fromCents(scanMeta.taxCents))} tax spread
                    across them.
                  </div>
                )}
                <div className="grid" style={{ gridTemplateColumns: 'repeat(auto-fit, minmax(240px, 1fr))', gap: 12 }}>
                  <label>
                    Receipt description
                    <input
                      value={receiptDescription}
                      onChange={(e) => setReceiptDescription(e.target.value)}
                      placeholder="Store or memo"
                    />
                  </label>
                  <label>
                    Receipt total
                    <input
                      type="number"
                      inputMode="decimal"
                      step="0.01"
                      min={0.01}
                      value={receiptTotal}
                      onFocus={selectOnFocus}
                      onChange={(e) => setReceiptTotal(Number(e.target.value))}
                      required
                    />
                  </label>
                  <label>
                    Receipt date
                    <input
                      type="date"
                      value={receiptDate}
                      onChange={(e) => setReceiptDate(e.target.value)}
                    />
                  </label>
                  <label>
                    Catch-all budget
                    <select
                      value={catchAllBudgetId ?? ''}
                      onChange={(e) => setCatchAllBudgetId(Number(e.target.value) || null)}
                      required
                    >
                      {budgets.map((option) => (
                        <option key={option.id} value={option.id}>
                          {option.name}
                        </option>
                      ))}
                    </select>
                  </label>
                </div>

                <div className="panel" style={{ marginTop: 12 }}>
                  <div className="card__header">
                    <div>
                      <p className="eyebrow">Line items</p>
                      <h3>Distribute the receipt</h3>
                      <p className="muted">
                        Add items tied to specific budgets. Anything unallocated flows into the catch-all budget below.
                      </p>
                    </div>
                    <button type="button" className="secondary button--sm" onClick={addItemLine}>
                      + Add line
                    </button>
                  </div>
                  <div className="grid" style={{ gap: 12 }}>
                    {itemizedLines.map((line) => (
                      <div
                        key={line.id}
                        className="panel"
                        style={{
                          padding: 12,
                          display: 'grid',
                          gap: 8,
                          gridTemplateColumns: 'repeat(auto-fit, minmax(180px, 1fr))',
                          alignItems: 'end'
                        }}
                      >
                        <label>
                          Budget {line.suggested && <span className="eyebrow">suggested</span>}
                          <select
                            value={line.budgetId ?? ''}
                            onChange={(e) =>
                              updateItemLine(line.id, { budgetId: Number(e.target.value) || null, suggested: false })
                            }
                          >
                            {/* Naming the catch-all here is the point: a blank
                                option read as "nothing chosen yet", so every row
                                looked like it needed setting. */}
                            <option value="">
                              {catchAllBudget ? `${catchAllBudget.name} (default)` : 'Select'}
                            </option>
                            {budgets.map((option) => (
                              <option key={option.id} value={option.id}>
                                {option.name}
                              </option>
                            ))}
                          </select>
                        </label>
                        <label>
                          Item label
                          <input
                            value={line.description}
                            onChange={(e) => updateItemLine(line.id, { description: e.target.value })}
                            placeholder="(optional) e.g., groceries"
                          />
                        </label>
                        <label>
                          Amount
                          <input
                            type="number"
                            inputMode="decimal"
                            step="0.01"
                            min={0.01}
                            value={line.amount}
                            onFocus={selectOnFocus}
                            onChange={(e) => updateItemLine(line.id, { amount: Number(e.target.value) })}
                          />
                          {(line.taxCents || 0) > 0 && (
                            <span className="muted">
                              +{formatNumber(fromCents(line.taxCents || 0))} tax ={' '}
                              {formatNumber(fromCents(lineTotalCents(line)))}
                            </span>
                          )}
                        </label>
                        <div className="actions" style={{ justifyContent: 'flex-end' }}>
                          <button
                            type="button"
                            className="icon ghost"
                            aria-label="Remove line"
                            onClick={() => removeItemLine(line.id)}
                            disabled={itemizedLines.length <= 1}
                          >
                            ✖
                          </button>
                        </div>
                      </div>
                    ))}
                  </div>
                </div>

                <div className="panel" style={{ marginTop: 12 }}>
                  <div className="card__header">
                    <div>
                      <p className="eyebrow">Remainder</p>
                      <h3>Catch-all allocation</h3>
                      <p className="muted">
                        Allocated {formatNumber(allocatedTotal)} of {formatNumber(receiptTotal)}. Remaining goes to{' '}
                        {catchAllBudget?.name || 'the catch-all'}.
                      </p>
                    </div>
                  </div>
                  {overAllocated ? (
                    <p className="error">
                      Allocations exceed the receipt by {formatNumber(Math.abs(itemizeRemainder))}. Trim a line item to
                      continue.
                    </p>
                  ) : (
                    <div className="badge">
                      Remainder {formatNumber(Math.max(itemizeRemainder, 0))} -&gt;{' '}
                      {catchAllBudget?.name || 'select a budget'}
                    </div>
                  )}
                </div>

                <div className="modal__footer">
                  <button type="button" className="secondary" onClick={() => resetItemizeWizard()}>
                    Cancel
                  </button>
                  <button
                    type="button"
                    onClick={() => {
                      if (!catchAllBudgetId) return;
                      itemizeReceipt.mutate({
                        description: receiptDescription,
                        date: receiptDate,
                        total: receiptTotal,
                        catchAllBudgetId,
                        lines: itemizedLines,
                        reconciled: scanMeta ? scanMeta.reconciliation.ok : true
                      });
                    }}
                    disabled={!itemizeReady || itemizeReceipt.isPending || scanning}
                  >
                    {itemizeReceipt.isPending ? 'Saving…' : 'Save itemized receipt'}
                  </button>
                </div>
                {itemizeReceipt.error && <p className="error">{(itemizeReceipt.error as Error).message}</p>}
              </>
            )}
            </div>
          </div>
        </ModalPortal>
      )}

      {newTxnBudget && (
        <ModalPortal>
          <div className="modal">
            <div className="modal__content">
            <div className="card__header">
              <div>
                <p className="eyebrow">New transaction</p>
                <h2>{budgets.find((b) => b.id === newTxnBudget)?.name}</h2>
              </div>
              <button
                type="button"
                className="icon ghost"
                aria-label="Close new transaction dialog"
                onClick={() => setNewTxnBudget(null)}
              >
                ✖
              </button>
            </div>
            <form
              className="form"
              onSubmit={(e) => {
                e.preventDefault();
                if (!newTxnBudget) return;
                createTransaction.mutate({ sourceBudgetId: newTxnBudget, txn: newTxn });
              }}
            >
              <label>
                Amount
                <input
                  type="number"
                  inputMode="decimal"
                  step="0.01"
                  min={0.01}
                  value={newTxn.amount}
                  onFocus={selectOnFocus}
                  onChange={(e) => setNewTxn((prev) => ({ ...prev, amount: Number(e.target.value) }))}
                  required
                />
              </label>
              <label>
                Description
                <input
                  value={newTxn.description}
                  onChange={(e) => setNewTxn((prev) => ({ ...prev, description: e.target.value }))}
                  required
                />
              </label>
              <label className={`toggle ${transferDisabled ? 'toggle--disabled' : ''}`}>
                <div className="toggle__text">
                  <span className="toggle__label">Transfer</span>
                  <span className="toggle__hint">Debit this budget and credit another with the same amount.</span>
                </div>
                <span className="toggle__control">
                  <input
                    type="checkbox"
                    checked={newTxn.transfer}
                    onChange={(e) =>
                      setNewTxn((prev) => {
                        if (transferDisabled && !prev.transfer && e.target.checked) return prev;
                        const checked = e.target.checked;
                        const existingValid = transferOptions.some((b) => b.id === prev.transferBudgetId);
                        const fallback = transferOptions[0]?.id ?? null;
                        return {
                          ...prev,
                          transfer: checked,
                          credit: checked ? false : prev.credit,
                          transferBudgetId: checked ? (existingValid ? prev.transferBudgetId : fallback) : null
                        };
                      })
                    }
                    disabled={!newTxn.transfer && transferDisabled}
                  />
                  <span className="toggle__track">
                    <span className="toggle__thumb" />
                  </span>
                </span>
              </label>
              <div className="swap-slot">
                {!newTxn.transfer ? (
                  <label className="toggle">
                    <div className="toggle__text">
                      <span className="toggle__label">Treat as credit</span>
                      <span className="toggle__hint">Add this amount to the selected budget.</span>
                    </div>
                    <span className="toggle__control">
                      <input
                        type="checkbox"
                        checked={newTxn.credit}
                        onChange={(e) => setNewTxn((prev) => ({ ...prev, credit: e.target.checked }))}
                      />
                      <span className="toggle__track">
                        <span className="toggle__thumb" />
                      </span>
                    </span>
                  </label>
                ) : (
                  <label>
                    Transfer into
                    <select
                      className="select--tall"
                      value={newTxn.transferBudgetId ?? ''}
                      onChange={(e) =>
                        setNewTxn((prev) => ({ ...prev, transferBudgetId: Number(e.target.value) || null }))
                      }
                      required
                    >
                      <option value="" disabled>
                        Select budget
                      </option>
                      {transferOptions.map((option) => (
                        <option key={option.id} value={option.id}>
                          {option.name}
                        </option>
                      ))}
                    </select>
                    {transferDisabled && <p className="muted">Create another budget to enable transfers.</p>}
                  </label>
                )}
              </div>
              <button type="submit" disabled={createTransaction.isPending || !transferReady}>
                {createTransaction.isPending ? 'Saving…' : 'Save transaction'}
              </button>
              {createTransaction.error && <p className="error">{(createTransaction.error as Error).message}</p>}
            </form>
            </div>
          </div>
        </ModalPortal>
      )}

      {newBudgetOpen && (
        <ModalPortal>
          <div className="modal">
            <div className="modal__content">
            <div className="card__header">
              <div>
                <p className="eyebrow">New budget</p>
                <h2>Create</h2>
              </div>
              <button
                type="button"
                className="icon ghost"
                aria-label="Close new budget dialog"
                onClick={() => setNewBudgetOpen(false)}
              >
                ✖
              </button>
            </div>
            <form className="form" onSubmit={startNewBudget}>
              <label>
                Name
                <input name="name" placeholder="Name" required />
              </label>
              <label>
                Payroll
                <input
                  name="payroll"
                  type="number"
                  inputMode="decimal"
                  step="0.01"
                  min={0}
                  placeholder="Payroll"
                  onFocus={selectOnFocus}
                />
              </label>
              <button type="submit" disabled={createBudget.isPending}>
                {createBudget.isPending ? 'Creating…' : 'Create budget'}
              </button>
              {createBudget.error && <p className="error">{(createBudget.error as Error).message}</p>}
            </form>
            </div>
          </div>
        </ModalPortal>
      )}

      {settingsBudget && (
        <ModalPortal>
          <div className="modal">
            <div className="modal__content">
            <div className="card__header">
              <div>
                <p className="eyebrow">Settings</p>
                <h2>{budgets.find((b) => b.id === settingsBudget)?.name}</h2>
              </div>
              <button
                type="button"
                className="icon ghost"
                aria-label="Close settings dialog"
                onClick={() => setSettingsBudget(null)}
              >
                ✖
              </button>
            </div>
            <div className="form">
              <label>
                Name
                <input
                  value={editingBudgetName}
                  onChange={(e) => setEditingBudgetName(e.target.value)}
                  placeholder="Budget name"
                  required
                />
              </label>
              <label>
                Payroll
                <input
                  type="number"
                  inputMode="decimal"
                  step="0.01"
                  min={0}
                  value={
                    payrollEdit !== null
                      ? payrollEdit
                      : budgets.find((b) => b.id === settingsBudget)?.payroll || 0
                  }
                  onFocus={selectOnFocus}
                  onChange={(e) => setPayrollEdit(Number(e.target.value))}
                />
              </label>
            </div>
            <div style={{ marginTop: 16 }}>
              <p className="eyebrow">Auto-balance</p>
              {autoBalanceQuery.isLoading && <p>Loading…</p>}
                {autoBalanceQuery.error && <p className="error">{(autoBalanceQuery.error as Error).message}</p>}
              <label className={`toggle ${autoBalanceQuery.isLoading ? 'toggle--disabled' : ''}`}>
                <div className="toggle__text">
                  <span className="toggle__label">Auto-balance before payroll</span>
                  <span className="toggle__hint">
                    Split this budget's deficit across selected budgets before payroll runs.
                  </span>
                </div>
                <span className="toggle__control">
                  <input
                    type="checkbox"
                    checked={autoBalanceEnabled}
                    onChange={(e) => setAutoBalanceEnabled(e.target.checked)}
                    disabled={autoBalanceQuery.isLoading}
                  />
                  <span className="toggle__track">
                    <span className="toggle__thumb" />
                  </span>
                </span>
              </label>
              <div
                className={`collapse collapse--scroll ${autoBalanceEnabled ? 'collapse--open' : ''}`}
                style={{ marginTop: 12 }}
              >
                {autoBalanceOptions.length === 0 ? (
                  <p className="muted">No other budgets available.</p>
                ) : (
                  <>
                    <div className="auto-balance-list">
                      {autoBalanceOptions.map((budget) => (
                        <label key={budget.id} className="toggle">
                          <div className="toggle__text">
                            <span className="toggle__label">{budget.name}</span>
                            <span className={`toggle__hint ${budget.balance < 0 ? 'negative' : ''}`}>
                              Balance {formatHeadingBalance(budget.balance)}
                            </span>
                          </div>
                          <span className="toggle__control">
                            <input
                              type="checkbox"
                              checked={autoBalanceSelections[budget.id] ?? false}
                              onChange={(e) =>
                                setAutoBalanceSelections((prev) => ({
                                  ...prev,
                                  [budget.id]: e.target.checked
                                }))
                              }
                            />
                            <span className="toggle__track">
                              <span className="toggle__thumb" />
                            </span>
                          </span>
                        </label>
                      ))}
                    </div>
                    <p className="muted">
                      Split evenly across {autoBalanceSelectionCount} budget
                      {autoBalanceSelectionCount === 1 ? '' : 's'}.
                    </p>
                  </>
                )}
              </div>
            </div>
            <div style={{ marginTop: 16 }}>
              <p className="eyebrow">Shared with</p>
              {sharesQuery.isLoading && <p>Loading…</p>}
              {sharesQuery.error && <p className="error">{(sharesQuery.error as Error).message}</p>}
              <div className="share-list">
                {sharesQuery.data?.data.map((share) => (
                  <div key={share.id} className="share-item">
                    <span>{share.email}</span>
                    <button
                      type="button"
                      className="icon ghost"
                      onClick={() =>
                        settingsBudget && removeShare.mutate({ budgetId: settingsBudget, email: share.email })
                      }
                    >
                      ✖
                    </button>
                  </div>
                ))}
                {sharesQuery.data?.data.length === 0 && <p className="muted">No shares.</p>}
              </div>
              <form
                className="form"
                onSubmit={(e) => {
                  e.preventDefault();
                  if (settingsBudget && shareEmail) {
                    addShare.mutate({ budgetId: settingsBudget, email: shareEmail });
                  }
                }}
              >
                <label>
                  Add email
                  <input
                    type="email"
                    value={shareEmail}
                    onChange={(e) => setShareEmail(e.target.value)}
                    placeholder="user@example.com"
                  />
                </label>
                <button type="submit" disabled={addShare.isPending}>
                  {addShare.isPending ? 'Adding…' : 'Add'}
                </button>
              </form>
            </div>
            <div className="modal__footer">
              <button
                type="button"
                className="danger"
                aria-label="Delete budget"
                onClick={confirmDeleteBudget}
                disabled={deleteBudget.isPending}
              >
                🗑 Delete
              </button>
              <button
                type="button"
                className="secondary"
                onClick={() => settingsBudget && runPayroll.mutate(settingsBudget)}
                disabled={runPayroll.isPending || !settingsBudget}
              >
                {runPayroll.isPending ? 'Running…' : 'Run payroll'}
              </button>
              <button
                type="button"
                onClick={() => {
                  const target = budgets.find((b) => b.id === settingsBudget);
                  if (!target || !editingBudgetName.trim()) return;
                  updateBudgetSettings.mutate({
                    budgetId: target.id,
                    name: editingBudgetName.trim(),
                    payroll: payrollEdit ?? target.payroll,
                    autoBalanceEnabled,
                    autoBalanceSelections
                  });
                }}
                disabled={updateBudgetSettings.isPending || autoBalanceQuery.isLoading || !editingBudgetName.trim()}
              >
                {updateBudgetSettings.isPending ? 'Saving…' : 'Save'}
              </button>
            </div>
            {updateBudgetSettings.error && <p className="error">{(updateBudgetSettings.error as Error).message}</p>}
            {runPayroll.error && <p className="error">{(runPayroll.error as Error).message}</p>}
            {runPayrollResult !== null && (
              <p className="muted">Created {runPayrollResult} payroll transaction{runPayrollResult === 1 ? '' : 's'}.</p>
            )}
            </div>
          </div>
        </ModalPortal>
      )}

      {balanceWizardOpen && (
        <ModalPortal>
          <div className="modal">
            <div className="modal__content modal__content--wide">
            <div className="card__header">
              <div>
                <p className="eyebrow">Month-end helper</p>
                <h2>Balance wizard</h2>
                <p className="muted">
                  Pick negative budgets to bring to zero and the positive budgets that will fund them. The transfer pulls
                  evenly from positives.
                </p>
              </div>
              <button
                type="button"
                className="icon ghost"
                aria-label="Close balance wizard"
                onClick={() => {
                  setBalanceWizardOpen(false);
                  setSelectedNegatives([]);
                  setSelectedPositives([]);
                }}
              >
                ✖
              </button>
            </div>

            <div className="grid" style={{ gridTemplateColumns: 'repeat(auto-fit, minmax(260px, 1fr))', gap: 12 }}>
              <div className="panel">
                <div className="card__header" style={{ marginBottom: 8 }}>
                  <div>
                    <p className="eyebrow">Step 1</p>
                    <h3>Negative budgets to fix</h3>
                  </div>
                </div>
                {negativeBudgets.length === 0 && <p className="muted">No negative balances right now.</p>}
                <div className="grid" style={{ gap: 8 }}>
                  {negativeBudgets.map((budget) => (
                    <label key={budget.id} className="toggle">
                      <div className="toggle__text">
                        <span className="toggle__label">{budget.name}</span>
                        <span className="toggle__hint">Balance {formatNumber(budget.balance)}</span>
                      </div>
                      <span className="toggle__control">
                        <input
                          type="checkbox"
                          checked={selectedNegatives.includes(budget.id)}
                          onChange={(e) =>
                            setSelectedNegatives((prev) =>
                              e.target.checked ? [...prev, budget.id] : prev.filter((id) => id !== budget.id)
                            )
                          }
                        />
                        <span className="toggle__track">
                          <span className="toggle__thumb" />
                        </span>
                      </span>
                    </label>
                  ))}
                </div>
              </div>

              <div className="panel">
                <div className="card__header" style={{ marginBottom: 8 }}>
                  <div>
                    <p className="eyebrow">Step 2</p>
                    <h3>Positive budgets to draw from</h3>
                  </div>
                </div>
                {positiveBudgets.length === 0 && <p className="muted">No positive balances to use.</p>}
                <div className="grid" style={{ gap: 8 }}>
                  {positiveBudgets.map((budget) => (
                    <label key={budget.id} className="toggle">
                      <div className="toggle__text">
                        <span className="toggle__label">{budget.name}</span>
                        <span className="toggle__hint">Balance {formatNumber(budget.balance)}</span>
                      </div>
                      <span className="toggle__control">
                        <input
                          type="checkbox"
                          checked={selectedPositives.includes(budget.id)}
                          onChange={(e) =>
                            setSelectedPositives((prev) =>
                              e.target.checked ? [...prev, budget.id] : prev.filter((id) => id !== budget.id)
                            )
                          }
                        />
                        <span className="toggle__track">
                          <span className="toggle__thumb" />
                        </span>
                      </span>
                    </label>
                  ))}
                </div>
              </div>
            </div>

              <div className="panel" style={{ marginTop: 12 }}>
                <div className="card__header">
                  <div>
                    <p className="eyebrow">Step 3</p>
                  <h3>Review distribution</h3>
                </div>
                <p className="muted">
                  Total to cover: {formatNumber(totalDeficit)} · Per positive:{' '}
                  {positiveAllocation[0] ? formatNumber(positiveAllocation[0]) : '0.00'}
                </p>
              </div>
              {coverageShortfall && (
                <p className="error">
                  Selected positive budgets only cover {formatNumber(positiveCoverage)} of {formatNumber(totalDeficit)}.
                  They will dip below zero.
                </p>
              )}
              {wizardReady ? (
                <div className="grid" style={{ gridTemplateColumns: 'repeat(auto-fit, minmax(260px, 1fr))', gap: 8 }}>
                  <div>
                    <p className="eyebrow">Credits applied</p>
                    {selectedNegatives.map((id) => {
                      const budget = budgets.find((b) => b.id === id);
                      if (!budget) return null;
                      return (
                        <p key={id}>
                          {budget.name}: +{formatNumber(Math.abs(budget.balance))}
                        </p>
                      );
                    })}
                  </div>
                  <div>
                    <p className="eyebrow">Debits applied</p>
                    {selectedPositives.map((id, idx) => {
                      const budget = budgets.find((b) => b.id === id);
                      if (!budget) return null;
                      return (
                        <p key={id}>
                          {budget.name}: -{positiveAllocation[idx] ? formatNumber(positiveAllocation[idx]) : '0.00'}
                        </p>
                      );
                    })}
                  </div>
                </div>
              ) : (
                <p className="muted">Select at least one negative and one positive budget to preview.</p>
              )}

              <div className="modal__footer">
                <button
                  type="button"
                  onClick={() =>
                    balanceBudgets.mutate({ negativeIds: selectedNegatives, positiveIds: selectedPositives })
                  }
                  disabled={!wizardReady || balanceBudgets.isPending}
                >
                  {balanceBudgets.isPending ? 'Balancing…' : 'Balance now'}
                </button>
              </div>
              {balanceBudgets.error && <p className="error">{(balanceBudgets.error as Error).message}</p>}
            </div>
            </div>
          </div>
        </ModalPortal>
      )}
    </section>
  );
};

export default Dashboard;
