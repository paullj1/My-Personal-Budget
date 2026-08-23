package handlers

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"

	"my-personal-budget/internal/receipt"
)

func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(prevOut); log.SetFlags(prevFlags) })
	fn()
	return buf.String()
}

// The blind spot this replaced: only failures were logged, so an extraction that
// balanced but was wrong left no record of how many items came back.
func TestLogScanOutcomeLogsSuccessfulScans(t *testing.T) {
	ex := receipt.Extraction{Items: []receipt.ExItem{
		{Position: 1, Description: "MILK", Amount: 3.50},
		{Position: 2, Description: "BREAD", Amount: 2.25},
	}}
	alloc := receipt.Allocation{
		Merchant:      "Target",
		SubtotalCents: 575,
		TaxCents:      35,
		TotalCents:    610,
		TaxEvidence:   receipt.EvidenceUnknown,
		Reconciliation: receipt.Reconciliation{
			OK: true, ItemsSumCents: 575, SubtotalCents: 575, PrintedCents: 610,
		},
	}
	info := receipt.NormalizeInfo{Width: 737, Height: 2048, Cropped: true}

	out := captureLog(t, func() { logScanOutcome(alloc, ex, info, 31*time.Second) })

	for _, want := range []string{"receipt scan ok", "items=2", "737x2048", "cropped=true", `merchant="Target"`} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "MISMATCH") {
		t.Errorf("a reconciling scan should not be flagged:\n%s", out)
	}
}

// A single line equal to the printed subtotal reconciles, so the item count is the
// only signal that a receipt was collapsed. It must appear on the ok path.
func TestLogScanOutcomeSurfacesCollapsedExtraction(t *testing.T) {
	ex := receipt.Extraction{Items: []receipt.ExItem{{Position: 1, Description: "MERCHANDISE", Amount: 205.35}}}
	alloc := receipt.Allocation{
		Merchant: "LOWE'S", SubtotalCents: 20535, TaxCents: 1233, TotalCents: 21768,
		Reconciliation: receipt.Reconciliation{OK: true, ItemsSumCents: 20535},
	}
	out := captureLog(t, func() {
		logScanOutcome(alloc, ex, receipt.NormalizeInfo{Width: 100, Height: 200}, time.Second)
	})
	if !strings.Contains(out, "items=1") {
		t.Errorf("item count is the only clue a receipt collapsed:\n%s", out)
	}
}

func TestLogScanOutcomeReportsMismatchDetail(t *testing.T) {
	ex := receipt.Extraction{Items: make([]receipt.ExItem, 14)}
	alloc := receipt.Allocation{
		Merchant: "Brasserie B", SubtotalCents: 42100, TotalCents: 45626,
		Reconciliation: receipt.Reconciliation{
			OK:              false,
			Message:         "items sum to 420.00 but the subtotal reads 421.00.",
			ItemsSumCents:   42000,
			ItemsDeltaCents: -100,
			TotalDeltaCents: -100,
		},
	}
	info := receipt.NormalizeInfo{Width: 761, Height: 1191, Cropped: true}
	out := captureLog(t, func() { logScanOutcome(alloc, ex, info, 53*time.Second) })

	for _, want := range []string{"MISMATCH", "items=14", "items_delta=-1.00", "subtotal reads 421.00"} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q:\n%s", want, out)
		}
	}
	// The old version printed `1 lines: ""` on every failure. Nothing should claim
	// a transcription that the schema no longer requests.
	if strings.Contains(out, `transcription=""`) || strings.Contains(out, "lines:") {
		t.Errorf("log reports an empty transcription as if it were data:\n%s", out)
	}
}

// Detection failing is the strongest predictor of a bad read, so its reason should
// travel with the mismatch rather than needing a separate lookup.
func TestLogScanOutcomeIncludesDetectReasonWhenUncropped(t *testing.T) {
	alloc := receipt.Allocation{
		TotalCents:     100,
		Reconciliation: receipt.Reconciliation{OK: false, Message: "no total was readable"},
	}
	info := receipt.NormalizeInfo{
		Width: 3024, Height: 4032, Cropped: false,
		Detect: receipt.DetectInfo{Reason: "bright region fills the frame; nothing to crop"},
	}
	out := captureLog(t, func() { logScanOutcome(alloc, receipt.Extraction{}, info, time.Second) })
	if !strings.Contains(out, "detect=") {
		t.Errorf("expected the detector's reason on an uncropped mismatch:\n%s", out)
	}
}

func TestCentsStringFormatsMoney(t *testing.T) {
	cases := map[int]string{0: "0.00", 5: "0.05", 100: "1.00", 42100: "421.00", -100: "-1.00", -5: "-0.05"}
	for in, want := range cases {
		if got := centsString(in); got != want {
			t.Errorf("centsString(%d) = %q, want %q", in, got, want)
		}
	}
}
