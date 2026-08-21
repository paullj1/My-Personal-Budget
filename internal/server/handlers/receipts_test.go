package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"my-personal-budget/internal/auth"
	"my-personal-budget/internal/config"
	"my-personal-budget/internal/passkey"
	"my-personal-budget/internal/receipt"
	"my-personal-budget/internal/store"
)

type fakeExtractor struct {
	extraction receipt.Extraction
	err        error
	gotImage   []byte
}

func (f *fakeExtractor) Extract(ctx context.Context, img []byte) (receipt.Extraction, error) {
	f.gotImage = img
	if f.err != nil {
		return receipt.Extraction{}, f.err
	}
	return f.extraction, nil
}

func fp(v float64) *float64 { return &v }
func bp(v bool) *bool       { return &v }
func sp(v string) *string   { return &v }

// targetExtraction is what qwen3.8:27b actually returned for the Target receipt.
func targetExtraction() receipt.Extraction {
	return receipt.Extraction{
		Merchant:    sp("Columbia"),
		PurchasedAt: sp("2026-08-20"),
		Items: []receipt.ExItem{
			{Position: 1, LineText: "203800178 GATORADE TF $6.99", Description: "GATORADE", Amount: 6.99, Taxable: bp(true), Marker: sp("TF")},
			{Position: 2, LineText: "203220654 Good&Gather TF $3.69", Description: "Good&Gather", Amount: 3.69, Taxable: bp(true), Marker: sp("TF")},
			{Position: 3, LineText: "072081663 ELEC KETTLE T $29.99", Description: "ELEC KETTLE", Amount: 29.99, Taxable: bp(true), Marker: sp("T")},
			{Position: 4, LineText: "072080526 Bodum T $29.99", Description: "Bodum", Amount: 29.99, Taxable: bp(true), Marker: sp("T")},
		},
		TaxLines:    []receipt.ExTaxLine{{Label: "MD TAX", Rate: fp(0.06), Base: fp(70.66), Amount: 4.24}},
		Subtotal:    fp(70.66),
		Total:       fp(74.90),
		TaxEvidence: receipt.EvidencePerLineFlags,
	}
}

func scanHandler(t *testing.T, fs *fakeStore, ex receipt.Extractor) *APIHandler {
	t.Helper()
	cfg := config.Config{
		JWTSecret:       "test-secret",
		ReceiptOCRModel: "qwen3.8:27b",
		ReceiptMaxEdge:  1600,
		ReceiptMaxBytes: 8 << 20,
		RelyingPartyID:  "localhost",
	}
	if ex != nil {
		cfg.ReceiptOCRURL = "http://stub"
	}
	h, err := NewAPIHandler(cfg, fs, passkey.NewChallengeStore())
	if err != nil {
		t.Fatalf("NewAPIHandler: %v", err)
	}
	// Replace the real client with the stub; NewAPIHandler builds one whenever a
	// URL is configured.
	h.extractor = ex
	return h
}

func withUser(r *http.Request, id int64) *http.Request {
	return r.WithContext(auth.WithUserID(r.Context(), id))
}

func pngBytes(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 200, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func multipartImage(t *testing.T, field string, data []byte) (string, *bytes.Buffer) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile(field, "receipt.png")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatalf("write part: %v", err)
	}
	mw.Close()
	return mw.FormDataContentType(), &body
}

func TestScanReceiptReturnsAllocatedDraft(t *testing.T) {
	fs := &fakeStore{suggestions: map[string]int64{"GATORADE TF": 7}}
	ex := &fakeExtractor{extraction: targetExtraction()}
	h := scanHandler(t, fs, ex)

	ct, body := multipartImage(t, "image", pngBytes(800, 600))
	req := withUser(httptest.NewRequest(http.MethodPost, "/receipts/scan", body), 1)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got scanResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if !got.Reconciliation.OK {
		t.Errorf("expected reconciliation to pass, got %+v", got.Reconciliation)
	}
	if got.TotalCents != 7490 || got.TaxCents != 424 {
		t.Errorf("totals = %d/%d, want 7490/424", got.TotalCents, got.TaxCents)
	}
	if got.TaxBasis != receipt.BasisPrintedBaseAll {
		t.Errorf("tax_basis = %q, want the printed base to win", got.TaxBasis)
	}
	if len(got.Items) != 4 {
		t.Fatalf("items = %d, want 4", len(got.Items))
	}

	wantTax := []int{42, 22, 180, 180}
	sum := 0
	for idx, it := range got.Items {
		if it.TaxCents != wantTax[idx] {
			t.Errorf("item %d tax = %d, want %d", idx, it.TaxCents, wantTax[idx])
		}
		sum += it.TotalCents
	}
	if sum != 7490 {
		t.Errorf("item totals sum to %d, want the printed 7490", sum)
	}

	// History-based suggestion is attached, and only where history exists.
	if got.Items[0].SuggestedBudgetID == nil || *got.Items[0].SuggestedBudgetID != 7 {
		t.Errorf("expected GATORADE to be suggested budget 7, got %v", got.Items[0].SuggestedBudgetID)
	}
	if got.Items[0].SuggestionSource != "history" {
		t.Errorf("suggestion_source = %q, want history", got.Items[0].SuggestionSource)
	}
	if got.Items[3].SuggestedBudgetID != nil {
		t.Error("unseen item should carry no suggestion rather than a guess")
	}
	if got.PurchasedAt == nil {
		t.Error("expected the printed date to be parsed")
	}
	// The image must be normalized before it reaches the model.
	if len(ex.gotImage) == 0 {
		t.Error("extractor received no image")
	}
	if got.Image.Width == 0 {
		t.Error("expected normalization info in the response")
	}
}

func TestScanReceiptSurfacesReconciliationFailure(t *testing.T) {
	extraction := targetExtraction()
	// Drop two items: the exact single-pass failure seen in Phase 0.
	extraction.Items = extraction.Items[2:]
	h := scanHandler(t, &fakeStore{}, &fakeExtractor{extraction: extraction})

	ct, body := multipartImage(t, "image", pngBytes(400, 300))
	req := withUser(httptest.NewRequest(http.MethodPost, "/receipts/scan", body), 1)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)

	// A mismatch still returns 200: the user can see the receipt and fix it.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 so the user can correct it", rec.Code)
	}
	var got scanResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Reconciliation.OK {
		t.Error("expected reconciliation to fail with items missing")
	}
	if got.Reconciliation.Message == "" {
		t.Error("expected a message the UI can show")
	}
}

func TestScanReceiptDisabledWithoutConfig(t *testing.T) {
	h := scanHandler(t, &fakeStore{}, nil)
	ct, body := multipartImage(t, "image", pngBytes(100, 100))
	req := withUser(httptest.NewRequest(http.MethodPost, "/receipts/scan", body), 1)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when RECEIPT_OCR_URL is unset", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "RECEIPT_OCR_URL") {
		t.Errorf("error should name the missing setting: %s", rec.Body.String())
	}
}

func TestScanReceiptInferenceFailureIsBadGateway(t *testing.T) {
	h := scanHandler(t, &fakeStore{}, &fakeExtractor{err: fmt.Errorf("%w: dial tcp: refused", receipt.ErrUnavailable)})
	ct, body := multipartImage(t, "image", pngBytes(200, 150))
	req := withUser(httptest.NewRequest(http.MethodPost, "/receipts/scan", body), 1)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	// The message must point at the fallback, not leak internals.
	if !strings.Contains(rec.Body.String(), "by hand") {
		t.Errorf("expected a fallback hint, got %s", rec.Body.String())
	}
}

func TestScanReceiptRejectsBadInput(t *testing.T) {
	cases := []struct {
		name       string
		build      func(t *testing.T) (string, *bytes.Buffer)
		wantStatus int
	}{
		{
			name: "not an image",
			build: func(t *testing.T) (string, *bytes.Buffer) {
				return multipartImage(t, "image", []byte("hello, not a picture"))
			},
			wantStatus: http.StatusUnsupportedMediaType,
		},
		{
			name: "wrong field name",
			build: func(t *testing.T) (string, *bytes.Buffer) {
				return multipartImage(t, "photo", pngBytes(100, 100))
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "empty body",
			build: func(t *testing.T) (string, *bytes.Buffer) {
				return "application/octet-stream", &bytes.Buffer{}
			},
			wantStatus: http.StatusBadRequest,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := scanHandler(t, &fakeStore{}, &fakeExtractor{extraction: targetExtraction()})
			ct, body := tc.build(t)
			req := withUser(httptest.NewRequest(http.MethodPost, "/receipts/scan", body), 1)
			req.Header.Set("Content-Type", ct)
			rec := httptest.NewRecorder()
			h.Router().ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

func TestScanReceiptRejectsOversizeImage(t *testing.T) {
	h := scanHandler(t, &fakeStore{}, &fakeExtractor{extraction: targetExtraction()})
	h.cfg.ReceiptMaxBytes = 512

	ct, body := multipartImage(t, "image", pngBytes(400, 400))
	req := withUser(httptest.NewRequest(http.MethodPost, "/receipts/scan", body), 1)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestScanReceiptAcceptsRawImageBody(t *testing.T) {
	h := scanHandler(t, &fakeStore{}, &fakeExtractor{extraction: targetExtraction()})
	req := withUser(httptest.NewRequest(http.MethodPost, "/receipts/scan", bytes.NewReader(pngBytes(300, 200))), 1)
	req.Header.Set("Content-Type", "image/png")
	rec := httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestScanReceiptSurvivesSuggestionFailure(t *testing.T) {
	fs := &fakeStore{suggestErr: errors.New("db down")}
	h := scanHandler(t, fs, &fakeExtractor{extraction: targetExtraction()})

	ct, body := multipartImage(t, "image", pngBytes(300, 200))
	req := withUser(httptest.NewRequest(http.MethodPost, "/receipts/scan", body), 1)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)

	// Suggestions are a convenience; the scan itself must still succeed.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want the scan to survive a suggestion failure", rec.Code)
	}
	var got scanResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, it := range got.Items {
		if it.SuggestedBudgetID != nil {
			t.Error("expected no suggestions when the lookup failed")
		}
	}
}

func TestScanReceiptMethodNotAllowed(t *testing.T) {
	h := scanHandler(t, &fakeStore{}, &fakeExtractor{extraction: targetExtraction()})
	req := withUser(httptest.NewRequest(http.MethodGet, "/receipts/scan", nil), 1)
	rec := httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestCommitReceiptPassesThroughToStore(t *testing.T) {
	fs := &fakeStore{commitResult: store.CommitReceiptResult{
		Receipt:   store.Receipt{ID: 12, TotalCents: 7490},
		BudgetIDs: []int64{3, 4},
	}}
	h := scanHandler(t, fs, nil)

	payload := map[string]any{
		"merchant":            "Target Columbia",
		"purchased_at":        "2026-08-20",
		"catch_all_budgetID":  nil,
		"catch_all_budget_id": 9,
		"tax_evidence":        "per_line_flags",
		"tax_basis":           "printed_base_all_items",
		"items": []map[string]any{
			{"position": 1, "budget_id": 3, "line_text": "203800178 GATORADE TF $6.99",
				"description": "GATORADE", "amount_cents": 699, "tax_cents": 42},
			{"position": 2, "budget_id": 4, "description": "Bodum", "amount_cents": 2999, "tax_cents": 180},
		},
	}
	body, _ := json.Marshal(payload)
	req := withUser(httptest.NewRequest(http.MethodPost, "/receipts", bytes.NewReader(body)), 1)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if fs.committedReceipt == nil {
		t.Fatal("store never received the receipt")
	}
	in := *fs.committedReceipt
	if in.CatchAllBudgetID != 9 {
		t.Errorf("catch-all = %d, want 9", in.CatchAllBudgetID)
	}
	if len(in.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(in.Items))
	}
	if in.PurchasedAt == nil {
		t.Error("expected the printed date to be parsed for backdating")
	}
	// norm_key must be derived when the client omits it, so suggestions still learn.
	if in.Items[0].NormKey != "GATORADE TF" {
		t.Errorf("norm_key = %q, want it derived from line_text", in.Items[0].NormKey)
	}
	if in.Items[1].NormKey != "BODUM" {
		t.Errorf("norm_key = %q, want it derived from description", in.Items[1].NormKey)
	}
}

func TestCommitReceiptValidation(t *testing.T) {
	cases := []struct {
		name       string
		payload    map[string]any
		wantStatus int
	}{
		{
			name:       "no catch-all",
			payload:    map[string]any{"items": []map[string]any{{"description": "x", "amount_cents": 100}}},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "no items",
			payload:    map[string]any{"catch_all_budget_id": 1, "items": []map[string]any{}},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "zero total",
			payload: map[string]any{"catch_all_budget_id": 1, "items": []map[string]any{
				{"description": "free", "amount_cents": 500, "adjust_cents": -500}}},
			wantStatus: http.StatusBadRequest,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := scanHandler(t, &fakeStore{}, nil)
			body, _ := json.Marshal(tc.payload)
			req := withUser(httptest.NewRequest(http.MethodPost, "/receipts", bytes.NewReader(body)), 1)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			h.Router().ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

func TestCommitReceiptMalformedJSON(t *testing.T) {
	h := scanHandler(t, &fakeStore{}, nil)
	req := withUser(httptest.NewRequest(http.MethodPost, "/receipts", strings.NewReader("{not json")), 1)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestCommitReceiptMapsStoreErrors(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{"inaccessible budget", store.ErrNotFound, http.StatusNotFound},
		{"no items", store.ErrNoReceiptItems, http.StatusBadRequest},
		{"unexpected", errors.New("boom"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := scanHandler(t, &fakeStore{commitErr: tc.err}, nil)
			body, _ := json.Marshal(map[string]any{
				"catch_all_budget_id": 1,
				"items":               []map[string]any{{"description": "x", "amount_cents": 100}},
			})
			req := withUser(httptest.NewRequest(http.MethodPost, "/receipts", bytes.NewReader(body)), 1)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			h.Router().ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
		})
	}
}

func TestGetReceipt(t *testing.T) {
	fs := &fakeStore{
		receipt:      &store.Receipt{ID: 5, Merchant: "Target", TotalCents: 7490},
		receiptItems: []store.CommittedReceiptItem{{ID: 1, Description: "GATORADE", TotalCents: 741}},
	}
	h := scanHandler(t, fs, nil)
	req := withUser(httptest.NewRequest(http.MethodGet, "/receipts/5", nil), 1)
	rec := httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "GATORADE") {
		t.Errorf("expected items in the response: %s", rec.Body.String())
	}

	h2 := scanHandler(t, &fakeStore{}, nil)
	req2 := withUser(httptest.NewRequest(http.MethodGet, "/receipts/5", nil), 1)
	rec2 := httptest.NewRecorder()
	h2.Router().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec2.Code)
	}

	h3 := scanHandler(t, fs, nil)
	req3 := withUser(httptest.NewRequest(http.MethodGet, "/receipts/abc", nil), 1)
	rec3 := httptest.NewRecorder()
	h3.Router().ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a non-numeric id", rec3.Code)
	}
}

func TestIndexReportsScanCapability(t *testing.T) {
	for _, tc := range []struct {
		name string
		ex   receipt.Extractor
		want bool
	}{
		{"configured", &fakeExtractor{}, true},
		{"not configured", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := scanHandler(t, &fakeStore{}, tc.ex)
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			rec := httptest.NewRecorder()
			h.Router().ServeHTTP(rec, req)
			var got struct {
				Features struct {
					ReceiptScan bool `json:"receipt_scan"`
				} `json:"features"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.Features.ReceiptScan != tc.want {
				t.Errorf("receipt_scan = %v, want %v", got.Features.ReceiptScan, tc.want)
			}
		})
	}
}

// A chunked request declares ContentLength -1, so the length check passes and
// ParseMultipartForm would otherwise spool without bound to temp files.
func TestScanReceiptBoundsChunkedUpload(t *testing.T) {
	h := scanHandler(t, &fakeStore{}, &fakeExtractor{extraction: targetExtraction()})

	ct, body := multipartImage(t, "image", pngBytes(1200, 1200))
	// The limit must sit below the real body size, or the request is simply legal.
	h.cfg.ReceiptMaxBytes = int64(body.Len() / 2)

	req := withUser(httptest.NewRequest(http.MethodPost, "/receipts/scan", body), 1)
	req.Header.Set("Content-Type", ct)
	// Exactly what a streaming client looks like: no declared length, so the
	// ContentLength check cannot catch it.
	req.ContentLength = -1
	req.TransferEncoding = []string{"chunked"}

	rec := httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 for an unbounded chunked upload (%s)", rec.Code, rec.Body.String())
	}
}

func TestScanReceiptRejectsOversizedPixelDimensions(t *testing.T) {
	h := scanHandler(t, &fakeStore{}, &fakeExtractor{extraction: targetExtraction()})

	// Small on the wire, enormous when decoded.
	huge := image.NewGray(image.Rect(0, 0, 20000, 20000))
	var raw bytes.Buffer
	if err := png.Encode(&raw, huge); err != nil {
		t.Skipf("could not build the fixture: %v", err)
	}
	h.cfg.ReceiptMaxBytes = int64(raw.Len()) + 4096

	ct, body := multipartImage(t, "image", raw.Bytes())
	req := withUser(httptest.NewRequest(http.MethodPost, "/receipts/scan", body), 1)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 for a decompression bomb (%s)", rec.Code, rec.Body.String())
	}
}

// The same bound must not reject a legitimately sized upload.
func TestScanReceiptAllowsChunkedUploadWithinLimit(t *testing.T) {
	h := scanHandler(t, &fakeStore{}, &fakeExtractor{extraction: targetExtraction()})
	ct, body := multipartImage(t, "image", pngBytes(600, 600))
	h.cfg.ReceiptMaxBytes = int64(body.Len()) * 4

	req := withUser(httptest.NewRequest(http.MethodPost, "/receipts/scan", body), 1)
	req.Header.Set("Content-Type", ct)
	req.ContentLength = -1
	req.TransferEncoding = []string{"chunked"}

	rec := httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
}

// The router grants ScanPath a longer handler deadline. If the constant and the
// real route ever drift, scans silently fall back to the short default, so pin
// them together: a request to ScanPath must reach the scan handler.
func TestScanPathRoutesToTheScanHandler(t *testing.T) {
	if ScanPath != "/receipts/scan" {
		t.Errorf("ScanPath = %q; update internal/server/router.go if this moved", ScanPath)
	}
	// Scanning disabled: 503 proves the request reached the handler, where 404
	// would mean the path never matched.
	h := scanHandler(t, &fakeStore{}, nil)
	req := withUser(httptest.NewRequest(http.MethodPost, ScanPath, nil), 1)
	rec := httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d at %s, want 503 from the scan handler", rec.Code, ScanPath)
	}
}
