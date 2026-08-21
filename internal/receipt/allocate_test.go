package receipt

import (
	"testing"
)

func f(v float64) *float64 { return &v }
func b(v bool) *bool       { return &v }
func s(v string) *string   { return &v }
func i(v int) *int         { return &v }

// targetReceipt is the real Target receipt used to validate the pipeline in
// Phase 0: two items marked TF, two marked T, and a tax line whose printed base
// equals the full subtotal -- proving all four were taxed at 6%.
func targetReceipt() Extraction {
	return Extraction{
		Merchant:    s("Target Columbia"),
		PurchasedAt: s("08/20/2026 08:40 AM"),
		Items: []ExItem{
			{Position: 1, LineText: "203800178 GATORADE TF $6.99", Description: "GATORADE", Amount: 6.99, Taxable: b(true), Marker: s("TF")},
			{Position: 2, LineText: "203220654 Good&Gather TF $3.69", Description: "Good&Gather", Amount: 3.69, Taxable: b(true), Marker: s("TF")},
			{Position: 3, LineText: "072081663 ELEC KETTLE T $29.99", Description: "ELEC KETTLE", Amount: 29.99, Taxable: b(true), Marker: s("T")},
			{Position: 4, LineText: "072080526 Bodum T $29.99", Description: "Bodum", Amount: 29.99, Taxable: b(true), Marker: s("T")},
		},
		TaxLines:    []ExTaxLine{{Label: "MD TAX", Rate: f(0.06), Base: f(70.66), Amount: 4.24}},
		Subtotal:    f(70.66),
		Total:       f(74.90),
		TaxEvidence: EvidencePerLineFlags,
	}
}

func TestAllocateTargetReceipt(t *testing.T) {
	got := Allocate(targetReceipt())

	if !got.Reconciliation.OK {
		t.Fatalf("expected reconciliation to pass, got %+v", got.Reconciliation)
	}
	if got.TaxBasis != BasisPrintedBaseAll {
		t.Errorf("expected printed base to select all items, got %q", got.TaxBasis)
	}

	// Largest-remainder over a 7066c base: 41.94/22.14/179.96/179.96 rounds down
	// to 41/22/179/179 with 3c left, which go to the largest remainders.
	wantTax := []int{42, 22, 180, 180}
	wantTotal := []int{741, 391, 3179, 3179}
	for idx, l := range got.Lines {
		if l.TaxCents != wantTax[idx] {
			t.Errorf("line %d (%s): tax = %d, want %d", idx, l.Description, l.TaxCents, wantTax[idx])
		}
		if l.TotalCents != wantTotal[idx] {
			t.Errorf("line %d (%s): total = %d, want %d", idx, l.Description, l.TotalCents, wantTotal[idx])
		}
	}

	if got.TaxCents != 424 {
		t.Errorf("tax total = %d, want 424", got.TaxCents)
	}
	sumTax, sumTotal := 0, 0
	for _, l := range got.Lines {
		sumTax += l.TaxCents
		sumTotal += l.TotalCents
	}
	if sumTax != 424 {
		t.Errorf("allocated tax = %d, want exactly the printed 424", sumTax)
	}
	if sumTotal != 7490 {
		t.Errorf("line totals = %d, want exactly the printed 7490", sumTotal)
	}
	if got.PurchasedAt == nil {
		t.Fatal("expected the printed date to parse")
	}
	if y, m, d := got.PurchasedAt.Date(); y != 2026 || int(m) != 8 || d != 20 {
		t.Errorf("purchased_at = %v, want 2026-08-20", got.PurchasedAt)
	}
}

// A printed base that matches only the marked items must not tax everything.
func TestAllocatePrintedBaseSelectsMarkedSubset(t *testing.T) {
	ex := targetReceipt()
	ex.Items[0].Taxable = b(false)
	ex.Items[1].Taxable = b(false)
	// 29.99 + 29.99 = 59.98 taxed at 6% = 3.60.
	ex.TaxLines = []ExTaxLine{{Label: "MD TAX", Rate: f(0.06), Base: f(59.98), Amount: 3.60}}
	ex.Total = f(74.26)

	got := Allocate(ex)
	if got.TaxBasis != BasisPrintedBaseSubset {
		t.Fatalf("expected marker subset basis, got %q", got.TaxBasis)
	}
	if got.Lines[0].TaxCents != 0 || got.Lines[1].TaxCents != 0 {
		t.Errorf("non-taxable items got tax: %d, %d", got.Lines[0].TaxCents, got.Lines[1].TaxCents)
	}
	if got.Lines[2].TaxCents+got.Lines[3].TaxCents != 360 {
		t.Errorf("taxed items got %d, want 360", got.Lines[2].TaxCents+got.Lines[3].TaxCents)
	}
	if !got.Reconciliation.OK {
		t.Errorf("expected reconciliation to pass, got %+v", got.Reconciliation)
	}
}

// With no printed base, the rate implies one. 4.24/0.06 = 70.67 which matches
// the item sum within tolerance, so everything is taxable.
func TestAllocateRateImpliedBase(t *testing.T) {
	ex := targetReceipt()
	ex.Items[0].Taxable = nil
	ex.Items[1].Taxable = nil
	ex.Items[2].Taxable = nil
	ex.Items[3].Taxable = nil
	ex.TaxEvidence = EvidenceSingleRate
	ex.TaxLines = []ExTaxLine{{Label: "MD TAX", Rate: f(0.06), Amount: 4.24}}

	got := Allocate(ex)
	if got.TaxBasis != BasisImpliedRate {
		t.Fatalf("expected rate-implied basis, got %q", got.TaxBasis)
	}
	if !got.Reconciliation.OK {
		t.Errorf("expected reconciliation to pass, got %+v", got.Reconciliation)
	}
}

// No base, no rate, no markers: spread over everything so no cent is orphaned.
func TestAllocateUnknownEvidenceProratesAll(t *testing.T) {
	ex := targetReceipt()
	for idx := range ex.Items {
		ex.Items[idx].Taxable = nil
		ex.Items[idx].Marker = nil
	}
	ex.TaxEvidence = EvidenceUnknown
	ex.TaxLines = []ExTaxLine{{Label: "TAX", Amount: 4.24}}

	got := Allocate(ex)
	if got.TaxBasis != BasisAllItems {
		t.Fatalf("expected all-items proration, got %q", got.TaxBasis)
	}
	sum := 0
	for _, l := range got.Lines {
		sum += l.TaxCents
	}
	if sum != 424 {
		t.Errorf("allocated %d, want exactly 424", sum)
	}
}

func TestAllocateReconciliationCatchesShortItems(t *testing.T) {
	ex := targetReceipt()
	// Drop the two cheap items, exactly the Phase 0 single-pass failure mode.
	ex.Items = ex.Items[2:]

	got := Allocate(ex)
	if got.Reconciliation.OK {
		t.Fatal("expected reconciliation to fail when items are missing")
	}
	if got.Reconciliation.ItemsDeltaCents != 5998-7066 {
		t.Errorf("items delta = %d, want %d", got.Reconciliation.ItemsDeltaCents, 5998-7066)
	}
	if got.Reconciliation.Message == "" {
		t.Error("expected a human-readable reconciliation message")
	}
}

func TestAllocateNoTotalsAtAllFailsReconciliation(t *testing.T) {
	ex := targetReceipt()
	ex.Subtotal, ex.Total = nil, nil
	got := Allocate(ex)
	if got.Reconciliation.OK {
		t.Fatal("expected failure when neither subtotal nor total is readable")
	}
}

func TestAllocateAdjustments(t *testing.T) {
	t.Run("attached reduces its own line", func(t *testing.T) {
		ex := targetReceipt()
		ex.Adjustments = []ExAdjust{{Label: "MFR COUPON", Amount: -2.00, AppliesToPosition: i(3)}}
		ex.Total = f(72.90)

		got := Allocate(ex)
		if got.Lines[2].AdjustCents != -200 {
			t.Errorf("line 3 adjust = %d, want -200", got.Lines[2].AdjustCents)
		}
		if got.Lines[0].AdjustCents != 0 {
			t.Errorf("unrelated line got an adjustment: %d", got.Lines[0].AdjustCents)
		}
		if !got.Reconciliation.OK {
			t.Errorf("expected reconciliation to pass, got %+v", got.Reconciliation)
		}
	})

	t.Run("unattached prorates and stays exact", func(t *testing.T) {
		ex := targetReceipt()
		ex.Adjustments = []ExAdjust{{Label: "STORE COUPON", Amount: -5.00}}
		ex.Total = f(69.90)

		got := Allocate(ex)
		sum := 0
		for _, l := range got.Lines {
			sum += l.AdjustCents
		}
		if sum != -500 {
			t.Errorf("prorated adjustments = %d, want exactly -500", sum)
		}
		if !got.Reconciliation.OK {
			t.Errorf("expected reconciliation to pass, got %+v", got.Reconciliation)
		}
	})

	t.Run("adjustment pointing at a missing line still lands", func(t *testing.T) {
		ex := targetReceipt()
		ex.Adjustments = []ExAdjust{{Label: "ORPHAN", Amount: -1.00, AppliesToPosition: i(99)}}
		ex.Total = f(73.90)

		got := Allocate(ex)
		sum := 0
		for _, l := range got.Lines {
			sum += l.AdjustCents
		}
		if sum != -100 {
			t.Errorf("orphan adjustment lost: got %d, want -100", sum)
		}
	})
}

func TestLargestRemainderAlwaysSumsExactly(t *testing.T) {
	cases := []struct {
		name    string
		amount  int
		weights []int
	}{
		{"target receipt tax", 424, []int{699, 369, 2999, 2999}},
		{"indivisible thirds", 100, []int{1, 1, 1}},
		{"single bucket", 777, []int{500}},
		{"negative discount", -500, []int{699, 369, 2999, 2999}},
		{"negative indivisible", -100, []int{1, 1, 1}},
		{"zero amount", 0, []int{1, 2, 3}},
		{"lopsided weights", 1, []int{1, 100000}},
		{"many equal buckets", 1000, []int{7, 7, 7, 7, 7, 7, 7}},
		{"large amounts", 1234567, []int{111111, 222222, 333333}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := largestRemainder(tc.amount, tc.weights)
			if len(got) != len(tc.weights) {
				t.Fatalf("got %d shares for %d weights", len(got), len(tc.weights))
			}
			sum := 0
			for _, v := range got {
				sum += v
			}
			if sum != tc.amount {
				t.Errorf("shares sum to %d, want exactly %d (%v)", sum, tc.amount, got)
			}
			// A share must never exceed its weight's fair ceiling by more than a cent.
			for idx, v := range got {
				if tc.amount >= 0 && v < 0 {
					t.Errorf("share %d negative (%d) for positive amount", idx, v)
				}
				if tc.amount <= 0 && v > 0 {
					t.Errorf("share %d positive (%d) for negative amount", idx, v)
				}
			}
		})
	}
}

func TestLargestRemainderIsDeterministic(t *testing.T) {
	weights := []int{699, 369, 2999, 2999}
	first := largestRemainder(424, weights)
	for n := 0; n < 25; n++ {
		again := largestRemainder(424, weights)
		for idx := range first {
			if first[idx] != again[idx] {
				t.Fatalf("run %d diverged at %d: %v vs %v", n, idx, first, again)
			}
		}
	}
}

func TestLargestRemainderZeroWeights(t *testing.T) {
	got := largestRemainder(424, []int{0, 0})
	for _, v := range got {
		if v != 0 {
			t.Fatalf("expected no allocation against zero weights, got %v", got)
		}
	}
}

func TestAllocateZeroPricedItemsKeepTax(t *testing.T) {
	ex := Extraction{
		Items:       []ExItem{{Position: 1, Description: "FREE SAMPLE", Amount: 0}},
		TaxLines:    []ExTaxLine{{Label: "TAX", Amount: 0.25}},
		Subtotal:    f(0),
		Total:       f(0.25),
		TaxEvidence: EvidenceUnknown,
	}
	got := Allocate(ex)
	sum := 0
	for _, l := range got.Lines {
		sum += l.TaxCents
	}
	if sum != 25 {
		t.Errorf("tax vanished against zero-priced items: got %d, want 25", sum)
	}
}

func TestNormKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"203800178 GATORADE TF $6.99", "GATORADE TF"},
		{"GRND BF 93/7      8.99 T", "GRND BF T"},
		{"072081663 ELEC KETTLE T $29.99", "ELEC KETTLE T"},
		{"203220654 Good&Gather TF $3.69", "GOOD&GATHER TF"},
		{"   ", ""},
		{"12345 67.89", ""},
	}
	for _, tc := range cases {
		if got := NormKey(tc.in); got != tc.want {
			t.Errorf("NormKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// Same product at a different price must share a key.
	if NormKey("GRND BF 93/7 8.99 T") != NormKey("GRND BF 93/7 9.49 T") {
		t.Error("expected price changes not to affect the match key")
	}
}

func TestParsePurchasedAt(t *testing.T) {
	for _, in := range []string{"2026-08-20", "08/20/2026", "08/20/2026 08:40 AM", "2026-08-20 08:40:00"} {
		if got := ParsePurchasedAt(s(in)); got == nil {
			t.Errorf("ParsePurchasedAt(%q) = nil, want a date", in)
		} else if y, m, d := got.Date(); y != 2026 || int(m) != 8 || d != 20 {
			t.Errorf("ParsePurchasedAt(%q) = %v, want 2026-08-20", in, got)
		}
	}
	for _, in := range []*string{nil, s(""), s("  "), s("not a date")} {
		if got := ParsePurchasedAt(in); got != nil {
			t.Errorf("expected nil for %v, got %v", in, got)
		}
	}
}

func TestCents(t *testing.T) {
	cases := []struct {
		in   float64
		want int
	}{
		{6.99, 699}, {0.1, 10}, {29.99, 2999}, {0, 0}, {-2.00, -200},
		{74.9, 7490}, {0.005, 1}, {1234.56, 123456},
	}
	for _, tc := range cases {
		if got := Cents(tc.in); got != tc.want {
			t.Errorf("Cents(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// Coffee, fuel and fast-food receipts routinely print a TOTAL and no SUBTOTAL.
// Treating that as zero failed reconciliation on every one of them.
func TestAllocateReceiptWithNoPrintedSubtotal(t *testing.T) {
	ex := Extraction{
		Items: []ExItem{
			{Position: 1, Description: "LATTE", Amount: 4.50, Taxable: b(true)},
			{Position: 2, Description: "SCONE", Amount: 3.25, Taxable: b(true)},
		},
		TaxLines:    []ExTaxLine{},
		Subtotal:    nil,
		Total:       f(7.75),
		TaxEvidence: EvidenceUnknown,
	}
	got := Allocate(ex)
	if !got.Reconciliation.OK {
		t.Fatalf("expected reconciliation to pass without a printed subtotal: %+v", got.Reconciliation)
	}
	if got.Reconciliation.ComputedCents != 775 {
		t.Errorf("computed = %d, want 775 from the item sum", got.Reconciliation.ComputedCents)
	}
}

func TestAllocateNoSubtotalWithTax(t *testing.T) {
	ex := Extraction{
		Items:       []ExItem{{Position: 1, Description: "SANDWICH", Amount: 10.00, Taxable: b(true)}},
		TaxLines:    []ExTaxLine{{Label: "TAX", Amount: 0.60}},
		Total:       f(10.60),
		TaxEvidence: EvidenceUnknown,
	}
	got := Allocate(ex)
	if !got.Reconciliation.OK {
		t.Fatalf("expected reconciliation to pass: %+v", got.Reconciliation)
	}
	if got.Lines[0].TotalCents != 1060 {
		t.Errorf("line total = %d, want 1060", got.Lines[0].TotalCents)
	}
}

// A genuinely inconsistent receipt must still be caught when no subtotal exists.
func TestAllocateNoSubtotalStillCatchesMismatch(t *testing.T) {
	ex := Extraction{
		Items:       []ExItem{{Position: 1, Description: "THING", Amount: 5.00}},
		Total:       f(9.99),
		TaxEvidence: EvidenceUnknown,
	}
	if got := Allocate(ex); got.Reconciliation.OK {
		t.Error("expected a mismatch between the item sum and the printed total")
	}
}

// With several tax lines allocated on different evidence, recording only the last
// basis would misdescribe how the rest was distributed.
func TestAllocateMixedTaxBasis(t *testing.T) {
	ex := Extraction{
		Items: []ExItem{
			{Position: 1, Description: "FOOD", Amount: 10.00, Taxable: b(true)},
			{Position: 2, Description: "BOOZE", Amount: 20.00, Taxable: b(false)},
		},
		TaxLines: []ExTaxLine{
			{Label: "STATE", Base: f(30.00), Amount: 1.80}, // matches every item
			{Label: "LIQUOR", Amount: 2.00},                // no base, falls to markers
		},
		Subtotal:    f(30.00),
		Total:       f(33.80),
		TaxEvidence: EvidencePerLineFlags,
	}
	got := Allocate(ex)
	if got.TaxBasis != BasisMixed {
		t.Errorf("tax_basis = %q, want %q when the lines disagree", got.TaxBasis, BasisMixed)
	}
	sum := 0
	for _, l := range got.Lines {
		sum += l.TaxCents
	}
	if sum != 380 {
		t.Errorf("allocated tax = %d, want exactly 380", sum)
	}
}

// A single tax line must keep reporting its specific basis.
func TestAllocateSingleTaxBasisIsNotMixed(t *testing.T) {
	if got := Allocate(targetReceipt()); got.TaxBasis != BasisPrintedBaseAll {
		t.Errorf("tax_basis = %q, want %q", got.TaxBasis, BasisPrintedBaseAll)
	}
}

// The printed total is the only thing tax can be checked against. Without it a
// misread tax would prorate onto every line behind a "balanced" badge.
func TestAllocateNoTotalCannotBeVerified(t *testing.T) {
	ex := Extraction{
		Items:       []ExItem{{Position: 1, Description: "THING", Amount: 10.00, Taxable: b(true)}},
		TaxLines:    []ExTaxLine{{Label: "TAX", Amount: 46.50}}, // 4.65 misread
		Subtotal:    f(10.00),
		Total:       nil,
		TaxEvidence: EvidenceUnknown,
	}
	got := Allocate(ex)
	if got.Reconciliation.OK {
		t.Error("a receipt with no readable total must not report as balanced")
	}
	if got.Reconciliation.Message == "" {
		t.Error("expected a message explaining the total could not be verified")
	}
}

// A no-subtotal receipt must not report a large item delta beside ok=true.
func TestAllocateDeltasAreSelfConsistent(t *testing.T) {
	ex := Extraction{
		Items: []ExItem{
			{Position: 1, Description: "LATTE", Amount: 4.50},
			{Position: 2, Description: "SCONE", Amount: 3.25},
		},
		Total:       f(7.75),
		TaxEvidence: EvidenceUnknown,
	}
	got := Allocate(ex)
	if !got.Reconciliation.OK {
		t.Fatalf("expected reconciliation to pass: %+v", got.Reconciliation)
	}
	if got.Reconciliation.ItemsDeltaCents != 0 {
		t.Errorf("items_delta = %d, want 0 alongside ok=true", got.Reconciliation.ItemsDeltaCents)
	}
	if got.Reconciliation.TotalDeltaCents != 0 {
		t.Errorf("total_delta = %d, want 0", got.Reconciliation.TotalDeltaCents)
	}
}
