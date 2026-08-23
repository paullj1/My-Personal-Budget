package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"my-personal-budget/internal/receipt"
	"my-personal-budget/internal/store"
)

// scanResponse is the draft handed back for review. Nothing is persisted until
// the user confirms, so a bad scan costs only time.
type scanResponse struct {
	Merchant       string                 `json:"merchant"`
	PurchasedAt    *time.Time             `json:"purchased_at"`
	Currency       string                 `json:"currency"`
	SubtotalCents  int                    `json:"subtotal_cents"`
	TaxCents       int                    `json:"tax_cents"`
	AdjustCents    int                    `json:"adjust_cents"`
	TotalCents     int                    `json:"total_cents"`
	TaxEvidence    string                 `json:"tax_evidence"`
	TaxBasis       string                 `json:"tax_basis"`
	Reconciliation receipt.Reconciliation `json:"reconciliation"`
	// Notes records corrections made while allocating, so an adjustment that was
	// dropped for not matching the printed total is visible rather than silent.
	Notes     []string              `json:"notes,omitempty"`
	Items     []scanItem            `json:"items"`
	Image     receipt.NormalizeInfo `json:"image"`
	Model     string                `json:"model"`
	ElapsedMS int64                 `json:"elapsed_ms"`
}

type scanItem struct {
	Position          int    `json:"position"`
	LineText          string `json:"line_text"`
	NormKey           string `json:"norm_key"`
	Description       string `json:"description"`
	Marker            string `json:"marker,omitempty"`
	Taxable           *bool  `json:"taxable"`
	AmountCents       int    `json:"amount_cents"`
	TaxCents          int    `json:"tax_cents"`
	AdjustCents       int    `json:"adjust_cents"`
	TotalCents        int    `json:"total_cents"`
	SuggestedBudgetID *int64 `json:"suggested_budget_id"`
	SuggestionSource  string `json:"suggestion_source,omitempty"`
}

// ScanPath is the API-relative path of the scan endpoint. The router needs it to
// grant that route a longer handler deadline, so it lives here next to the
// handler rather than being spelled out again at the call site.
const ScanPath = "/receipts/scan"

// handleReceipts serves the collection routes.
func (h *APIHandler) handleReceipts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.commitReceipt(w, r)
	default:
		methodNotAllowed(w, http.MethodPost)
	}
}

// handleReceiptByPath serves /receipts/scan and /receipts/{id}.
func (h *APIHandler) handleReceiptByPath(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/receipts/")
	switch {
	case rest == strings.TrimPrefix(ScanPath, "/receipts/"):
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		h.scanReceipt(w, r)
	case rest != "":
		id, err := strconv.ParseInt(strings.Split(rest, "/")[0], 10, 64)
		if err != nil {
			respondError(w, http.StatusBadRequest, "invalid receipt id")
			return
		}
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		h.getReceipt(w, r, id)
	default:
		respondError(w, http.StatusNotFound, "not found")
	}
}

func (h *APIHandler) scanReceipt(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	if h.extractor == nil {
		respondError(w, http.StatusServiceUnavailable,
			"receipt scanning is not configured; set RECEIPT_OCR_URL to enable it")
		return
	}

	maxBytes := h.cfg.ReceiptMaxBytes
	if maxBytes <= 0 {
		maxBytes = 16 << 20
	}
	image, err := readUploadedImage(r, maxBytes)
	if err != nil {
		respondError(w, statusForUpload(err), err.Error())
		return
	}

	// Normalize server-side even though the browser already does: a direct API
	// caller that skips it would silently lose half the items on a tall receipt.
	normalized, info, err := receipt.Normalize(image, h.cfg.ReceiptMaxEdge)
	if errors.Is(err, receipt.ErrImageTooLarge) {
		respondError(w, http.StatusRequestEntityTooLarge, err.Error())
		return
	}
	if err != nil {
		// The browser re-encodes to JPEG before upload, so this is mostly for direct
		// API callers -- naming the formats saves them guessing. HEIC in particular
		// is common straight off an iPhone and cannot be decoded here.
		respondError(w, http.StatusUnsupportedMediaType,
			"could not read that image; JPEG and PNG are supported")
		return
	}

	started := time.Now()
	extraction, err := h.extractor.Extract(r.Context(), normalized)
	elapsed := time.Since(started)
	if err != nil {
		log.Printf("receipt scan failed after %s: %v", elapsed.Round(time.Millisecond), err)
		status := http.StatusBadGateway
		if r.Context().Err() != nil {
			status = http.StatusGatewayTimeout
		}
		// The client falls back to pre-filled manual entry either way, so the
		// message matters more than the code.
		respondError(w, status, "could not read that receipt automatically; enter the items by hand")
		return
	}

	alloc := receipt.Allocate(extraction)
	logScanOutcome(alloc, extraction, info, elapsed)

	keys := make([]string, 0, len(alloc.Lines))
	for _, l := range alloc.Lines {
		keys = append(keys, l.NormKey)
	}
	suggestions, err := h.store.SuggestBudgets(r.Context(), userID, keys)
	if err != nil {
		// Suggestions are a convenience; losing them must not fail the scan.
		log.Printf("receipt budget suggestions failed: %v", err)
		suggestions = map[string]int64{}
	}

	resp := scanResponse{
		Merchant:       alloc.Merchant,
		PurchasedAt:    plausibleReceiptDate(alloc.PurchasedAt, time.Now()),
		Currency:       alloc.Currency,
		SubtotalCents:  alloc.SubtotalCents,
		TaxCents:       alloc.TaxCents,
		AdjustCents:    alloc.AdjustCents,
		TotalCents:     alloc.TotalCents,
		TaxEvidence:    alloc.TaxEvidence,
		TaxBasis:       alloc.TaxBasis,
		Reconciliation: alloc.Reconciliation,
		Notes:          alloc.Notes,
		Image:          info,
		Model:          h.cfg.ReceiptOCRModel,
		ElapsedMS:      elapsed.Milliseconds(),
		Items:          make([]scanItem, 0, len(alloc.Lines)),
	}
	for _, l := range alloc.Lines {
		item := scanItem{
			Position:    l.Position,
			LineText:    l.LineText,
			NormKey:     l.NormKey,
			Description: l.Description,
			Marker:      l.Marker,
			Taxable:     l.Taxable,
			AmountCents: l.AmountCents,
			TaxCents:    l.TaxCents,
			AdjustCents: l.AdjustCents,
			TotalCents:  l.TotalCents,
		}
		if budgetID, found := suggestions[l.NormKey]; found {
			id := budgetID
			item.SuggestedBudgetID = &id
			item.SuggestionSource = "history"
		}
		resp.Items = append(resp.Items, item)
	}
	respondJSON(w, http.StatusOK, resp)
}

type commitReceiptRequest struct {
	Merchant         string  `json:"merchant"`
	PurchasedAt      *string `json:"purchased_at"`
	Currency         string  `json:"currency"`
	CatchAllBudgetID int64   `json:"catch_all_budget_id"`
	TaxEvidence      string  `json:"tax_evidence"`
	TaxBasis         string  `json:"tax_basis"`
	Reconciled       *bool   `json:"reconciled"`
	Items            []struct {
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
	} `json:"items"`
}

func (h *APIHandler) commitReceipt(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	var req commitReceiptRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON payload")
		return
	}
	if req.CatchAllBudgetID <= 0 {
		respondError(w, http.StatusBadRequest, "catch_all_budget_id is required")
		return
	}
	if len(req.Items) == 0 {
		respondError(w, http.StatusBadRequest, "at least one item is required")
		return
	}

	items := make([]store.ReceiptItemInput, 0, len(req.Items))
	net := 0
	for idx, in := range req.Items {
		desc := strings.TrimSpace(in.Description)
		if desc == "" {
			desc = strings.TrimSpace(in.LineText)
		}
		if desc == "" {
			desc = fmt.Sprintf("Item %d", idx+1)
		}
		normKey := strings.TrimSpace(in.NormKey)
		if normKey == "" {
			// Derived here so suggestions still learn from hand-entered lines.
			normKey = receipt.NormKey(in.LineText)
			if normKey == "" {
				normKey = receipt.NormKey(desc)
			}
		}
		position := in.Position
		if position == 0 {
			position = idx + 1
		}
		items = append(items, store.ReceiptItemInput{
			Position:    position,
			BudgetID:    in.BudgetID,
			LineText:    strings.TrimSpace(in.LineText),
			NormKey:     normKey,
			Description: desc,
			Marker:      strings.TrimSpace(in.Marker),
			Taxable:     in.Taxable,
			AmountCents: in.AmountCents,
			TaxCents:    in.TaxCents,
			AdjustCents: in.AdjustCents,
		})
		net += in.AmountCents + in.TaxCents + in.AdjustCents
	}
	if net <= 0 {
		respondError(w, http.StatusBadRequest, "receipt total must be greater than zero")
		return
	}

	reconciled := true
	if req.Reconciled != nil {
		reconciled = *req.Reconciled
	}

	result, err := h.store.CommitReceipt(r.Context(), userID, store.ReceiptInput{
		Merchant:         strings.TrimSpace(req.Merchant),
		PurchasedAt:      receipt.ParsePurchasedAt(req.PurchasedAt),
		Currency:         req.Currency,
		CatchAllBudgetID: req.CatchAllBudgetID,
		TaxEvidence:      req.TaxEvidence,
		TaxBasis:         req.TaxBasis,
		Reconciled:       reconciled,
		Model:            h.cfg.ReceiptOCRModel,
		Items:            items,
	})
	switch {
	case errors.Is(err, store.ErrNoReceiptItems):
		respondError(w, http.StatusBadRequest, "at least one item is required")
		return
	case errors.Is(err, store.ErrNotFound):
		respondError(w, http.StatusNotFound, "budget not found")
		return
	case err != nil:
		log.Printf("commit receipt: %v", err)
		respondError(w, http.StatusInternalServerError, "failed to save receipt")
		return
	}
	respondJSON(w, http.StatusCreated, result)
}

func (h *APIHandler) getReceipt(w http.ResponseWriter, r *http.Request, id int64) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	rec, items, err := h.store.GetReceipt(r.Context(), id, userID)
	if errors.Is(err, store.ErrNotFound) {
		respondError(w, http.StatusNotFound, "receipt not found")
		return
	}
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load receipt")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"receipt": rec, "items": items})
}

// readUploadedImage accepts either a multipart form field named "image" or a raw
// image body, so curl and the browser can both post one.
func readUploadedImage(r *http.Request, maxBytes int64) ([]byte, error) {
	contentType := r.Header.Get("Content-Type")
	if r.ContentLength > maxBytes {
		return nil, fmt.Errorf("image exceeds the %d byte limit", maxBytes)
	}

	// A chunked request declares ContentLength -1, so the check above passes and
	// ParseMultipartForm would spool without bound to temp files. Cap the body
	// itself, which holds regardless of how the length was declared.
	r.Body = http.MaxBytesReader(nil, r.Body, maxBytes+1)

	if strings.HasPrefix(contentType, "multipart/form-data") {
		if err := r.ParseMultipartForm(maxBytes); err != nil {
			if strings.Contains(err.Error(), "too large") {
				return nil, fmt.Errorf("image exceeds the %d byte limit", maxBytes)
			}
			return nil, fmt.Errorf("could not read the uploaded image")
		}
		file, _, err := r.FormFile("image")
		if err != nil {
			return nil, fmt.Errorf("no image field in the upload")
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
		if err != nil {
			return nil, fmt.Errorf("could not read the uploaded image")
		}
		if int64(len(data)) > maxBytes {
			return nil, fmt.Errorf("image exceeds the %d byte limit", maxBytes)
		}
		if len(data) == 0 {
			return nil, fmt.Errorf("uploaded image is empty")
		}
		return data, nil
	}

	data, err := io.ReadAll(io.LimitReader(r.Body, maxBytes+1))
	if err != nil {
		if strings.Contains(err.Error(), "too large") {
			return nil, fmt.Errorf("image exceeds the %d byte limit", maxBytes)
		}
		return nil, fmt.Errorf("could not read the request body")
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("image exceeds the %d byte limit", maxBytes)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("no image supplied")
	}
	return data, nil
}

func statusForUpload(err error) int {
	if strings.Contains(err.Error(), "exceeds the") {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}

// plausibleReceiptDate filters a model-suggested date.
//
// The date sits in small, low-contrast print and is misread often enough not to
// be trusted: on a clear photo reading "08/20/2026 08:40 AM" the model
// transcribed "05/20/2026 10:41". A wrong month is silent and books spending to
// the wrong period, so anything implausible is dropped and the review field is
// left empty for the user to fill rather than pre-filled with a guess.
//
// This only screens the suggestion. A date the user types on commit is their
// explicit choice and is never second-guessed.
func plausibleReceiptDate(t *time.Time, now time.Time) *time.Time {
	if t == nil {
		return nil
	}
	// A day of slack absorbs timezone differences between the receipt and here.
	if t.After(now.AddDate(0, 0, 1)) {
		return nil
	}
	if t.Before(now.AddDate(-2, 0, 0)) {
		return nil
	}
	return t
}

// logScanOutcome records one line for every scan, successful or not.
//
// Logging only failures left a real blind spot. Reconciliation checks arithmetic,
// not completeness: a single line equal to the printed subtotal balances perfectly,
// so an extraction that collapsed a six-item receipt into one bogus line passes and
// says nothing. When a scan was reported as wrong in production there was no record
// of how many items came back, which is the first thing worth knowing.
//
// One line, always, greppable: item count and the geometry that produced it, since
// whether the crop succeeded is the strongest predictor of extraction quality.
func logScanOutcome(alloc receipt.Allocation, ex receipt.Extraction, info receipt.NormalizeInfo, elapsed time.Duration) {
	outcome := "ok"
	if !alloc.Reconciliation.OK {
		outcome = "MISMATCH"
	}
	merchant := alloc.Merchant
	if merchant == "" {
		merchant = "?"
	}

	log.Printf("receipt scan %s in %s: items=%d image=%dx%d cropped=%v "+
		"items_sum=%s subtotal=%s tax=%s total=%s evidence=%s basis=%s merchant=%q%s",
		outcome, elapsed.Round(time.Millisecond), len(ex.Items),
		info.Width, info.Height, info.Cropped,
		centsString(alloc.Reconciliation.ItemsSumCents), centsString(alloc.SubtotalCents),
		centsString(alloc.TaxCents), centsString(alloc.TotalCents),
		alloc.TaxEvidence, alloc.TaxBasis, merchant,
		reconcileDetail(alloc, info, ex))
}

// reconcileDetail appends why a scan did not add up, and the things worth having
// when it did not: the detector's reasoning, and any transcription the model
// volunteered. The schema no longer asks for one, so this is usually empty --
// the previous version printed `1 lines: ""` on every failure, which read like
// information and was not.
func reconcileDetail(alloc receipt.Allocation, info receipt.NormalizeInfo, ex receipt.Extraction) string {
	if alloc.Reconciliation.OK {
		return ""
	}
	detail := fmt.Sprintf(" | %s items_delta=%s total_delta=%s",
		alloc.Reconciliation.Message,
		centsString(alloc.Reconciliation.ItemsDeltaCents),
		centsString(alloc.Reconciliation.TotalDeltaCents))
	if !info.Cropped && info.Detect.Reason != "" {
		// An uncropped frame spends most of its pixels on background, which is the
		// usual reason a scan reads badly.
		detail += fmt.Sprintf(" detect=%q", info.Detect.Reason)
	}
	if t := strings.TrimSpace(ex.Transcription); t != "" {
		detail += fmt.Sprintf(" transcription=%q", t)
	}
	return detail
}

// centsString renders integer cents for logs. Money is held in cents throughout,
// so formatting it as a float anywhere risks that habit leaking into arithmetic.
func centsString(cents int) string {
	sign := ""
	if cents < 0 {
		sign, cents = "-", -cents
	}
	return fmt.Sprintf("%s%d.%02d", sign, cents/100, cents%100)
}
