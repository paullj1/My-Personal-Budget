// Pure helpers for turning reviewed receipt lines into a commit payload.
//
// Kept out of the component so the money arithmetic is directly testable: this
// is where a mistake silently misfiles spending.

export type ItemizedLine = {
  id: string;
  budgetId: number | null;
  description: string;
  amount: number;
  // Populated by a scan; harmless zeros for hand-entered lines. Tax is carried
  // per line because the server prorates it exactly, in cents.
  lineText?: string;
  normKey?: string;
  marker?: string;
  taxable?: boolean | null;
  taxCents?: number;
  adjustCents?: number;
  suggested?: boolean;
};

export type CommitItem = {
  position: number;
  budget_id: number;
  line_text: string;
  norm_key: string;
  description: string;
  marker: string;
  taxable: boolean | null;
  amount_cents: number;
  tax_cents: number;
  adjust_cents: number;
};

export type CommitPayload = {
  merchant: string;
  purchased_at: string | null;
  catch_all_budget_id: number;
  reconciled: boolean;
  items: CommitItem[];
};

export const toCents = (value: number) => Math.round((Number.isFinite(value) ? value : 0) * 100);
export const fromCents = (cents: number) => cents / 100;

/** What this line actually costs the budget: net price plus its share of tax and discounts. */
export const lineTotalCents = (line: ItemizedLine) =>
  toCents(line.amount) + (line.taxCents || 0) + (line.adjustCents || 0);

/** Sum of every line as it will be committed, in cents. */
export const allocatedCents = (lines: ItemizedLine[]) =>
  lines.reduce((sum, line) => sum + lineTotalCents(line), 0);

/** Receipts print dates; the date input wants yyyy-mm-dd in local time. */
export const toDateInput = (iso: string | null) => {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
};

export type BuildCommitArgs = {
  description: string;
  date: string;
  total: number;
  catchAllBudgetId: number;
  lines: ItemizedLine[];
  reconciled: boolean;
};

/**
 * Builds the commit payload, appending a catch-all line for anything the user
 * left unallocated. Throws with a user-facing message when the receipt cannot
 * be committed as entered.
 */
export function buildReceiptCommit(args: BuildCommitArgs): CommitPayload {
  const merchant = args.description.trim() || 'Itemized receipt';
  if (args.total <= 0) {
    throw new Error('Total must be greater than zero.');
  }

  const assigned = args.lines.filter(
    (line): line is ItemizedLine & { budgetId: number } => line.budgetId !== null && line.amount > 0
  );
  if (!assigned.length) {
    throw new Error('Add at least one line item with a budget and amount.');
  }

  const remainder = toCents(args.total) - allocatedCents(assigned);
  // A cent of slack absorbs rounding between the printed total and the lines.
  if (remainder < -1) {
    throw new Error('Allocations exceed the receipt total.');
  }

  const items: CommitItem[] = assigned.map((line, index) => ({
    position: index + 1,
    budget_id: line.budgetId,
    line_text: line.lineText || '',
    norm_key: line.normKey || '',
    description: line.description.trim(),
    marker: line.marker || '',
    taxable: line.taxable ?? null,
    amount_cents: toCents(line.amount),
    tax_cents: line.taxCents || 0,
    adjust_cents: line.adjustCents || 0
  }));

  // Anything unassigned still belongs on the receipt, so it becomes its own
  // catch-all line rather than being silently dropped.
  if (remainder > 0) {
    items.push({
      position: items.length + 1,
      budget_id: args.catchAllBudgetId,
      line_text: '',
      norm_key: '',
      description: 'Unitemized remainder',
      marker: '',
      taxable: null,
      amount_cents: remainder,
      tax_cents: 0,
      adjust_cents: 0
    });
  }

  return {
    merchant,
    purchased_at: args.date || null,
    catch_all_budget_id: args.catchAllBudgetId,
    reconciled: args.reconciled,
    items
  };
}
