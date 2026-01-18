package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"my-personal-budget/internal/auth"
	"my-personal-budget/internal/store"
)

type MCPHandler struct {
	store MCPStore
}

type MCPStore interface {
	ListBudgets(ctx context.Context, userID *int64) ([]store.Budget, error)
	CreateTransaction(ctx context.Context, budgetID int64, userID *int64, description string, credit bool, amount float64) (store.Transaction, error)
}

func NewMCPHandler(store MCPStore) http.Handler {
	return &MCPHandler{store: store}
}

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

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
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	var req mcpRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeMCPError(w, nil, -32700, "invalid JSON payload")
		return
	}
	if req.JSONRPC != "" && req.JSONRPC != "2.0" {
		writeMCPError(w, req.ID, -32600, "invalid jsonrpc version")
		return
	}
	switch req.Method {
	case "initialize":
		h.handleInitialize(w, req.ID)
	case "tools/list":
		h.handleToolsList(w, req.ID)
	case "tools/call":
		h.handleToolsCall(w, r, req.ID, req.Params)
	case "ping":
		writeMCPResult(w, req.ID, map[string]any{"now": time.Now().UTC()})
	default:
		writeMCPError(w, req.ID, -32601, "method not found")
	}
}

func (h *MCPHandler) handleInitialize(w http.ResponseWriter, id any) {
	result := map[string]any{
		"protocolVersion": "2024-11-05",
		"serverInfo": map[string]any{
			"name":    "My Personal Budget MCP",
			"version": "1.0",
		},
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
	}
	writeMCPResult(w, id, result)
}

func (h *MCPHandler) handleToolsList(w http.ResponseWriter, id any) {
	tools := []map[string]any{
		{
			"name":        "list_budgets",
			"description": "List budgets visible to the API key.",
			"inputSchema": map[string]any{
				"type":                 "object",
				"properties":           map[string]any{},
				"additionalProperties": false,
			},
		},
		{
			"name":        "add_transaction",
			"description": "Add a transaction to a budget you can access.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"budget_id": map[string]any{"type": "integer"},
					"description": map[string]any{
						"type": "string",
					},
					"amount": map[string]any{"type": "number"},
					"credit": map[string]any{"type": "boolean"},
				},
				"required":             []string{"budget_id", "description", "amount", "credit"},
				"additionalProperties": false,
			},
		},
	}
	writeMCPResult(w, id, map[string]any{"tools": tools})
}

func (h *MCPHandler) handleToolsCall(w http.ResponseWriter, r *http.Request, id any, params json.RawMessage) {
	var payload struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		writeMCPError(w, id, -32602, "invalid tool call payload")
		return
	}

	switch payload.Name {
	case "list_budgets":
		h.callListBudgets(w, r, id)
	case "add_transaction":
		h.callAddTransaction(w, r, id, payload.Arguments)
	default:
		writeMCPError(w, id, -32601, "unknown tool")
	}
}

func (h *MCPHandler) callListBudgets(w http.ResponseWriter, r *http.Request, id any) {
	userID := auth.UserIDFromContext(r.Context())
	if userID == nil {
		writeMCPError(w, id, -32001, "unauthorized")
		return
	}
	budgets, err := h.store.ListBudgets(r.Context(), userID)
	if err != nil {
		writeMCPError(w, id, -32000, "failed to list budgets")
		return
	}
	type budgetOut struct {
		ID      int64   `json:"id"`
		Name    string  `json:"name"`
		Balance float64 `json:"balance"`
		Payroll float64 `json:"payroll"`
	}
	out := make([]budgetOut, 0, len(budgets))
	for _, b := range budgets {
		out = append(out, budgetOut{
			ID:      b.ID,
			Name:    b.Name,
			Balance: b.Balance,
			Payroll: b.Payroll,
		})
	}
	writeMCPResult(w, id, map[string]any{
		"content": []map[string]any{
			{
				"type": "text",
				"text": mustJSON(out),
			},
		},
	})
}

func (h *MCPHandler) callAddTransaction(w http.ResponseWriter, r *http.Request, id any, args json.RawMessage) {
	userID := auth.UserIDFromContext(r.Context())
	if userID == nil {
		writeMCPError(w, id, -32001, "unauthorized")
		return
	}
	var req struct {
		BudgetID    int64   `json:"budget_id"`
		Description string  `json:"description"`
		Amount      float64 `json:"amount"`
		Credit      bool    `json:"credit"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		writeMCPError(w, id, -32602, "invalid arguments")
		return
	}
	req.Description = strings.TrimSpace(req.Description)
	if req.BudgetID <= 0 || req.Description == "" || req.Amount <= 0 {
		writeMCPError(w, id, -32602, "budget_id, description, and amount must be provided")
		return
	}
	txn, err := h.store.CreateTransaction(r.Context(), req.BudgetID, userID, req.Description, req.Credit, req.Amount)
	if errors.Is(err, store.ErrNotFound) {
		writeMCPError(w, id, -32004, "budget not found")
		return
	}
	if err != nil {
		writeMCPError(w, id, -32000, "failed to create transaction")
		return
	}
	out := map[string]any{
		"id":          txn.ID,
		"budget_id":   txn.BudgetID,
		"description": txn.Description,
		"credit":      txn.Credit,
		"amount":      txn.Amount,
		"created_at":  txn.CreatedAt,
	}
	writeMCPResult(w, id, map[string]any{
		"content": []map[string]any{
			{
				"type": "text",
				"text": mustJSON(out),
			},
		},
	})
}

func writeMCPResult(w http.ResponseWriter, id any, result any) {
	writeMCPResponse(w, mcpResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func writeMCPError(w http.ResponseWriter, id any, code int, message string) {
	writeMCPResponse(w, mcpResponse{JSONRPC: "2.0", ID: id, Error: &mcpError{Code: code, Message: message}})
}

func writeMCPResponse(w http.ResponseWriter, resp mcpResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func mustJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}
