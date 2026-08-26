package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"my-personal-budget/internal/auth"
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
