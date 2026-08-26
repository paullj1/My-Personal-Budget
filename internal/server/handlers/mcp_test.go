package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"my-personal-budget/internal/auth"
	"my-personal-budget/internal/receipt"
	"my-personal-budget/internal/store"
)

// mcpCall posts one JSON-RPC message as the given user.
func mcpCall(t *testing.T, h http.Handler, userID int64, body any) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(encoded))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithUserID(req.Context(), userID))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// toolCall runs a tools/call and returns the decoded envelope.
func toolCall(t *testing.T, h http.Handler, userID int64, name string, args any) mcpResponse {
	t.Helper()
	rr := mcpCall(t, h, userID, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": name, "arguments": args},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("tools/call %s: status %d body %s", name, rr.Code, rr.Body.String())
	}
	var resp mcpResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v (%s)", err, rr.Body.String())
	}
	return resp
}

// toolPayload pulls the JSON a tool returned out of its text content block.
func toolPayload(t *testing.T, resp mcpResponse) map[string]any {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("unexpected tool error: %d %s", resp.Error.Code, resp.Error.Message)
	}
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result is not an object: %#v", resp.Result)
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("result has no content: %#v", result)
	}
	first, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("content entry is not an object: %#v", content[0])
	}
	text, ok := first["text"].(string)
	if !ok {
		t.Fatalf("content entry has no text: %#v", first)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("tool payload is not JSON: %v (%s)", err, text)
	}
	return payload
}

// A client sends notifications/initialized right after initialize. Answering it
// with "method not found" -- which this handler used to do -- is a protocol
// error that some clients surface as a failed connection.
func TestMCPNotificationIsAcknowledgedNotAnswered(t *testing.T) {
	h := NewMCPHandler(&fakeStore{})
	rr := mcpCall(t, h, 1, map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})
	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
	if body := bytes.TrimSpace(rr.Body.Bytes()); len(body) != 0 {
		t.Fatalf("expected an empty body, got %s", body)
	}
}

func TestMCPInitializeEchoesSupportedVersion(t *testing.T) {
	h := NewMCPHandler(&fakeStore{})

	rr := mcpCall(t, h, 1, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"protocolVersion": "2025-03-26"},
	})
	var resp mcpResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	result := resp.Result.(map[string]any)
	if got := result["protocolVersion"]; got != "2025-03-26" {
		t.Fatalf("expected the client's version to be echoed, got %v", got)
	}

	// An unknown version falls back to this server's newest rather than agreeing
	// to something it cannot speak.
	rr = mcpCall(t, h, 1, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"protocolVersion": "1999-01-01"},
	})
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	result = resp.Result.(map[string]any)
	if got := result["protocolVersion"]; got != supportedProtocolVersions[0] {
		t.Fatalf("expected fallback to %s, got %v", supportedProtocolVersions[0], got)
	}
}

func TestMCPRejectsUnsupportedProtocolHeader(t *testing.T) {
	h := NewMCPHandler(&fakeStore{})
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)))
	req.Header.Set("MCP-Protocol-Version", "1999-01-01")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

// This server answers on the POST that asked, so it declines the server-initiated
// stream rather than leaving a GET hanging.
func TestMCPGetIsNotAllowed(t *testing.T) {
	h := NewMCPHandler(&fakeStore{})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/mcp", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rr.Code)
	}
	if allow := rr.Header().Get("Allow"); allow != http.MethodPost {
		t.Fatalf("expected Allow: POST, got %q", allow)
	}
}

func TestMCPToolsListCarriesTheExtractionContract(t *testing.T) {
	h := NewMCPHandler(&fakeStore{})
	rr := mcpCall(t, h, 1, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})

	var resp mcpResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	tools := resp.Result.(map[string]any)["tools"].([]any)
	byName := map[string]map[string]any{}
	for _, entry := range tools {
		tool := entry.(map[string]any)
		byName[tool["name"].(string)] = tool
	}
	for _, want := range []string{"list_budgets", "add_transaction", "draft_receipt", "commit_receipt"} {
		if _, ok := byName[want]; !ok {
			t.Fatalf("tool %s is missing from tools/list", want)
		}
	}

	// The rules a remote model reads must be the same ones the built-in backends
	// send, or the two extraction paths drift apart silently.
	desc := byName["draft_receipt"]["description"].(string)
	if !bytes.Contains([]byte(desc), []byte(receipt.ExtractionRules())) {
		t.Fatal("draft_receipt description does not carry the extraction rules verbatim")
	}
	schema := byName["draft_receipt"]["inputSchema"].(map[string]any)
	props := schema["properties"].(map[string]any)
	for _, want := range []string{"items", "tax_lines", "subtotal", "total", "tax_evidence"} {
		if _, ok := props[want]; !ok {
			t.Fatalf("draft_receipt input schema is missing %q", want)
		}
	}
}

// targetDraftArgs is the Target receipt as a client would send it: facts only,
// no arithmetic.
func targetDraftArgs(t *testing.T) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(targetExtraction())
	if err != nil {
		t.Fatalf("marshal extraction: %v", err)
	}
	var args map[string]any
	if err := json.Unmarshal(encoded, &args); err != nil {
		t.Fatalf("unmarshal extraction: %v", err)
	}
	return args
}

func TestMCPDraftReceiptAllocatesAndReconciles(t *testing.T) {
	fs := &fakeStore{suggestions: map[string]int64{}}
	h := NewMCPHandler(fs)

	payload := toolPayload(t, toolCall(t, h, 1, "draft_receipt", targetDraftArgs(t)))

	if payload["draft_id"] == nil || payload["draft_id"] == "" {
		t.Fatal("expected a draft_id")
	}
	// 70.66 subtotal + 4.24 tax, computed here rather than trusted from the caller.
	if got := payload["total_cents"].(float64); got != 7490 {
		t.Fatalf("total_cents: got %v want 7490", got)
	}
	if got := payload["tax_cents"].(float64); got != 424 {
		t.Fatalf("tax_cents: got %v want 424", got)
	}
	recon := payload["reconciliation"].(map[string]any)
	if ok := recon["ok"].(bool); !ok {
		t.Fatalf("expected the Target receipt to reconcile: %#v", recon)
	}
	items := payload["items"].([]any)
	if len(items) != 4 {
		t.Fatalf("expected 4 items, got %d", len(items))
	}
}

func TestMCPDraftReceiptRejectsEmptyExtraction(t *testing.T) {
	h := NewMCPHandler(&fakeStore{})
	resp := toolCall(t, h, 1, "draft_receipt", map[string]any{"items": []any{}})
	if resp.Error == nil {
		t.Fatal("expected an error for an extraction with no items")
	}
}

// The whole point of the draft is that the caller never re-sends the money: it
// chooses budgets, and the cents come from the allocation computed here.
func TestMCPCommitReceiptUsesDraftAmountsNotCallerAmounts(t *testing.T) {
	fs := &fakeStore{
		suggestions:  map[string]int64{},
		commitResult: store.CommitReceiptResult{Receipt: store.Receipt{ID: 7, TotalCents: 7490}},
	}
	h := NewMCPHandler(fs)

	draft := toolPayload(t, toolCall(t, h, 1, "draft_receipt", targetDraftArgs(t)))
	draftID := draft["draft_id"].(string)

	resp := toolCall(t, h, 1, "commit_receipt", map[string]any{
		"draft_id":            draftID,
		"catch_all_budget_id": 3,
		"assignments": []map[string]any{
			{"position": 3, "budget_id": 9},
			// A bogus amount here has nowhere to land: commit takes positions and
			// budget ids only.
			{"position": 4, "budget_id": 9, "amount_cents": 1},
		},
	})
	toolPayload(t, resp)

	if fs.committedReceipt == nil {
		t.Fatal("nothing was committed")
	}
	in := *fs.committedReceipt
	if in.ExtractionSource != store.SourceClientSupplied {
		t.Fatalf("extraction_source: got %q want %q", in.ExtractionSource, store.SourceClientSupplied)
	}
	if in.CatchAllBudgetID != 3 {
		t.Fatalf("catch_all_budget_id: got %d", in.CatchAllBudgetID)
	}
	if len(in.Items) != 4 {
		t.Fatalf("expected 4 items, got %d", len(in.Items))
	}

	total := 0
	for _, item := range in.Items {
		total += item.TotalCents()
	}
	if total != 7490 {
		t.Fatalf("committed total: got %d want 7490", total)
	}

	byPosition := map[int]store.ReceiptItemInput{}
	for _, item := range in.Items {
		byPosition[item.Position] = item
	}
	if b := byPosition[3].BudgetID; b == nil || *b != 9 {
		t.Fatalf("position 3 should have gone to budget 9, got %v", b)
	}
	// Unassigned lines are left nil so the store routes them to the catch-all.
	if b := byPosition[1].BudgetID; b != nil {
		t.Fatalf("position 1 should have been left unassigned, got %v", *b)
	}
	// The kettle line is 29.99 plus its share of tax, not the 1 cent the caller
	// tried to attach.
	if got := byPosition[3].AmountCents; got != 2999 {
		t.Fatalf("position 3 amount_cents: got %d want 2999", got)
	}
}

func TestMCPCommitReceiptRefusesUnreconciledDraft(t *testing.T) {
	fs := &fakeStore{suggestions: map[string]int64{}}
	h := NewMCPHandler(fs)

	// Drop a line from the extraction so the printed subtotal no longer matches.
	args := targetDraftArgs(t)
	items := args["items"].([]any)
	args["items"] = items[:3]

	draft := toolPayload(t, toolCall(t, h, 1, "draft_receipt", args))
	if draft["reconciliation"].(map[string]any)["ok"].(bool) {
		t.Fatal("expected a missing line to break reconciliation")
	}
	draftID := draft["draft_id"].(string)

	resp := toolCall(t, h, 1, "commit_receipt", map[string]any{
		"draft_id": draftID, "catch_all_budget_id": 3,
	})
	if resp.Error == nil {
		t.Fatal("expected an unreconciled draft to be refused")
	}
	if fs.committedReceipt != nil {
		t.Fatal("an unreconciled receipt was written anyway")
	}

	// The refusal is a guard rail, not a wall: it can be overridden deliberately.
	fs.commitResult = store.CommitReceiptResult{Receipt: store.Receipt{ID: 8}}
	resp = toolCall(t, h, 1, "commit_receipt", map[string]any{
		"draft_id": draftID, "catch_all_budget_id": 3, "accept_unreconciled": true,
	})
	toolPayload(t, resp)
	if fs.committedReceipt == nil {
		t.Fatal("accept_unreconciled did not let the commit through")
	}
	if fs.committedReceipt.Reconciled {
		t.Fatal("a forced commit should still be recorded as unreconciled")
	}
}

func TestMCPDraftIsNotVisibleToAnotherUser(t *testing.T) {
	fs := &fakeStore{suggestions: map[string]int64{}}
	h := NewMCPHandler(fs)

	draft := toolPayload(t, toolCall(t, h, 1, "draft_receipt", targetDraftArgs(t)))
	draftID := draft["draft_id"].(string)

	resp := toolCall(t, h, 2, "commit_receipt", map[string]any{
		"draft_id": draftID, "catch_all_budget_id": 3,
	})
	if resp.Error == nil {
		t.Fatal("another user was able to commit someone else's draft")
	}
	if fs.committedReceipt != nil {
		t.Fatal("a cross-user commit reached the store")
	}
}

func TestMCPCommitConsumesTheDraft(t *testing.T) {
	fs := &fakeStore{
		suggestions:  map[string]int64{},
		commitResult: store.CommitReceiptResult{Receipt: store.Receipt{ID: 7}},
	}
	h := NewMCPHandler(fs)

	draft := toolPayload(t, toolCall(t, h, 1, "draft_receipt", targetDraftArgs(t)))
	draftID := draft["draft_id"].(string)

	args := map[string]any{"draft_id": draftID, "catch_all_budget_id": 3}
	toolPayload(t, toolCall(t, h, 1, "commit_receipt", args))

	// Committing the same draft twice would double-charge the budgets.
	if resp := toolCall(t, h, 1, "commit_receipt", args); resp.Error == nil {
		t.Fatal("expected the second commit of the same draft to be refused")
	}
}

func TestMCPToolsRequireAuthentication(t *testing.T) {
	h := NewMCPHandler(&fakeStore{})
	req := httptest.NewRequest(http.MethodPost, "/mcp",
		bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_budgets","arguments":{}}}`)))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	var resp mcpResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != -32001 {
		t.Fatalf("expected an unauthorized error, got %#v", resp.Error)
	}
}
