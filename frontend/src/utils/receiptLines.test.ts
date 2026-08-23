import { describe, expect, it } from 'vitest';

import {
  ItemizedLine,
  allocatedCents,
  buildReceiptCommit,
  fromCents,
  lineTotalCents,
  toCents,
  toDateInput
} from './receiptLines';

const line = (over: Partial<ItemizedLine> = {}): ItemizedLine => ({
  id: 'x',
  budgetId: 1,
  description: 'Item',
  amount: 0,
  taxCents: 0,
  adjustCents: 0,
  ...over
});

// The real Target receipt: 4 items, 424c of tax already prorated, 7490c total.
const targetLines = (): ItemizedLine[] => [
  line({ id: 'a', budgetId: 3, description: 'GATORADE', amount: 6.99, taxCents: 42, lineText: '203800178 GATORADE TF $6.99', normKey: 'GATORADE TF', marker: 'TF', taxable: true }),
  line({ id: 'b', budgetId: 3, description: 'Good&Gather', amount: 3.69, taxCents: 22, normKey: 'GOOD&GATHER TF', marker: 'TF', taxable: true }),
  line({ id: 'c', budgetId: 4, description: 'ELEC KETTLE', amount: 29.99, taxCents: 180, normKey: 'ELEC KETTLE T', marker: 'T', taxable: true }),
  line({ id: 'd', budgetId: 4, description: 'Bodum', amount: 29.99, taxCents: 180, normKey: 'BODUM T', marker: 'T', taxable: true })
];

describe('cents helpers', () => {
  it('converts without float drift', () => {
    expect(toCents(6.99)).toBe(699);
    expect(toCents(74.9)).toBe(7490);
    expect(toCents(0.1)).toBe(10);
    expect(toCents(29.99)).toBe(2999);
    expect(fromCents(7490)).toBe(74.9);
  });

  it('treats non-finite input as zero rather than NaN', () => {
    expect(toCents(NaN)).toBe(0);
    expect(toCents(Infinity)).toBe(0);
  });

  it('includes tax and discounts in a line total', () => {
    expect(lineTotalCents(line({ amount: 6.99, taxCents: 42 }))).toBe(741);
    expect(lineTotalCents(line({ amount: 29.99, taxCents: 180, adjustCents: -200 }))).toBe(2979);
    expect(lineTotalCents(line({ amount: 5 }))).toBe(500);
  });

  it('sums the scanned receipt to its printed total', () => {
    // The key invariant: a scanned receipt is fully allocated, so no phantom
    // remainder gets pushed into the catch-all.
    expect(allocatedCents(targetLines())).toBe(7490);
  });
});

describe('buildReceiptCommit', () => {
  it('commits a scanned receipt with no catch-all remainder', () => {
    const payload = buildReceiptCommit({
      description: 'Target Columbia',
      date: '2026-08-20',
      total: 74.9,
      catchAllBudgetId: 9,
      lines: targetLines(),
      reconciled: true
    });

    expect(payload.items).toHaveLength(4);
    expect(payload.merchant).toBe('Target Columbia');
    expect(payload.purchased_at).toBe('2026-08-20');
    expect(payload.reconciled).toBe(true);

    const total = payload.items.reduce(
      (sum, i) => sum + i.amount_cents + i.tax_cents + i.adjust_cents,
      0
    );
    expect(total).toBe(7490);
    // Scan metadata must survive to the server so suggestions keep learning.
    expect(payload.items[0].norm_key).toBe('GATORADE TF');
    expect(payload.items[0].marker).toBe('TF');
    expect(payload.items[0].taxable).toBe(true);
    expect(payload.items[0].tax_cents).toBe(42);
  });

  it('sends the unallocated remainder to the catch-all', () => {
    const payload = buildReceiptCommit({
      description: 'Corner store',
      date: '',
      total: 50,
      catchAllBudgetId: 9,
      lines: [line({ budgetId: 3, description: 'Coffee', amount: 20 })],
      reconciled: true
    });

    expect(payload.items).toHaveLength(2);
    const remainder = payload.items[1];
    expect(remainder.budget_id).toBe(9);
    expect(remainder.amount_cents).toBe(3000);
    expect(remainder.description).toBe('Unitemized remainder');
    expect(payload.purchased_at).toBeNull();
  });

  it('skips the remainder line when the receipt is fully allocated', () => {
    const payload = buildReceiptCommit({
      description: 'x',
      date: '',
      total: 20,
      catchAllBudgetId: 9,
      lines: [line({ budgetId: 3, amount: 20 })],
      reconciled: true
    });
    expect(payload.items).toHaveLength(1);
  });

  it('tolerates a one-cent rounding gap but rejects real over-allocation', () => {
    const build = (total: number) =>
      buildReceiptCommit({
        description: 'x',
        date: '',
        total,
        catchAllBudgetId: 9,
        lines: [line({ budgetId: 3, amount: 20 })],
        reconciled: true
      });
    expect(() => build(19.99)).not.toThrow();
    expect(() => build(15)).toThrow(/exceed/i);
  });

  it('sends unassigned lines to the catch-all individually, not as one blob', () => {
    const payload = buildReceiptCommit({
      description: 'x',
      date: '',
      total: 30,
      catchAllBudgetId: 9,
      lines: [
        line({ budgetId: 3, amount: 10, description: 'Batteries', normKey: 'BATTERIES' }),
        line({ budgetId: null, amount: 10, description: 'Milk', normKey: 'MILK' }),
        line({ budgetId: null, amount: 10, description: 'Bread', normKey: 'BREAD' })
      ],
      reconciled: true
    });

    // Three real lines and no remainder: the unassigned two are committed on their
    // own, so nothing is left over to lump together.
    expect(payload.items).toHaveLength(3);
    expect(payload.items.map((i) => i.budget_id)).toEqual([3, 9, 9]);
    expect(payload.items.map((i) => i.description)).toEqual(['Batteries', 'Milk', 'Bread']);
    // The keys suggestions learn from must survive, or the feature can never
    // bootstrap: no history means no suggestion, and a suggestion-less commit that
    // dropped its keys would build no history.
    expect(payload.items.map((i) => i.norm_key)).toEqual(['BATTERIES', 'MILK', 'BREAD']);
    expect(payload.items.some((i) => i.description === 'Unitemized remainder')).toBe(false);
  });

  // The reported case: a grocery run where only one item belongs elsewhere.
  it('needs only the one exception selected', () => {
    const groceries = 9;
    const lines = Array.from({ length: 10 }, (_, i) =>
      line({ budgetId: null, amount: 5, description: `item ${i}`, normKey: `ITEM ${i}` })
    );
    lines[3] = line({ budgetId: 4, amount: 5, description: 'wine', normKey: 'WINE' });

    const payload = buildReceiptCommit({
      description: 'Market',
      date: '',
      total: 50,
      catchAllBudgetId: groceries,
      lines,
      reconciled: true
    });

    expect(payload.items).toHaveLength(10);
    expect(payload.items.filter((i) => i.budget_id === groceries)).toHaveLength(9);
    expect(payload.items.filter((i) => i.budget_id === 4)).toHaveLength(1);
  });

  // A scan of an unseen merchant returns no suggestions at all, so every line
  // arrives unassigned. That must still commit as a full itemization.
  it('commits a wholly unassigned receipt to the catch-all', () => {
    const payload = buildReceiptCommit({
      description: 'New merchant',
      date: '',
      total: 30,
      catchAllBudgetId: 9,
      lines: [
        line({ budgetId: null, amount: 10 }),
        line({ budgetId: null, amount: 10 }),
        line({ budgetId: null, amount: 10 })
      ],
      reconciled: true
    });
    expect(payload.items).toHaveLength(3);
    expect(payload.items.every((i) => i.budget_id === 9)).toBe(true);
  });

  it('still skips lines with no amount', () => {
    const payload = buildReceiptCommit({
      description: 'x',
      date: '',
      total: 10,
      catchAllBudgetId: 9,
      lines: [line({ budgetId: 3, amount: 10 }), line({ budgetId: 4, amount: 0 })],
      reconciled: true
    });
    expect(payload.items).toHaveLength(1);
    expect(payload.items[0].amount_cents).toBe(1000);
  });

  it('renumbers positions consecutively', () => {
    const payload = buildReceiptCommit({
      description: 'x',
      date: '',
      total: 40,
      catchAllBudgetId: 9,
      lines: [line({ budgetId: 3, amount: 10 }), line({ budgetId: 4, amount: 10 })],
      reconciled: true
    });
    // Two lines plus the 20.00 the total does not account for.
    expect(payload.items.map((i) => i.position)).toEqual([1, 2, 3]);
  });

  it('falls back to a default merchant', () => {
    const payload = buildReceiptCommit({
      description: '   ',
      date: '',
      total: 10,
      catchAllBudgetId: 9,
      lines: [line({ budgetId: 3, amount: 10 })],
      reconciled: true
    });
    expect(payload.merchant).toBe('Itemized receipt');
  });

  it('propagates a failed reconciliation so the server records it', () => {
    const payload = buildReceiptCommit({
      description: 'x',
      date: '',
      total: 10,
      catchAllBudgetId: 9,
      lines: [line({ budgetId: 3, amount: 10 })],
      reconciled: false
    });
    expect(payload.reconciled).toBe(false);
  });

  it('rejects receipts it cannot commit', () => {
    const base = { description: 'x', date: '', catchAllBudgetId: 9, reconciled: true };
    expect(() =>
      buildReceiptCommit({ ...base, total: 0, lines: [line({ budgetId: 3, amount: 10 })] })
    ).toThrow(/greater than zero/i);
    expect(() => buildReceiptCommit({ ...base, total: 10, lines: [] })).toThrow(/at least one/i);
    // No amount is the only thing that makes a line uncommittable now. An
    // unassigned line is fine -- it goes to the catch-all.
    expect(() =>
      buildReceiptCommit({ ...base, total: 10, lines: [line({ budgetId: 3, amount: 0 })] })
    ).toThrow(/at least one/i);
    expect(() =>
      buildReceiptCommit({ ...base, total: 10, lines: [line({ budgetId: null, amount: 10 })] })
    ).not.toThrow();
  });
});

describe('toDateInput', () => {
  it('keeps the calendar date the receipt printed', () => {
    // The date on a receipt is a calendar date, not an instant. Reading midnight
    // UTC with local getters moved it a day earlier anywhere west of UTC, so a
    // receipt dated the 20th was committed as the 19th.
    expect(toDateInput('2026-08-20T00:00:00Z')).toBe('2026-08-20');
    expect(toDateInput('2026-08-20T08:40:00Z')).toBe('2026-08-20');
    expect(toDateInput('2026-01-01T00:00:00Z')).toBe('2026-01-01');
    expect(toDateInput('2026-08-20')).toBe('2026-08-20');
    // A far-eastern offset must not push it forward either.
    expect(toDateInput('2026-08-20T23:59:00+14:00')).toBe('2026-08-20');
  });

  it('returns empty for missing or unparseable dates', () => {
    expect(toDateInput(null)).toBe('');
    expect(toDateInput('')).toBe('');
    expect(toDateInput('   ')).toBe('');
    expect(toDateInput('not a date')).toBe('');
  });
});
