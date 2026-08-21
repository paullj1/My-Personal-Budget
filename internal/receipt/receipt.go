// Package receipt turns an extracted receipt into per-budget allocations.
//
// The model that reads the photo reports facts only: line amounts, taxability
// markers, and the tax lines as printed. All arithmetic happens here, in integer
// cents, so the allocation is exact and reproducible. See
// docs/receipt-scan-design.md for why the split is drawn there.
package receipt

import (
	"math"
	"strings"
	"time"
	"unicode"
)

// Extraction mirrors the JSON schema handed to the vision model. Pointers mark
// fields the model is expected to leave null rather than guess.
type Extraction struct {
	Merchant    *string     `json:"merchant"`
	PurchasedAt *string     `json:"purchased_at"`
	Currency    *string     `json:"currency"`
	Items       []ExItem    `json:"items"`
	Adjustments []ExAdjust  `json:"adjustments"`
	TaxLines    []ExTaxLine `json:"tax_lines"`
	Subtotal    *float64    `json:"subtotal"`
	Total       *float64    `json:"total"`
	TaxEvidence string      `json:"tax_evidence"`
	Notes       []string    `json:"notes,omitempty"`
}

type ExItem struct {
	Position    int      `json:"position"`
	LineText    string   `json:"line_text"`
	Description string   `json:"description"`
	Quantity    *float64 `json:"quantity"`
	UnitPrice   *float64 `json:"unit_price"`
	Amount      float64  `json:"amount"`
	Taxable     *bool    `json:"taxable"`
	Marker      *string  `json:"marker"`
}

type ExAdjust struct {
	Label             string  `json:"label"`
	Amount            float64 `json:"amount"`
	AppliesToPosition *int    `json:"applies_to_position"`
}

type ExTaxLine struct {
	Label  string   `json:"label"`
	Rate   *float64 `json:"rate"`
	Base   *float64 `json:"base"`
	Amount float64  `json:"amount"`
}

// Tax evidence values the model may report, describing what the receipt showed.
const (
	EvidencePerLineFlags = "per_line_flags"
	EvidenceSingleRate   = "single_rate"
	EvidenceMultiRate    = "multi_rate"
	EvidenceUnknown      = "unknown"
)

// How the taxable set was chosen, surfaced so the UI can explain itself.
const (
	BasisPrintedBaseAll    = "printed_base_all_items"
	BasisPrintedBaseSubset = "printed_base_marker_subset"
	BasisMarkers           = "per_line_markers"
	BasisImpliedRate       = "rate_implied_base"
	BasisAllItems          = "all_items_proration"
	BasisNoTax             = "no_tax"
	// BasisMixed means several tax lines were allocated on different evidence.
	BasisMixed = "mixed"
)

// Line is one receipt item with its share of tax and discounts folded in.
type Line struct {
	Position    int    `json:"position"`
	LineText    string `json:"line_text"`
	NormKey     string `json:"norm_key"`
	Description string `json:"description"`
	Marker      string `json:"marker,omitempty"`
	Taxable     *bool  `json:"taxable"`
	AmountCents int    `json:"amount_cents"`
	TaxCents    int    `json:"tax_cents"`
	AdjustCents int    `json:"adjust_cents"`
	TotalCents  int    `json:"total_cents"`
}

// Reconciliation is the validator that decides whether an extraction is
// trustworthy. It is far more reliable than model self-confidence, which
// measured 1.0 on a 50%-wrong answer during Phase 0.
type Reconciliation struct {
	OK              bool   `json:"ok"`
	ItemsSumCents   int    `json:"items_sum_cents"`
	SubtotalCents   int    `json:"subtotal_cents"`
	ItemsDeltaCents int    `json:"items_delta_cents"`
	ComputedCents   int    `json:"computed_total_cents"`
	PrintedCents    int    `json:"printed_total_cents"`
	TotalDeltaCents int    `json:"total_delta_cents"`
	Message         string `json:"message,omitempty"`
}

// Allocation is the fully-computed receipt: every cent assigned to exactly one line.
type Allocation struct {
	Merchant       string         `json:"merchant"`
	PurchasedAt    *time.Time     `json:"purchased_at"`
	Currency       string         `json:"currency"`
	Lines          []Line         `json:"lines"`
	SubtotalCents  int            `json:"subtotal_cents"`
	TaxCents       int            `json:"tax_cents"`
	AdjustCents    int            `json:"adjust_cents"`
	TotalCents     int            `json:"total_cents"`
	TaxEvidence    string         `json:"tax_evidence"`
	TaxBasis       string         `json:"tax_basis"`
	Reconciliation Reconciliation `json:"reconciliation"`
	Notes          []string       `json:"notes,omitempty"`
}

// Cents converts a printed decimal amount to integer cents. Every downstream
// calculation uses cents; floats never participate in distribution.
func Cents(v float64) int {
	return int(math.Round(v * 100))
}

// centsPtr treats a missing value as zero, which is what an absent printed
// subtotal or total means for reconciliation purposes.
func centsPtr(v *float64) int {
	if v == nil {
		return 0
	}
	return Cents(*v)
}

// NormKey reduces an item line to a stable matching key for budget suggestions:
// upper-cased, digits and punctuation stripped, whitespace collapsed. So
// "GRND BF 93/7   8.99 T" and "GRND BF 93/7  9.49 T" share a key.
func NormKey(s string) string {
	var b strings.Builder
	prevSpace := true
	for _, r := range strings.ToUpper(s) {
		switch {
		case unicode.IsLetter(r) || r == '&':
			b.WriteRune(r)
			prevSpace = false
		case unicode.IsSpace(r), unicode.IsDigit(r), unicode.IsPunct(r), unicode.IsSymbol(r):
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// ParsePurchasedAt accepts the date formats receipts actually print. A receipt
// with no readable date returns nil so the caller can fall back to now.
func ParsePurchasedAt(s *string) *time.Time {
	if s == nil {
		return nil
	}
	raw := strings.TrimSpace(*s)
	if raw == "" {
		return nil
	}
	layouts := []string{
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006-01-02",
		"01/02/2006 03:04 PM",
		"01/02/2006 15:04",
		"01/02/2006",
		"01/02/06",
	}
	for _, l := range layouts {
		if t, err := time.ParseInLocation(l, raw, time.Local); err == nil {
			return &t
		}
	}
	return nil
}
