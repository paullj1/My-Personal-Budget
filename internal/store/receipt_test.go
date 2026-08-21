package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// These exercise CommitReceipt's atomicity against a real Postgres, which is the
// only way to prove the single-transaction guarantee. Set TEST_DATABASE_URL to
// run them; without it the package still tests clean.
//
//	podman run -d --name mpb-test-db -e POSTGRES_PASSWORD=budgetpass \
//	  -e POSTGRES_DB=budget -p 5433:5432 docker.io/library/postgres:11-alpine
//	psql -f db/schema.sql
//	TEST_DATABASE_URL='postgres://postgres:budgetpass@localhost:5433/budget?sslmode=disable' go test ./internal/store/
func testStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database integration tests")
	}
	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return New(db)
}

// fixture creates an isolated user with three budgets, cleaned up afterwards.
type fixture struct {
	store     *Store
	userID    int64
	other     int64
	groceries int64
	household int64
	catchAll  int64
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	s := testStore(t)
	ctx := context.Background()
	f := &fixture{store: s}

	suffix := time.Now().UnixNano()
	mustScan(t, s, &f.userID, `INSERT INTO users (email) VALUES ($1) RETURNING id`,
		mkEmail("owner", suffix))
	mustScan(t, s, &f.other, `INSERT INTO users (email) VALUES ($1) RETURNING id`,
		mkEmail("stranger", suffix))

	for name, dst := range map[string]*int64{
		"Groceries": &f.groceries, "Household": &f.household, "Misc": &f.catchAll,
	} {
		mustScan(t, s, dst, `INSERT INTO budgets (name) VALUES ($1) RETURNING id`, name)
		mustExec(t, s, `INSERT INTO users_budgets (user_id, budget_id) VALUES ($1,$2)`, f.userID, *dst)
	}

	t.Cleanup(func() {
		// receipts and transacts cascade or null out via FKs; budgets carry the rest.
		mustExec(t, s, `DELETE FROM receipts WHERE user_id = $1`, f.userID)
		for _, b := range []int64{f.groceries, f.household, f.catchAll} {
			mustExec(t, s, `DELETE FROM budgets WHERE id = $1`, b)
		}
		mustExec(t, s, `DELETE FROM users WHERE id = ANY($1)`, []int64{f.userID, f.other})
	})
	_ = ctx
	return f
}

func mkEmail(prefix string, suffix int64) string {
	return prefix + "-" + time.Unix(0, suffix).Format("20060102150405.000000000") + "@test.local"
}

func mustScan(t *testing.T, s *Store, dst any, q string, args ...any) {
	t.Helper()
	if err := s.db.QueryRow(q, args...).Scan(dst); err != nil {
		t.Fatalf("query %q: %v", q, err)
	}
}

func mustExec(t *testing.T, s *Store, q string, args ...any) {
	t.Helper()
	if _, err := s.db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func b(v bool) *bool    { return &v }
func id(v int64) *int64 { return &v }

// targetItems is the real Target receipt after allocation: 42/22/180/180 cents
// of tax spread across four items, totalling 7490c.
func targetItems(groceries, household int64) []ReceiptItemInput {
	return []ReceiptItemInput{
		{Position: 1, BudgetID: id(groceries), Description: "GATORADE", NormKey: "GATORADE TF",
			Marker: "TF", Taxable: b(true), AmountCents: 699, TaxCents: 42},
		{Position: 2, BudgetID: id(groceries), Description: "Good&Gather", NormKey: "GOOD&GATHER TF",
			Marker: "TF", Taxable: b(true), AmountCents: 369, TaxCents: 22},
		{Position: 3, BudgetID: id(household), Description: "ELEC KETTLE", NormKey: "ELEC KETTLE T",
			Marker: "T", Taxable: b(true), AmountCents: 2999, TaxCents: 180},
		{Position: 4, BudgetID: id(household), Description: "Bodum", NormKey: "BODUM T",
			Marker: "T", Taxable: b(true), AmountCents: 2999, TaxCents: 180},
	}
}

func TestCommitReceiptGroupsByBudget(t *testing.T) {
	f := newFixture(t)
	purchased := time.Date(2026, 8, 20, 8, 40, 0, 0, time.Local)

	got, err := f.store.CommitReceipt(context.Background(), &f.userID, ReceiptInput{
		Merchant:         "Target Columbia",
		PurchasedAt:      &purchased,
		CatchAllBudgetID: f.catchAll,
		TaxEvidence:      "per_line_flags",
		TaxBasis:         "printed_base_all_items",
		Reconciled:       true,
		Model:            "qwen3.8:27b",
		Parsed:           json.RawMessage(`{"total":74.90}`),
		Items:            targetItems(f.groceries, f.household),
	})
	if err != nil {
		t.Fatalf("CommitReceipt: %v", err)
	}

	// Four items across two budgets must yield exactly two ledger rows.
	if len(got.Transactions) != 2 {
		t.Fatalf("transactions = %d, want 2 (one per budget)", len(got.Transactions))
	}
	byBudget := map[int64]Transaction{}
	for _, tr := range got.Transactions {
		byBudget[tr.BudgetID] = tr
	}
	if tr, ok := byBudget[f.groceries]; !ok {
		t.Error("no transaction for groceries")
	} else if tr.Amount != 11.32 {
		t.Errorf("groceries amount = %v, want 11.32 (699+42 plus 369+22)", tr.Amount)
	}
	if tr, ok := byBudget[f.household]; !ok {
		t.Error("no transaction for household")
	} else if tr.Amount != 63.58 {
		t.Errorf("household amount = %v, want 63.58 (2999+180+2999+180)", tr.Amount)
	}

	// Every cent must survive the round trip.
	var sum float64
	for _, tr := range got.Transactions {
		sum += tr.Amount
	}
	if Cents := int(sum*100 + 0.5); Cents != 7490 {
		t.Errorf("transactions total %d cents, want the printed 7490", Cents)
	}

	if got.Receipt.TotalCents != 7490 || got.Receipt.TaxCents != 424 {
		t.Errorf("receipt totals = %d/%d, want 7490/424", got.Receipt.TotalCents, got.Receipt.TaxCents)
	}
	if !got.Receipt.Reconciled {
		t.Error("expected reconciled to persist as true")
	}

	// Backdating: transactions carry the printed date, not now.
	for _, tr := range got.Transactions {
		if tr.CreatedAt.Year() != 2026 || tr.CreatedAt.Month() != time.August || tr.CreatedAt.Day() != 20 {
			t.Errorf("transaction created_at = %v, want the printed 2026-08-20", tr.CreatedAt)
		}
	}

	// Items link to both the receipt and their budget's transaction.
	if len(got.Items) != 4 {
		t.Fatalf("items = %d, want 4", len(got.Items))
	}
	for _, it := range got.Items {
		if it.TransactID == nil {
			t.Errorf("item %q has no transaction link", it.Description)
		}
	}
	var linked int
	mustScan(t, f.store, &linked,
		`SELECT COUNT(*) FROM transacts WHERE receipt_id = $1`, got.Receipt.ID)
	if linked != 2 {
		t.Errorf("transacts.receipt_id set on %d rows, want 2", linked)
	}
}

func TestCommitReceiptUnassignedItemsGoToCatchAll(t *testing.T) {
	f := newFixture(t)
	items := targetItems(f.groceries, f.household)
	items[0].BudgetID = nil // reviewer left this one alone
	items[1].BudgetID = id(0)

	got, err := f.store.CommitReceipt(context.Background(), &f.userID, ReceiptInput{
		Merchant: "Target", CatchAllBudgetID: f.catchAll, Items: items,
	})
	if err != nil {
		t.Fatalf("CommitReceipt: %v", err)
	}
	var catchAllAmount float64
	found := false
	for _, tr := range got.Transactions {
		if tr.BudgetID == f.catchAll {
			catchAllAmount, found = tr.Amount, true
		}
	}
	if !found {
		t.Fatal("expected a catch-all transaction for unassigned items")
	}
	if catchAllAmount != 11.32 {
		t.Errorf("catch-all amount = %v, want 11.32 (699+42+369+22)", catchAllAmount)
	}
}

func TestCommitReceiptIsAtomic(t *testing.T) {
	f := newFixture(t)
	items := targetItems(f.groceries, f.household)
	// A budget the user cannot reach must abort the whole commit.
	var foreign int64
	mustScan(t, f.store, &foreign, `INSERT INTO budgets (name) VALUES ('Not Yours') RETURNING id`)
	t.Cleanup(func() { mustExec(t, f.store, `DELETE FROM budgets WHERE id = $1`, foreign) })
	items[2].BudgetID = &foreign

	before := countRows(t, f.store, `SELECT COUNT(*) FROM receipts`)
	beforeTx := countRows(t, f.store, `SELECT COUNT(*) FROM transacts`)

	_, err := f.store.CommitReceipt(context.Background(), &f.userID, ReceiptInput{
		Merchant: "Target", CatchAllBudgetID: f.catchAll, Items: items,
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for an inaccessible budget, got %v", err)
	}
	if got := countRows(t, f.store, `SELECT COUNT(*) FROM receipts`); got != before {
		t.Errorf("receipts changed from %d to %d; commit was not atomic", before, got)
	}
	if got := countRows(t, f.store, `SELECT COUNT(*) FROM transacts`); got != beforeTx {
		t.Errorf("transacts changed from %d to %d; commit was not atomic", beforeTx, got)
	}
}

func TestCommitReceiptRejectsEmptyAndMissingCatchAll(t *testing.T) {
	f := newFixture(t)
	if _, err := f.store.CommitReceipt(context.Background(), &f.userID, ReceiptInput{
		CatchAllBudgetID: f.catchAll,
	}); !errors.Is(err, ErrNoReceiptItems) {
		t.Errorf("expected ErrNoReceiptItems, got %v", err)
	}
	if _, err := f.store.CommitReceipt(context.Background(), &f.userID, ReceiptInput{
		Items: targetItems(f.groceries, f.household),
	}); err == nil {
		t.Error("expected an error when no catch-all budget is given")
	}
}

func TestCommitReceiptSkipsZeroSumGroups(t *testing.T) {
	f := newFixture(t)
	// A fully discounted line nets zero and must not violate amount > 0.
	items := []ReceiptItemInput{
		{Position: 1, BudgetID: id(f.groceries), Description: "FREE ITEM", AmountCents: 500, AdjustCents: -500},
		{Position: 2, BudgetID: id(f.household), Description: "KETTLE", AmountCents: 2999},
	}
	got, err := f.store.CommitReceipt(context.Background(), &f.userID, ReceiptInput{
		Merchant: "Target", CatchAllBudgetID: f.catchAll, Items: items,
	})
	if err != nil {
		t.Fatalf("CommitReceipt: %v", err)
	}
	if len(got.Transactions) != 1 {
		t.Errorf("transactions = %d, want 1 (the zero-sum group is skipped)", len(got.Transactions))
	}
	// The item row still persists so the receipt stays complete.
	if len(got.Items) != 2 {
		t.Errorf("items = %d, want 2", len(got.Items))
	}
}

func TestSuggestBudgetsLearnsFromHistory(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if _, err := f.store.CommitReceipt(ctx, &f.userID, ReceiptInput{
		Merchant: "Target", CatchAllBudgetID: f.catchAll,
		Items: targetItems(f.groceries, f.household),
	}); err != nil {
		t.Fatalf("seed commit: %v", err)
	}

	got, err := f.store.SuggestBudgets(ctx, &f.userID,
		[]string{"GATORADE TF", "ELEC KETTLE T", "NEVER SEEN"})
	if err != nil {
		t.Fatalf("SuggestBudgets: %v", err)
	}
	if got["GATORADE TF"] != f.groceries {
		t.Errorf("GATORADE suggested %d, want groceries %d", got["GATORADE TF"], f.groceries)
	}
	if got["ELEC KETTLE T"] != f.household {
		t.Errorf("KETTLE suggested %d, want household %d", got["ELEC KETTLE T"], f.household)
	}
	if _, has := got["NEVER SEEN"]; has {
		t.Error("unseen key should have no suggestion, not a guess")
	}
}

func TestSuggestBudgetsPrefersMostFrequent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// Filed to groceries twice, household once: groceries should win.
	for _, budget := range []int64{f.groceries, f.groceries, f.household} {
		if _, err := f.store.CommitReceipt(ctx, &f.userID, ReceiptInput{
			Merchant: "Target", CatchAllBudgetID: f.catchAll,
			Items: []ReceiptItemInput{{Position: 1, BudgetID: id(budget),
				Description: "MILK", NormKey: "MILK", AmountCents: 399}},
		}); err != nil {
			t.Fatalf("seed commit: %v", err)
		}
	}
	got, err := f.store.SuggestBudgets(ctx, &f.userID, []string{"MILK"})
	if err != nil {
		t.Fatalf("SuggestBudgets: %v", err)
	}
	if got["MILK"] != f.groceries {
		t.Errorf("MILK suggested %d, want the more frequent groceries %d", got["MILK"], f.groceries)
	}
}

func TestSuggestBudgetsScopedToAccessibleBudgets(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if _, err := f.store.CommitReceipt(ctx, &f.userID, ReceiptInput{
		Merchant: "Target", CatchAllBudgetID: f.catchAll,
		Items: targetItems(f.groceries, f.household),
	}); err != nil {
		t.Fatalf("seed commit: %v", err)
	}

	// A user with no share of these budgets must learn nothing from them.
	got, err := f.store.SuggestBudgets(ctx, &f.other, []string{"GATORADE TF"})
	if err != nil {
		t.Fatalf("SuggestBudgets: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no suggestions across an unshared budget, got %v", got)
	}
}

func TestSuggestBudgetsHandlesEmptyInput(t *testing.T) {
	f := newFixture(t)
	got, err := f.store.SuggestBudgets(context.Background(), &f.userID, []string{"", "   "})
	if err != nil {
		t.Fatalf("SuggestBudgets: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no suggestions, got %v", got)
	}
}

func TestGetReceipt(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	committed, err := f.store.CommitReceipt(ctx, &f.userID, ReceiptInput{
		Merchant: "Target Columbia", CatchAllBudgetID: f.catchAll,
		Items: targetItems(f.groceries, f.household),
	})
	if err != nil {
		t.Fatalf("CommitReceipt: %v", err)
	}

	r, items, err := f.store.GetReceipt(ctx, committed.Receipt.ID, &f.userID)
	if err != nil {
		t.Fatalf("GetReceipt: %v", err)
	}
	if r.Merchant != "Target Columbia" || r.TotalCents != 7490 {
		t.Errorf("receipt = %+v", r)
	}
	if len(items) != 4 {
		t.Errorf("items = %d, want 4", len(items))
	}
	if len(items) > 0 && items[0].TotalCents != 741 {
		t.Errorf("first item total = %d, want 741 (699+42)", items[0].TotalCents)
	}

	if _, _, err := f.store.GetReceipt(ctx, committed.Receipt.ID, &f.other); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for an unrelated user, got %v", err)
	}
	if _, _, err := f.store.GetReceipt(ctx, 999999999, &f.userID); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for a missing receipt, got %v", err)
	}
}

func countRows(t *testing.T, s *Store, q string) int {
	t.Helper()
	var n int
	mustScan(t, s, &n, q)
	return n
}
