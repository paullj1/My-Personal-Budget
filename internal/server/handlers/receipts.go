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
	Items          []scanItem             `json:"items"`
	Image          receipt.NormalizeInfo  `json:"image"`
	Model          string                 `json:"model"`
	ElapsedMS      int64                  `json:"elapsed_ms"`
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
	case rest == "scan":
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
		respondError(w, http.StatusUnsupportedMediaType, "could not read that image")
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
		PurchasedAt:    alloc.PurchasedAt,
		Currency:       alloc.Currency,
		SubtotalCents:  alloc.SubtotalCents,
		TaxCents:       alloc.TaxCents,
		AdjustCents:    alloc.AdjustCents,
		TotalCents:     alloc.TotalCents,
		TaxEvidence:    alloc.TaxEvidence,
		TaxBasis:       alloc.TaxBasis,
		Reconciliation: alloc.Reconciliation,
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
