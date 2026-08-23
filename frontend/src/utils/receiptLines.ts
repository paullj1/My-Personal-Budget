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

/**
 * Formats a receipt date for a date input.
 *
 * The calendar date printed on a receipt is not an instant, so it must not be
 * shifted by a timezone. The server sends it as midnight UTC, and reading that
 * back with local getters moved it a day earlier anywhere west of UTC -- a
 * receipt dated the 20th was committed as the 19th. Take the date part of the
 * ISO string verbatim and only fall back to parsing for other formats, using UTC
 * getters so the calendar date survives.
 */
export const toDateInput = (iso: string | null) => {
  if (!iso) return '';
  const trimmed = iso.trim();
  const direct = /^(\d{4}-\d{2}-\d{2})/.exec(trimmed);
  if (direct) return direct[1];
  const d = new Date(trimmed);
  if (Number.isNaN(d.getTime())) return '';
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getUTCFullYear()}-${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())}`;
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
 * Builds the commit payload.
 *
 * Lines the user did not assign go to the catch-all budget individually, keeping
 * their descriptions and norm_keys. A catch-all line is appended only for value the
 * lines do not account for at all -- a gap between their sum and the printed total.
 *
 * Throws with a user-facing message when the receipt cannot be committed as
 * entered.
 */
export function buildReceiptCommit(args: BuildCommitArgs): CommitPayload {
  const merchant = args.description.trim() || 'Itemized receipt';
  if (args.total <= 0) {
    throw new Error('Total must be greater than zero.');
  }

  // A line with no budget chosen falls to the catch-all. It is NOT dropped.
  //
  // Dropping it was the old behaviour, and it made the catch-all look broken: on a
  // grocery run where only one item belongs elsewhere, every other line has to be
  // set by hand or its detail is lost. Worse, a receipt from a merchant with no
  // history gets no suggestions at all, so every line starts unset -- the whole
  // receipt collapsed into a single "Unitemized remainder". The money landed in the
  // right budget, but the per-item norm_keys never reached the server, which is what
  // budget suggestions learn from. So the feature could never bootstrap: no history
  // meant no suggestions, and committing without suggestions built no history.
  const committable = args.lines.filter((line) => line.amount > 0);
  if (!committable.length) {
    throw new Error('Add at least one line item with an amount.');
  }

  const remainder = toCents(args.total) - allocatedCents(committable);
  // A cent of slack absorbs rounding between the printed total and the lines.
  if (remainder < -1) {
    throw new Error('Allocations exceed the receipt total.');
  }

  const items: CommitItem[] = committable.map((line, index) => ({
    position: index + 1,
    budget_id: line.budgetId ?? args.catchAllBudgetId,
    line_text: line.lineText || '',
    norm_key: line.normKey || '',
    description: line.description.trim(),
    marker: line.marker || '',
    taxable: line.taxable ?? null,
    amount_cents: toCents(line.amount),
    tax_cents: line.taxCents || 0,
    adjust_cents: line.adjustCents || 0
  }));

  // Value the lines do not account for -- a hand-typed total higher than the items,
  // say -- still belongs on the receipt, so it becomes its own catch-all line rather
  // than being silently dropped. Unassigned lines no longer land here; they are
  // committed above with their own detail.
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
