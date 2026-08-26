package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"my-personal-budget/internal/auth"
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

func TestMCPToolsList(t *testing.T) {
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
	for _, want := range []string{"list_budgets", "add_transaction"} {
		if _, ok := byName[want]; !ok {
			t.Fatalf("tool %s is missing from tools/list", want)
		}
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
