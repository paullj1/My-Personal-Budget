package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"my-personal-budget/internal/auth"
	"my-personal-budget/internal/receipt"
	"my-personal-budget/internal/store"
)

// Protocol versions this server speaks, newest first. Streamable HTTP and the
// notification handling below assume at least 2025-03-26; the older revision is
// still listed because a client that opens with it works unchanged.
var supportedProtocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// defaultProtocolVersion is assumed when a request omits MCP-Protocol-Version,
// as the spec requires for backwards compatibility.
const defaultProtocolVersion = "2025-03-26"

// maxMCPBody bounds a JSON-RPC payload. An extraction for a long grocery
// receipt is the largest legitimate body and is well under this.
const maxMCPBody = 4 << 20

type MCPHandler struct {
	store  MCPStore
	drafts *receipt.DraftStore
}

type MCPStore interface {
	ListBudgets(ctx context.Context, userID *int64) ([]store.Budget, error)
	CreateTransaction(ctx context.Context, budgetID int64, userID *int64, description string, credit bool, amount float64) (store.Transaction, error)
	ListTransactionsFiltered(ctx context.Context, budgetID int64, userID *int64, f store.TransactionFilter) ([]store.Transaction, store.TransactionSummary, error)
	SuggestBudgets(ctx context.Context, userID *int64, keys []string) (map[string]int64, error)
	CommitReceipt(ctx context.Context, userID *int64, in store.ReceiptInput) (store.CommitReceiptResult, error)
}

func NewMCPHandler(store MCPStore) http.Handler {
	return &MCPHandler{
		store:  store,
		drafts: receipt.NewDraftStore(receipt.DefaultDraftTTL, 64),
	}
}

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// isNotification reports whether a message expects no reply. JSON-RPC says a
// notification is a request without an id, and answering one is a protocol
// error -- which is what the previous version of this handler did, replying
// "method not found" to the notifications/initialized every client sends right
// after initialize.
func (r mcpRequest) isNotification() bool { return r.ID == nil }

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id,omitempty"`
	Result  any       `json:"result,omitempty"`
	Error   *mcpError `json:"error,omitempty"`
}

func (h *MCPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
	case http.MethodGet, http.MethodDelete:
		// Streamable HTTP lets a server decline the server-initiated SSE stream
		// and session termination. This server has neither: every response is
		// produced synchronously from the POST that asked for it, and there is no
		// session state to tear down.
		methodNotAllowed(w, http.MethodPost)
		return
	default:
		methodNotAllowed(w, http.MethodPost)
		return
	}

	if version := strings.TrimSpace(r.Header.Get("MCP-Protocol-Version")); version != "" && !supportedVersion(version) {
		respondJSON(w, http.StatusBadRequest, map[string]any{
			"error":     "unsupported MCP-Protocol-Version",
			"supported": supportedProtocolVersions,
		})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxMCPBody))
	if err != nil {
		writeMCPError(w, nil, -32700, "could not read request body")
		return
	}

	// JSON-RPC batching was removed in 2025-06-18 but earlier clients still send
	// arrays, and a batch that fails to parse looks like a broken server.
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	if len(trimmed) > 0 && trimmed[0] == '[' {
		h.serveBatch(w, r, trimmed)
		return
	}

	var req mcpRequest
	if err := json.Unmarshal(trimmed, &req); err != nil {
		writeMCPError(w, nil, -32700, "invalid JSON payload")
		return
	}
	resp, ok := h.dispatch(r, req)
	if !ok {
		// A notification: acknowledge receipt and send nothing back.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeMCPResponse(w, resp)
}

func (h *MCPHandler) serveBatch(w http.ResponseWriter, r *http.Request, body []byte) {
	var reqs []mcpRequest
	if err := json.Unmarshal(body, &reqs); err != nil {
		writeMCPError(w, nil, -32700, "invalid JSON payload")
		return
	}
	if len(reqs) == 0 {
		writeMCPError(w, nil, -32600, "empty batch")
		return
	}
	responses := make([]mcpResponse, 0, len(reqs))
	for _, req := range reqs {
		if resp, ok := h.dispatch(r, req); ok {
			responses = append(responses, resp)
		}
	}
	if len(responses) == 0 {
		// Every message was a notification.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(responses); err != nil {
		log.Printf("mcp: encoding batch response: %v", err)
	}
}

// dispatch routes one message. The bool is false for notifications, which
// produce no response at all.
func (h *MCPHandler) dispatch(r *http.Request, req mcpRequest) (mcpResponse, bool) {
	if req.JSONRPC != "" && req.JSONRPC != "2.0" {
		return errorResponse(req.ID, -32600, "invalid jsonrpc version"), !req.isNotification()
	}
	if req.isNotification() {
		// Nothing this server does depends on a client notification, but they must
		// be accepted rather than answered.
		return mcpResponse{}, false
	}

	switch req.Method {
	case "initialize":
		return resultResponse(req.ID, h.initializeResult(req.Params)), true
	case "tools/list":
		return resultResponse(req.ID, map[string]any{"tools": mcpTools()}), true
	case "tools/call":
		return h.handleToolsCall(r, req.ID, req.Params), true
	case "ping":
		// The spec's ping takes and returns an empty object.
		return resultResponse(req.ID, map[string]any{}), true
	default:
		return errorResponse(req.ID, -32601, "method not found"), true
	}
}

func (h *MCPHandler) initializeResult(params json.RawMessage) map[string]any {
	// Echo the client's version when this server speaks it, so an older client
	// is not forced to downgrade a connection it opened correctly.
	version := supportedProtocolVersions[0]
	var req struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &req); err == nil && supportedVersion(req.ProtocolVersion) {
			version = req.ProtocolVersion
		}
	}
	return map[string]any{
		"protocolVersion": version,
		"serverInfo": map[string]any{
			"name":    "My Personal Budget MCP",
			"version": "1.1",
		},
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
	}
}

func supportedVersion(v string) bool {
	for _, known := range supportedProtocolVersions {
		if known == v {
			return true
		}
	}
	return false
}

// --- Tool definitions -----------------------------------------------------

// draftReceiptDescription tells the calling model how to read a receipt.
//
// A tool description is the only prompt a remote client ever sees, so the rules
// this app spent Phase 0 learning have to travel in it. receipt.ExtractionRules
// supplies them verbatim rather than a paraphrase.
func draftReceiptDescription() string {
	return strings.Join([]string{
		"Turn a receipt you have already read into a priced, per-line allocation.",
		"",
		"Call this when you can see the receipt image yourself: read it, structure it",
		"to the schema below, and send the facts. Do NOT do any arithmetic -- tax",
		"proration, discounts and the reconciliation check all happen server-side in",
		"integer cents, and a total you compute yourself will be discarded.",
		"",
		"The result reports whether the printed subtotal and total reconcile against",
		"the lines you sent. If they do not, you misread something: fix it and call",
		"again rather than committing.",
		"",
		"Returns a draft_id. Pass it to commit_receipt with your budget choices.",
		"",
		receipt.ExtractionRules(),
	}, "\n")
}

func mcpTools() []map[string]any {
	return []map[string]any{
		{
			"name":        "list_budgets",
			"description": "List budgets you can access, with their current balances.",
			"inputSchema": map[string]any{
				"type":                 "object",
				"properties":           map[string]any{},
				"additionalProperties": false,
			},
		},
		{
			"name":        "add_transaction",
			"description": "Add a single transaction to a budget you can access.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"budget_id":   map[string]any{"type": "integer"},
					"description": map[string]any{"type": "string"},
					"amount":      map[string]any{"type": "number", "description": "Positive amount in the budget's currency."},
					"credit":      map[string]any{"type": "boolean", "description": "True for money in, false for money out."},
				},
				"required":             []string{"budget_id", "description", "amount", "credit"},
				"additionalProperties": false,
			},
		},
		{
			"name": "list_transactions",
			"description": strings.Join([]string{
				"List transactions from a budget, newest first, with totals.",
				"",
				"Narrow with limit (the last N), and/or a from/to date range. Dates accept",
				"YYYY-MM-DD or RFC 3339; a plain date in `to` covers that whole day. Both",
				"ends are inclusive and either may be omitted for an open range.",
				"",
				"The reply carries credit_total, debit_total and net over the rows returned,",
				"so a summary does not require adding them up. If more rows matched than the",
				"limit allowed, truncated is true and the totals cover only what came back.",
			}, "\n"),
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"budget_id": map[string]any{"type": "integer"},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Most recent N transactions. Default 100, max 500.",
					},
					"from": map[string]any{
						"type":        "string",
						"description": "Inclusive start, YYYY-MM-DD or RFC 3339.",
					},
					"to": map[string]any{
						"type":        "string",
						"description": "Inclusive end, YYYY-MM-DD or RFC 3339. A plain date covers the whole day.",
					},
					"search": map[string]any{
						"type":        "string",
						"description": "Case-insensitive substring match on the description.",
					},
				},
				"required":             []string{"budget_id"},
				"additionalProperties": false,
			},
		},
		{
			"name":        "draft_receipt",
			"description": draftReceiptDescription(),
			"inputSchema": receipt.ExtractionSchema(),
		},
		{
			"name": "commit_receipt",
			"description": strings.Join([]string{
				"Commit a draft from draft_receipt, writing one transaction per budget.",
				"",
				"Assign lines to budgets by position. Anything you leave unassigned goes to",
				"catch_all_budget_id, so every cent lands somewhere. Amounts come from the",
				"draft and cannot be overridden here.",
				"",
				"A draft that failed reconciliation is refused unless accept_unreconciled is",
				"true; prefer re-reading the receipt over forcing it through.",
			}, "\n"),
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"draft_id": map[string]any{"type": "string"},
					"catch_all_budget_id": map[string]any{
						"type":        "integer",
						"description": "Budget that receives every line you did not assign.",
					},
					"assignments": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"position":  map[string]any{"type": "integer"},
								"budget_id": map[string]any{"type": "integer"},
							},
							"required":             []string{"position", "budget_id"},
							"additionalProperties": false,
						},
					},
					"accept_unreconciled": map[string]any{"type": "boolean"},
				},
				"required":             []string{"draft_id", "catch_all_budget_id"},
				"additionalProperties": false,
			},
		},
	}
}

// --- Tool dispatch --------------------------------------------------------

func (h *MCPHandler) handleToolsCall(r *http.Request, id any, params json.RawMessage) mcpResponse {
	var payload struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return errorResponse(id, -32602, "invalid tool call payload")
	}
	userID := auth.UserIDFromContext(r.Context())
	if userID == nil {
		return errorResponse(id, -32001, "unauthorized")
	}

	switch payload.Name {
	case "list_budgets":
		return h.callListBudgets(r, id, userID)
	case "add_transaction":
		return h.callAddTransaction(r, id, userID, payload.Arguments)
	case "list_transactions":
		return h.callListTransactions(r, id, userID, payload.Arguments)
	case "draft_receipt":
		return h.callDraftReceipt(r, id, userID, payload.Arguments)
	case "commit_receipt":
		return h.callCommitReceipt(r, id, userID, payload.Arguments)
	default:
		return errorResponse(id, -32601, "unknown tool")
	}
}

func (h *MCPHandler) callListBudgets(r *http.Request, id any, userID *int64) mcpResponse {
	budgets, err := h.store.ListBudgets(r.Context(), userID)
	if err != nil {
		return errorResponse(id, -32000, "failed to list budgets")
	}
	type budgetOut struct {
		ID      int64   `json:"id"`
		Name    string  `json:"name"`
		Balance float64 `json:"balance"`
		Payroll float64 `json:"payroll"`
	}
	out := make([]budgetOut, 0, len(budgets))
	for _, b := range budgets {
		out = append(out, budgetOut{ID: b.ID, Name: b.Name, Balance: b.Balance, Payroll: b.Payroll})
	}
	return toolResult(id, out)
}

func (h *MCPHandler) callAddTransaction(r *http.Request, id any, userID *int64, args json.RawMessage) mcpResponse {
	var req struct {
		BudgetID    int64   `json:"budget_id"`
		Description string  `json:"description"`
		Amount      float64 `json:"amount"`
		Credit      bool    `json:"credit"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return errorResponse(id, -32602, "invalid arguments")
	}
	req.Description = strings.TrimSpace(req.Description)
	if req.BudgetID <= 0 || req.Description == "" || req.Amount <= 0 {
		return errorResponse(id, -32602, "budget_id, description, and amount must be provided")
	}
	txn, err := h.store.CreateTransaction(r.Context(), req.BudgetID, userID, req.Description, req.Credit, req.Amount)
	if errors.Is(err, store.ErrNotFound) {
		return errorResponse(id, -32004, "budget not found")
	}
	if err != nil {
		return errorResponse(id, -32000, "failed to create transaction")
	}
	return toolResult(id, map[string]any{
		"id":          txn.ID,
		"budget_id":   txn.BudgetID,
		"description": txn.Description,
		"credit":      txn.Credit,
		"amount":      txn.Amount,
		"created_at":  txn.CreatedAt,
	})
}

// parseFilterDate accepts the forms a caller actually sends: a plain date, or a
// full timestamp. endOfDay makes a bare date on the `to` side cover its whole
// day, so "to: 2026-03-31" does not silently exclude everything after midnight.
func parseFilterDate(raw string, endOfDay bool) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if t, err := time.ParseInLocation("2006-01-02", raw, time.Local); err == nil {
		if endOfDay {
			t = t.Add(24*time.Hour - time.Nanosecond)
		}
		return &t, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return &t, nil
		}
	}
	return nil, fmt.Errorf("could not read %q as a date; use YYYY-MM-DD or RFC 3339", raw)
}

func (h *MCPHandler) callListTransactions(r *http.Request, id any, userID *int64, args json.RawMessage) mcpResponse {
	var req struct {
		BudgetID int64  `json:"budget_id"`
		Limit    int    `json:"limit"`
		From     string `json:"from"`
		To       string `json:"to"`
		Search   string `json:"search"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return errorResponse(id, -32602, "invalid arguments")
	}
	if req.BudgetID <= 0 {
		return errorResponse(id, -32602, "budget_id is required")
	}
	from, err := parseFilterDate(req.From, false)
	if err != nil {
		return errorResponse(id, -32602, err.Error())
	}
	to, err := parseFilterDate(req.To, true)
	if err != nil {
		return errorResponse(id, -32602, err.Error())
	}
	if from != nil && to != nil && to.Before(*from) {
		return errorResponse(id, -32602, "to is earlier than from")
	}

	txns, sum, err := h.store.ListTransactionsFiltered(r.Context(), req.BudgetID, userID, store.TransactionFilter{
		Limit: req.Limit, From: from, To: to, Search: req.Search,
	})
	if errors.Is(err, store.ErrNotFound) {
		return errorResponse(id, -32004, "budget not found")
	}
	if err != nil {
		log.Printf("mcp list_transactions: %v", err)
		return errorResponse(id, -32000, "failed to list transactions")
	}

	type txnOut struct {
		ID          int64     `json:"id"`
		Description string    `json:"description"`
		Credit      bool      `json:"credit"`
		Amount      float64   `json:"amount"`
		CreatedAt   time.Time `json:"created_at"`
	}
	out := make([]txnOut, 0, len(txns))
	for _, t := range txns {
		out = append(out, txnOut{t.ID, t.Description, t.Credit, t.Amount, t.CreatedAt})
	}
	return toolResult(id, map[string]any{
		"budget_id":    req.BudgetID,
		"summary":      sum,
		"transactions": out,
	})
}

// draftLine is one allocated line as the caller sees it: what it is, what it
// costs all-in, and which budget the app would guess for it.
type draftLine struct {
	Position          int    `json:"position"`
	Description       string `json:"description"`
	AmountCents       int    `json:"amount_cents"`
	TaxCents          int    `json:"tax_cents"`
	AdjustCents       int    `json:"adjust_cents"`
	TotalCents        int    `json:"total_cents"`
	SuggestedBudgetID *int64 `json:"suggested_budget_id,omitempty"`
	SuggestionSource  string `json:"suggestion_source,omitempty"`
}

func (h *MCPHandler) callDraftReceipt(r *http.Request, id any, userID *int64, args json.RawMessage) mcpResponse {
	var extraction receipt.Extraction
	if err := json.Unmarshal(args, &extraction); err != nil {
		return errorResponse(id, -32602, "arguments do not match the extraction schema")
	}
	if len(extraction.Items) == 0 {
		return errorResponse(id, -32602, "at least one item is required")
	}

	alloc := receipt.Allocate(extraction)

	keys := make([]string, 0, len(alloc.Lines))
	for _, l := range alloc.Lines {
		keys = append(keys, l.NormKey)
	}
	suggestions, err := h.store.SuggestBudgets(r.Context(), userID, keys)
	if err != nil {
		// Suggestions are a convenience; losing them must not fail the draft.
		log.Printf("mcp: receipt budget suggestions failed: %v", err)
		suggestions = map[string]int64{}
	}

	draftID, err := h.drafts.Put(receipt.Draft{
		UserID:      *userID,
		Alloc:       alloc,
		Extraction:  extraction,
		Suggestions: suggestions,
	})
	if err != nil {
		return errorResponse(id, -32000, "failed to store draft")
	}

	lines := make([]draftLine, 0, len(alloc.Lines))
	for _, l := range alloc.Lines {
		line := draftLine{
			Position:    l.Position,
			Description: l.Description,
			AmountCents: l.AmountCents,
			TaxCents:    l.TaxCents,
			AdjustCents: l.AdjustCents,
			TotalCents:  l.TotalCents,
		}
		if budgetID, found := suggestions[l.NormKey]; found {
			b := budgetID
			line.SuggestedBudgetID = &b
			line.SuggestionSource = "history"
		}
		lines = append(lines, line)
	}

	log.Printf("mcp draft_receipt: merchant=%q lines=%d total=%s reconciled=%t basis=%s",
		alloc.Merchant, len(alloc.Lines), centsString(alloc.TotalCents),
		alloc.Reconciliation.OK, alloc.TaxBasis)

	return toolResult(id, map[string]any{
		"draft_id":       draftID,
		"merchant":       alloc.Merchant,
		"purchased_at":   plausibleReceiptDate(alloc.PurchasedAt, time.Now()),
		"currency":       alloc.Currency,
		"subtotal_cents": alloc.SubtotalCents,
		"tax_cents":      alloc.TaxCents,
		"adjust_cents":   alloc.AdjustCents,
		"total_cents":    alloc.TotalCents,
		"tax_evidence":   alloc.TaxEvidence,
		"tax_basis":      alloc.TaxBasis,
		"reconciliation": alloc.Reconciliation,
		"notes":          alloc.Notes,
		"items":          lines,
		"expires_in_s":   int(receipt.DefaultDraftTTL.Seconds()),
	})
}

func (h *MCPHandler) callCommitReceipt(r *http.Request, id any, userID *int64, args json.RawMessage) mcpResponse {
	var req struct {
		DraftID          string `json:"draft_id"`
		CatchAllBudgetID int64  `json:"catch_all_budget_id"`
		Assignments      []struct {
			Position int   `json:"position"`
			BudgetID int64 `json:"budget_id"`
		} `json:"assignments"`
		AcceptUnreconciled bool `json:"accept_unreconciled"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return errorResponse(id, -32602, "invalid arguments")
	}
	if strings.TrimSpace(req.DraftID) == "" {
		return errorResponse(id, -32602, "draft_id is required")
	}
	if req.CatchAllBudgetID <= 0 {
		return errorResponse(id, -32602, "catch_all_budget_id is required")
	}

	draft, ok := h.drafts.Get(strings.TrimSpace(req.DraftID), *userID)
	if !ok {
		return errorResponse(id, -32004, "that draft expired or does not exist; call draft_receipt again")
	}
	if !draft.Alloc.Reconciliation.OK && !req.AcceptUnreconciled {
		return errorResponse(id, -32005, fmt.Sprintf(
			"this receipt does not reconcile (%s); re-read it, or pass accept_unreconciled to commit it as-is",
			draft.Alloc.Reconciliation.Message))
	}

	assigned := make(map[int]int64, len(req.Assignments))
	for _, a := range req.Assignments {
		if a.BudgetID > 0 {
			assigned[a.Position] = a.BudgetID
		}
	}

	// Amounts come from the draft, never from the request: the caller chooses
	// budgets, this side owns the cents.
	items := make([]store.ReceiptItemInput, 0, len(draft.Alloc.Lines))
	for _, l := range draft.Alloc.Lines {
		item := store.ReceiptItemInput{
			Position:    l.Position,
			LineText:    l.LineText,
			NormKey:     l.NormKey,
			Description: l.Description,
			Marker:      l.Marker,
			Taxable:     l.Taxable,
			AmountCents: l.AmountCents,
			TaxCents:    l.TaxCents,
			AdjustCents: l.AdjustCents,
		}
		if budgetID, found := assigned[l.Position]; found {
			b := budgetID
			item.BudgetID = &b
		} else if budgetID, found := draft.Suggestions[l.NormKey]; found {
			b := budgetID
			item.BudgetID = &b
		}
		items = append(items, item)
	}

	parsed, err := json.Marshal(draft.Extraction)
	if err != nil {
		parsed = []byte(`{}`)
	}

	result, err := h.store.CommitReceipt(r.Context(), userID, store.ReceiptInput{
		Merchant:         draft.Alloc.Merchant,
		PurchasedAt:      plausibleReceiptDate(draft.Alloc.PurchasedAt, time.Now()),
		Currency:         draft.Alloc.Currency,
		CatchAllBudgetID: req.CatchAllBudgetID,
		TaxEvidence:      draft.Alloc.TaxEvidence,
		TaxBasis:         draft.Alloc.TaxBasis,
		Reconciled:       draft.Alloc.Reconciliation.OK,
		ExtractionSource: store.SourceClientSupplied,
		Parsed:           parsed,
		Items:            items,
	})
	switch {
	case errors.Is(err, store.ErrNoReceiptItems):
		return errorResponse(id, -32602, "at least one item is required")
	case errors.Is(err, store.ErrNotFound):
		return errorResponse(id, -32004, "budget not found")
	case err != nil:
		log.Printf("mcp commit_receipt: %v", err)
		return errorResponse(id, -32000, "failed to save receipt")
	}

	// The draft has served its purpose; keeping it would let the same receipt be
	// committed twice.
	h.drafts.Delete(strings.TrimSpace(req.DraftID))

	budgetIDs := append([]int64(nil), result.BudgetIDs...)
	sort.Slice(budgetIDs, func(i, j int) bool { return budgetIDs[i] < budgetIDs[j] })

	return toolResult(id, map[string]any{
		"receipt_id":   result.Receipt.ID,
		"merchant":     result.Receipt.Merchant,
		"total_cents":  result.Receipt.TotalCents,
		"reconciled":   result.Receipt.Reconciled,
		"budget_ids":   budgetIDs,
		"transactions": len(result.Transactions),
		"items":        len(result.Items),
	})
}

// --- Response helpers -----------------------------------------------------

// toolResult wraps a value in the content block shape tools/call returns.
func toolResult(id any, value any) mcpResponse {
	return resultResponse(id, map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": mustJSON(value)},
		},
	})
}

func resultResponse(id any, result any) mcpResponse {
	return mcpResponse{JSONRPC: "2.0", ID: id, Result: result}
}

func errorResponse(id any, code int, message string) mcpResponse {
	return mcpResponse{JSONRPC: "2.0", ID: id, Error: &mcpError{Code: code, Message: message}}
}

func writeMCPError(w http.ResponseWriter, id any, code int, message string) {
	writeMCPResponse(w, errorResponse(id, code, message))
}

func writeMCPResponse(w http.ResponseWriter, resp mcpResponse) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("mcp: encoding response: %v", err)
	}
}

func mustJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}
