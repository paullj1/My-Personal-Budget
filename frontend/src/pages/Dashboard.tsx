import { FormEvent, ReactNode, useEffect, useMemo, useState } from 'react';
import { createPortal } from 'react-dom';
import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient
} from '@tanstack/react-query';

import { request } from '../api/client';

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

type ItemizedLine = {
  id: string;
  budgetId: number | null;
  description: string;
  amount: number;
};

const INITIAL_TXN: NewTxnState = {
  description: '',
  credit: false,
  amount: 0,
  transfer: false,
  transferBudgetId: null
};

const PAGE_SIZE = 20;
const round2 = (value: number) => Math.round(value * 100) / 100;
const newLine = (): ItemizedLine => ({
  id: Math.random().toString(36).slice(2, 10),
  budgetId: null,
  description: '',
  amount: 0
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
  const [balanceWizardOpen, setBalanceWizardOpen] = useState(false);
  const [selectedNegatives, setSelectedNegatives] = useState<number[]>([]);
  const [selectedPositives, setSelectedPositives] = useState<number[]>([]);
  const [itemizeWizardOpen, setItemizeWizardOpen] = useState(false);
  const [receiptTotal, setReceiptTotal] = useState<number>(0);
  const [receiptDescription, setReceiptDescription] = useState('');
  const [catchAllBudgetId, setCatchAllBudgetId] = useState<number | null>(null);
  const [itemizedLines, setItemizedLines] = useState<ItemizedLine[]>([newLine()]);
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
  const budgets = budgetsQuery.data?.data || [];
  useEffect(() => {
    if (!settingsBudget) {
      setEditingBudgetName('');
      setPayrollEdit(null);
      return;
    }
    const target = budgets.find((b) => b.id === settingsBudget);
    setEditingBudgetName(target?.name ?? '');
    setPayrollEdit(target?.payroll ?? null);
  }, [settingsBudget, budgets]);
  useEffect(() => {
    if (budgets.length === 0) {
      setCatchAllBudgetId(null);
      return;
    }
    setItemizedLines((prev) =>
      prev.map((line) => {
        if (!line.budgetId) return line;
        return budgets.some((b) => b.id === line.budgetId) ? line : { ...line, budgetId: null };
      })
    );
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
  const allocatedTotal = useMemo(
    () =>
      round2(
        itemizedLines.reduce((sum, line) => {
          if (!Number.isFinite(line.amount)) return sum;
          return sum + (line.amount || 0);
        }, 0)
      ),
    [itemizedLines]
  );
  const itemizeRemainder = useMemo(() => round2(receiptTotal - allocatedTotal), [receiptTotal, allocatedTotal]);
  const overAllocated = itemizeRemainder < -0.009;
  const activeItemLines = useMemo(
    () => itemizedLines.filter((line) => line.budgetId !== null && line.amount > 0),
    [itemizedLines]
  );
  const catchAllBudget = useMemo(
    () => budgets.find((b) => b.id === catchAllBudgetId) || null,
    [budgets, catchAllBudgetId]
  );
  const itemizeReady = receiptTotal > 0 && catchAllBudgetId !== null && activeItemLines.length > 0 && !overAllocated;

  useEffect(() => {
    setSelectedNegatives((prev) => prev.filter((id) => budgets.some((b) => b.id === id && b.balance < 0)));
    setSelectedPositives((prev) => prev.filter((id) => budgets.some((b) => b.id === id && b.balance > 0)));
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
      total: number;
      catchAllBudgetId: number;
      lines: ItemizedLine[];
    }) => {
      const baseDescription = payload.description.trim() || 'Itemized receipt';
      if (payload.total <= 0) {
        throw new Error('Total must be greater than zero.');
      }
      const lines = payload.lines
        .filter((line): line is ItemizedLine & { budgetId: number } => line.budgetId !== null && line.amount > 0)
        .map((line) => ({ ...line, description: line.description.trim() }));
      if (!lines.length) {
        throw new Error('Add at least one line item with a budget and amount.');
      }
      const allocated = round2(lines.reduce((sum, line) => sum + line.amount, 0));
      const remainder = round2(payload.total - allocated);
      if (remainder < -0.009) {
        throw new Error('Allocations exceed the receipt total.');
      }

      const touched = new Set<number>();
      for (const line of lines) {
        const desc = line.description ? `${baseDescription} - ${line.description}` : baseDescription;
        await request(`/api/v1/budgets/${line.budgetId}/transactions`, {
          method: 'POST',
          body: { description: desc, credit: false, amount: line.amount }
        });
        touched.add(line.budgetId);
      }
      if (remainder > 0.009) {
        await request(`/api/v1/budgets/${payload.catchAllBudgetId}/transactions`, {
          method: 'POST',
          body: { description: `${baseDescription} - catch-all`, credit: false, amount: remainder }
        });
        touched.add(payload.catchAllBudgetId);
      }
      return { budgetIds: Array.from(touched) };
    },
    onSuccess: (result) => {
      resetItemizeWizard(false);
      queryClient.invalidateQueries({ queryKey: ['budgets'] });
      result.budgetIds.forEach((id) => queryClient.invalidateQueries({ queryKey: ['transactions', id] }));
      itemizeReceipt.reset();
    }
  });

  const updatePayroll = useMutation({
    mutationFn: (payload: { budgetId: number; name: string; payroll: number }) =>
      request<Budget>(`/api/v1/budgets/${payload.budgetId}`, {
        method: 'PUT',
        body: { name: payload.name, payroll: payload.payroll }
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['budgets'] });
      setPayrollEdit(null);
      setSettingsBudget(null);
      setEditingBudgetName('');
    }
  });

  const deleteBudget = useMutation({
    mutationFn: (budgetId: number) => request(`/api/v1/budgets/${budgetId}`, { method: 'DELETE' }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['budgets'] });
      setExpanded(null);
      setSettingsBudget(null);
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
    setItemizeWizardOpen(false);
    setReceiptTotal(0);
    setReceiptDescription('');
    setItemizedLines([newLine()]);
    if (resetMutation) {
      itemizeReceipt.reset();
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
                  <p className="muted">{budget.balance.toFixed(2)}</p>
                  {active && (
                    <p className="muted">
                      Net {budget.balance.toFixed(2)} · Payroll {budget.payroll.toFixed(2)} · Credits{' '}
                      {budget.credits.toFixed(2)} · Debits {budget.debits.toFixed(2)}
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
                      ? flattened.filter(
                          (txn) =>
                            txn.description.toLowerCase().includes(term) ||
                            txn.amount.toFixed(2).includes(term.replace(/[^0-9.-]/g, ''))
                        )
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
                                {txn.amount.toFixed(2)}
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
                  Start with the receipt total, assign the line items you know, and drop the remainder into a catch-all budget automatically.
                </p>
              </div>
              <button
                type="button"
                className="icon ghost"
                aria-label="Close itemize wizard"
                onClick={() => resetItemizeWizard()}
              >
                ✖
              </button>
            </div>

            {!budgets.length ? (
              <p className="error">Create a budget first to itemize a receipt.</p>
            ) : (
              <>
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
                      step="0.01"
                      min={0.01}
                      value={receiptTotal}
                      onChange={(e) => setReceiptTotal(Number(e.target.value))}
                      required
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
                          Budget
                          <select
                            value={line.budgetId ?? ''}
                            onChange={(e) =>
                              updateItemLine(line.id, { budgetId: Number(e.target.value) || null })
                            }
                          >
                            <option value="">Select</option>
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
                            step="0.01"
                            min={0.01}
                            value={line.amount}
                            onChange={(e) => updateItemLine(line.id, { amount: Number(e.target.value) })}
                          />
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
                        Allocated {allocatedTotal.toFixed(2)} of {receiptTotal.toFixed(2)}. Remaining goes to{' '}
                        {catchAllBudget?.name || 'the catch-all'}.
                      </p>
                    </div>
                  </div>
                  {overAllocated ? (
                    <p className="error">
                      Allocations exceed the receipt by {Math.abs(itemizeRemainder).toFixed(2)}. Trim a line item to
                      continue.
                    </p>
                  ) : (
                    <div className="badge">
                      Remainder {Math.max(itemizeRemainder, 0).toFixed(2)} -&gt;{' '}
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
                        total: receiptTotal,
                        catchAllBudgetId,
                        lines: itemizedLines
                      });
                    }}
                    disabled={!itemizeReady || itemizeReceipt.isPending}
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
                Description
                <input
                  value={newTxn.description}
                  onChange={(e) => setNewTxn((prev) => ({ ...prev, description: e.target.value }))}
                  required
                />
              </label>
              <label>
                Amount
                <input
                  type="number"
                  inputMode="decimal"
                  step="0.01"
                  min={0.01}
                  value={newTxn.amount}
                  onChange={(e) => setNewTxn((prev) => ({ ...prev, amount: Number(e.target.value) }))}
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
                <input name="payroll" type="number" step="0.01" min={0} placeholder="Payroll" />
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
                  step="0.01"
                  min={0}
                  value={
                    payrollEdit !== null
                      ? payrollEdit
                      : budgets.find((b) => b.id === settingsBudget)?.payroll || 0
                  }
                  onChange={(e) => setPayrollEdit(Number(e.target.value))}
                />
              </label>
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
                onClick={() => {
                  const target = budgets.find((b) => b.id === settingsBudget);
                  if (!target || !editingBudgetName.trim()) return;
                  updatePayroll.mutate({
                    budgetId: target.id,
                    name: editingBudgetName.trim(),
                    payroll: payrollEdit ?? target.payroll
                  });
                }}
                disabled={updatePayroll.isPending || !editingBudgetName.trim()}
              >
                {updatePayroll.isPending ? 'Saving…' : 'Save'}
              </button>
            </div>
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
                        <span className="toggle__hint">Balance {budget.balance.toFixed(2)}</span>
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
                        <span className="toggle__hint">Balance {budget.balance.toFixed(2)}</span>
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
                  Total to cover: {totalDeficit.toFixed(2)} · Per positive: {positiveAllocation[0]?.toFixed(2) || '0.00'}
                </p>
              </div>
              {coverageShortfall && (
                <p className="error">
                  Selected positive budgets only cover {positiveCoverage.toFixed(2)} of {totalDeficit.toFixed(2)}. They will
                  dip below zero.
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
                          {budget.name}: +{Math.abs(budget.balance).toFixed(2)}
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
                          {budget.name}: -{positiveAllocation[idx]?.toFixed(2) || '0.00'}
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
