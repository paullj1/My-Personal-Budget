package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ReceiptItemInput is one reviewed line ready to commit. Amounts are integer
// cents; the conversion to the float column on transacts happens once, here.
type ReceiptItemInput struct {
	Position    int    `json:"position"`
	BudgetID    *int64 `json:"budget_id"`
	LineText    string `json:"line_text"`
	NormKey     string `json:"norm_key"`
	Description string `json:"description"`
	Marker      string `json:"marker"`
	Taxable     *bool  `json:"taxable"`
	AmountCents int    `json:"amount_cents"`
	TaxCents    int    `json:"tax_cents"`
	AdjustCents int    `json:"adjust_cents"`
}

// TotalCents is what actually lands in a budget for this line.
func (i ReceiptItemInput) TotalCents() int {
	return i.AmountCents + i.TaxCents + i.AdjustCents
}

// ReceiptInput is a reviewed receipt awaiting commit.
type ReceiptInput struct {
	Merchant         string
	PurchasedAt      *time.Time
	Currency         string
	CatchAllBudgetID int64
	TaxEvidence      string
	TaxBasis         string
	Reconciled       bool
	Model            string
	// ExtractionSource records which path produced the extraction: SourceServerOCR
	// when this app read the photo itself, SourceClientSupplied when an MCP client
	// structured it and only the arithmetic happened here. Empty defaults to
	// SourceServerOCR, which is what every pre-existing row is.
	ExtractionSource string
	Parsed           json.RawMessage
	Items            []ReceiptItemInput
}

// Where an extraction came from -- the app's own pipeline, or a caller that
// arrived with the receipt already structured.
//
// Deliberately not model names. The server-side model is already recorded in
// Model, and the client-side one is not something this app can observe or trust
// to stay the same; a column full of last year's model name would be worse than
// no column. Hand-entered and hand-corrected receipts count as SourceServerOCR:
// they came through this app's own path, which is the distinction being drawn.
const (
	SourceServerOCR      = "server_ocr"
	SourceClientSupplied = "client_supplied"
)

// Receipt is a committed receipt.
type Receipt struct {
	ID            int64      `json:"id"`
	UserID        *int64     `json:"user_id,omitempty"`
	Merchant      string     `json:"merchant"`
	PurchasedAt   *time.Time `json:"purchased_at,omitempty"`
	Currency      string     `json:"currency"`
	SubtotalCents int        `json:"subtotal_cents"`
	TaxCents      int        `json:"tax_cents"`
	TotalCents    int        `json:"total_cents"`
	TaxEvidence   string     `json:"tax_evidence"`
	TaxBasis      string     `json:"tax_basis"`
	Reconciled    bool       `json:"reconciled"`
	// ExtractionSource is SourceServerOCR or SourceClientSupplied.
	ExtractionSource string    `json:"extraction_source"`
	CreatedAt        time.Time `json:"created_at"`
}

// CommitReceiptResult reports what was written.
type CommitReceiptResult struct {
	Receipt      Receipt                `json:"receipt"`
	Transactions []Transaction          `json:"transactions"`
	BudgetIDs    []int64                `json:"budget_ids"`
	Items        []CommittedReceiptItem `json:"items"`
}

type CommittedReceiptItem struct {
	ID          int64  `json:"id"`
	BudgetID    *int64 `json:"budget_id,omitempty"`
	TransactID  *int64 `json:"transact_id,omitempty"`
	Description string `json:"description"`
	TotalCents  int    `json:"total_cents"`
}

// ErrNoReceiptItems means there was nothing to commit.
var ErrNoReceiptItems = errors.New("receipt has no items to commit")

// CommitReceipt writes the receipt, one transaction per budget, and the item
// rows, all inside a single database transaction.
//
// This replaces the frontend's former loop of independent POSTs, which could
// leave a partially-itemized receipt behind if one call failed mid-way.
func (s *Store) CommitReceipt(ctx context.Context, userID *int64, in ReceiptInput) (CommitReceiptResult, error) {
	if len(in.Items) == 0 {
		return CommitReceiptResult{}, ErrNoReceiptItems
	}
	if in.CatchAllBudgetID == 0 {
		return CommitReceiptResult{}, fmt.Errorf("catch-all budget is required")
	}

	// Resolve every line to a budget up front so access checks cover the real set.
	items := make([]ReceiptItemInput, len(in.Items))
	copy(items, in.Items)
	touched := map[int64]bool{in.CatchAllBudgetID: true}
	for idx := range items {
		if items[idx].BudgetID == nil || *items[idx].BudgetID == 0 {
			b := in.CatchAllBudgetID
			items[idx].BudgetID = &b
		}
		touched[*items[idx].BudgetID] = true
	}
	budgetIDs := make([]int64, 0, len(touched))
	for id := range touched {
		budgetIDs = append(budgetIDs, id)
	}
	sort.Slice(budgetIDs, func(i, j int) bool { return budgetIDs[i] < budgetIDs[j] })

	for _, id := range budgetIDs {
		if err := s.ensureBudgetAccess(ctx, id, userID); err != nil {
			return CommitReceiptResult{}, err
		}
	}

	subtotal, tax, adjust := 0, 0, 0
	for _, it := range items {
		subtotal += it.AmountCents
		tax += it.TaxCents
		adjust += it.AdjustCents
	}
	total := subtotal + tax + adjust

	purchasedAt := in.PurchasedAt
	// Backdating uses the printed receipt date so spending lands in the period it
	// happened; a receipt with no readable date falls back to now.
	stamp := time.Now()
	if purchasedAt != nil {
		stamp = *purchasedAt
	}

	currency := strings.TrimSpace(in.Currency)
	if currency == "" {
		currency = "USD"
	}
	parsed := in.Parsed
	if len(parsed) == 0 {
		parsed = json.RawMessage(`{}`)
	}
	source := in.ExtractionSource
	if source == "" {
		source = SourceServerOCR
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CommitReceiptResult{}, err
	}
	defer tx.Rollback()

	var receipt Receipt
	err = tx.QueryRowContext(ctx, `
		INSERT INTO receipts (user_id, merchant, purchased_at, currency, subtotal_cents,
		                      tax_cents, total_cents, tax_evidence, tax_basis, reconciled, parsed, model,
		                      extraction_source)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		RETURNING id, user_id, merchant, purchased_at, currency, subtotal_cents,
		          tax_cents, total_cents, tax_evidence, tax_basis, reconciled, extraction_source, created_at;`,
		userID, in.Merchant, purchasedAt, currency, subtotal, tax, total,
		nullableEvidence(in.TaxEvidence), in.TaxBasis, in.Reconciled, []byte(parsed), in.Model, source,
	).Scan(&receipt.ID, &receipt.UserID, &receipt.Merchant, &receipt.PurchasedAt, &receipt.Currency,
		&receipt.SubtotalCents, &receipt.TaxCents, &receipt.TotalCents, &receipt.TaxEvidence,
		&receipt.TaxBasis, &receipt.Reconciled, &receipt.ExtractionSource, &receipt.CreatedAt)
	if err != nil {
		if isForeignKeyError(err) {
			return CommitReceiptResult{}, ErrNotFound
		}
		return CommitReceiptResult{}, fmt.Errorf("insert receipt: %w", err)
	}

	// One transaction per budget keeps the ledger readable: a 40-item grocery run
	// becomes a handful of rows, with the detail preserved in receipt_items.
	grouped := make(map[int64][]int)
	order := make([]int64, 0, len(budgetIDs))
	for idx, it := range items {
		id := *it.BudgetID
		if _, seen := grouped[id]; !seen {
			order = append(order, id)
		}
		grouped[id] = append(grouped[id], idx)
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })

	result := CommitReceiptResult{Receipt: receipt}
	txnByBudget := make(map[int64]int64, len(order))
	for _, budgetID := range order {
		idxs := grouped[budgetID]
		sum := 0
		for _, idx := range idxs {
			sum += items[idx].TotalCents()
		}
		if sum == 0 {
			// A fully-discounted group would violate the amount > 0 constraint.
			continue
		}
		desc := receiptDescription(in.Merchant, len(idxs))
		credit := sum < 0
		amount := float64(sum) / 100
		if credit {
			amount = -amount
		}

		var t Transaction
		err = tx.QueryRowContext(ctx, `
			INSERT INTO transacts (budget_id, user_id, description, credit, amount, receipt_id, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$7)
			RETURNING id, budget_id, user_id, description, credit, amount, created_at, updated_at;`,
			budgetID, userID, desc, credit, amount, receipt.ID, stamp,
		).Scan(&t.ID, &t.BudgetID, &t.UserID, &t.Description, &t.Credit, &t.Amount, &t.CreatedAt, &t.UpdatedAt)
		if err != nil {
			if isForeignKeyError(err) {
				return CommitReceiptResult{}, ErrNotFound
			}
			return CommitReceiptResult{}, fmt.Errorf("insert transaction: %w", err)
		}
		result.Transactions = append(result.Transactions, t)
		txnByBudget[budgetID] = t.ID
	}

	for _, it := range items {
		var txnID *int64
		if id, ok := txnByBudget[*it.BudgetID]; ok {
			txnID = &id
		}
		normKey := it.NormKey
		if normKey == "" {
			normKey = it.Description
		}
		var itemID int64
		err = tx.QueryRowContext(ctx, `
			INSERT INTO receipt_items (receipt_id, budget_id, transact_id, line_text, norm_key,
			                           description, marker, amount_cents, tax_cents, adjust_cents, taxable, position)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
			RETURNING id;`,
			receipt.ID, it.BudgetID, txnID, it.LineText, normKey, it.Description, it.Marker,
			it.AmountCents, it.TaxCents, it.AdjustCents, it.Taxable, it.Position,
		).Scan(&itemID)
		if err != nil {
			return CommitReceiptResult{}, fmt.Errorf("insert receipt item: %w", err)
		}
		result.Items = append(result.Items, CommittedReceiptItem{
			ID: itemID, BudgetID: it.BudgetID, TransactID: txnID,
			Description: it.Description, TotalCents: it.TotalCents(),
		})
	}

	if err := tx.Commit(); err != nil {
		return CommitReceiptResult{}, fmt.Errorf("commit receipt: %w", err)
	}
	result.BudgetIDs = budgetIDs
	return result, nil
}

// SuggestBudgets maps normalized item keys to the budget they were last filed
// under, scoped to budgets the user can reach so shared budgets learn together.
//
// Most-frequent wins, most-recent breaks ties.
func (s *Store) SuggestBudgets(ctx context.Context, userID *int64, keys []string) (map[string]int64, error) {
	out := make(map[string]int64)
	cleaned := make([]string, 0, len(keys))
	seen := make(map[string]bool, len(keys))
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		cleaned = append(cleaned, k)
	}
	if len(cleaned) == 0 {
		return out, nil
	}

	// A nil userID means auth is disabled; fall back to global history.
	var (
		rows *sql.Rows
		err  error
	)
	const scoped = `
		SELECT ri.norm_key, ri.budget_id
		FROM receipt_items ri
		JOIN users_budgets ub ON ub.budget_id = ri.budget_id
		WHERE ri.budget_id IS NOT NULL
		  AND ub.user_id = $2
		  AND ri.norm_key = ANY($1)
		GROUP BY ri.norm_key, ri.budget_id
		ORDER BY ri.norm_key, COUNT(*) DESC, MAX(ri.id) DESC;`
	const global = `
		SELECT ri.norm_key, ri.budget_id
		FROM receipt_items ri
		WHERE ri.budget_id IS NOT NULL
		  AND ri.norm_key = ANY($1)
		GROUP BY ri.norm_key, ri.budget_id
		ORDER BY ri.norm_key, COUNT(*) DESC, MAX(ri.id) DESC;`

	if userID != nil {
		rows, err = s.db.QueryContext(ctx, scoped, cleaned, *userID)
	} else {
		rows, err = s.db.QueryContext(ctx, global, cleaned)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var key string
		var budgetID int64
		if err := rows.Scan(&key, &budgetID); err != nil {
			return nil, err
		}
		// ORDER BY puts the winner first; ignore the rest for that key.
		if _, taken := out[key]; !taken {
			out[key] = budgetID
		}
	}
	return out, rows.Err()
}

// GetReceipt returns a committed receipt and its items, for drill-down.
func (s *Store) GetReceipt(ctx context.Context, id int64, userID *int64) (Receipt, []CommittedReceiptItem, error) {
	var r Receipt
	err := s.db.QueryRowContext(ctx, `
		SELECT id, user_id, merchant, purchased_at, currency, subtotal_cents, tax_cents,
		       total_cents, tax_evidence, tax_basis, reconciled, extraction_source, created_at
		FROM receipts WHERE id = $1;`, id).
		Scan(&r.ID, &r.UserID, &r.Merchant, &r.PurchasedAt, &r.Currency, &r.SubtotalCents,
			&r.TaxCents, &r.TotalCents, &r.TaxEvidence, &r.TaxBasis, &r.Reconciled,
			&r.ExtractionSource, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Receipt{}, nil, ErrNotFound
	}
	if err != nil {
		return Receipt{}, nil, err
	}
	// Visibility follows the budgets the receipt touched.
	if userID != nil && (r.UserID == nil || *r.UserID != *userID) {
		var visible bool
		err = s.db.QueryRowContext(ctx, `
			SELECT TRUE FROM receipt_items ri
			JOIN users_budgets ub ON ub.budget_id = ri.budget_id
			WHERE ri.receipt_id = $1 AND ub.user_id = $2 LIMIT 1;`, id, *userID).Scan(&visible)
		if errors.Is(err, sql.ErrNoRows) {
			return Receipt{}, nil, ErrNotFound
		}
		if err != nil {
			return Receipt{}, nil, err
		}
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, budget_id, transact_id, description, amount_cents + tax_cents + adjust_cents
		FROM receipt_items WHERE receipt_id = $1 ORDER BY position, id;`, id)
	if err != nil {
		return Receipt{}, nil, err
	}
	defer rows.Close()

	var items []CommittedReceiptItem
	for rows.Next() {
		var it CommittedReceiptItem
		if err := rows.Scan(&it.ID, &it.BudgetID, &it.TransactID, &it.Description, &it.TotalCents); err != nil {
			return Receipt{}, nil, err
		}
		items = append(items, it)
	}
	return r, items, rows.Err()
}

func receiptDescription(merchant string, n int) string {
	merchant = strings.TrimSpace(merchant)
	if merchant == "" {
		merchant = "Receipt"
	}
	if n == 1 {
		return fmt.Sprintf("%s - 1 item", merchant)
	}
	return fmt.Sprintf("%s - %d items", merchant, n)
}

func nullableEvidence(v string) string {
	if strings.TrimSpace(v) == "" {
		return "unknown"
	}
	return v
}
