package receipt

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeLlama struct {
	path   string
	body   map[string]any
	reply  string
	status int
	delay  time.Duration
}

func (f *fakeLlama) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.path = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &f.body)
		if f.delay > 0 {
			time.Sleep(f.delay)
		}
		if f.status != 0 {
			w.WriteHeader(f.status)
		}
		_, _ = w.Write([]byte(f.reply))
	}))
}

func oaiReply(content string) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{
			"message":       map[string]any{"role": "assistant", "content": content},
			"finish_reason": "stop",
		}},
	})
	return string(b)
}

func TestLlamaCppRequestShape(t *testing.T) {
	fake := &fakeLlama{reply: oaiReply(`{"merchant":"Target","items":[],"tax_lines":[],"subtotal":0,"total":0,"tax_evidence":"unknown"}`)}
	srv := fake.server(t)
	defer srv.Close()

	image := []byte{0xFF, 0xD8, 0xFF, 0xD9}
	if _, err := NewLlamaCppExtractor(LlamaCppOptions{BaseURL: srv.URL, Model: "qwen3.8-27b"}).
		Extract(context.Background(), image); err != nil {
		t.Fatalf("Extract: %v", err)
	}

	if fake.path != "/v1/chat/completions" {
		t.Errorf("path = %q, want /v1/chat/completions", fake.path)
	}
	if temp, _ := fake.body["temperature"].(float64); temp != 0 {
		t.Errorf("temperature = %v, want 0", fake.body["temperature"])
	}
	// A stale prompt cache is how the previous backend answered with the wrong
	// receipt, so caching must be off rather than merely discouraged.
	if cache, ok := fake.body["cache_prompt"].(bool); !ok || cache {
		t.Errorf("cache_prompt = %v, want false", fake.body["cache_prompt"])
	}
	// Thinking off, or the model deliberates instead of answering.
	kwargs, ok := fake.body["chat_template_kwargs"].(map[string]any)
	if !ok {
		t.Fatal("chat_template_kwargs missing: thinking would not be suppressed")
	}
	if think, ok := kwargs["enable_thinking"].(bool); !ok || think {
		t.Errorf("enable_thinking = %v, want false", kwargs["enable_thinking"])
	}
	// Grammar-constrained output.
	rf, ok := fake.body["response_format"].(map[string]any)
	if !ok || rf["type"] != "json_schema" {
		t.Fatalf("response_format = %v, want a json_schema", fake.body["response_format"])
	}
	js, ok := rf["json_schema"].(map[string]any)
	if !ok {
		t.Fatal("json_schema missing")
	}
	if strict, _ := js["strict"].(bool); !strict {
		t.Error("json_schema.strict should be true")
	}
	if _, ok := js["schema"].(map[string]any); !ok {
		t.Error("json_schema.schema missing: output would be unconstrained")
	}

	// The image travels as a data: URI here, unlike Ollama's bare base64.
	msgs := fake.body["messages"].([]any)
	parts := msgs[0].(map[string]any)["content"].([]any)
	var sawText, sawImage bool
	for _, p := range parts {
		part := p.(map[string]any)
		switch part["type"] {
		case "text":
			sawText = true
		case "image_url":
			sawImage = true
			url := part["image_url"].(map[string]any)["url"].(string)
			want := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(image)
			if url != want {
				t.Error("image payload does not round-trip as a data URI")
			}
		}
	}
	if !sawText || !sawImage {
		t.Errorf("expected both text and image parts, got text=%v image=%v", sawText, sawImage)
	}
}

func TestLlamaCppParsesExtraction(t *testing.T) {
	content := `{"merchant":"Brasserie B","purchased_at":null,
	  "items":[{"position":1,"line_text":"1 Able Baker - Dra 17.00","description":"Able Baker - Dra","amount":17.00,"taxable":null}],
	  "adjustments":[],"tax_lines":[{"label":"Tax","amount":35.26}],
	  "subtotal":421.00,"total":456.26,"tax_evidence":"unknown"}`
	fake := &fakeLlama{reply: oaiReply(content)}
	srv := fake.server(t)
	defer srv.Close()

	ex, err := NewLlamaCppExtractor(LlamaCppOptions{BaseURL: srv.URL}).Extract(context.Background(), []byte{1})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if ex.Merchant == nil || *ex.Merchant != "Brasserie B" {
		t.Errorf("merchant = %v", ex.Merchant)
	}
	if len(ex.Items) != 1 || ex.Items[0].Amount != 17.00 {
		t.Errorf("items = %+v", ex.Items)
	}
	if ex.Subtotal == nil || *ex.Subtotal != 421.00 {
		t.Errorf("subtotal = %v", ex.Subtotal)
	}
}

func TestLlamaCppFailureModes(t *testing.T) {
	cases := []struct {
		name      string
		fake      *fakeLlama
		wantMatch string
	}{
		{
			name:      "reasoning only, no content",
			fake:      &fakeLlama{reply: `{"choices":[{"message":{"content":"","reasoning_content":"Let me think..."},"finish_reason":"stop"}]}`},
			wantMatch: "only reasoning",
		},
		{
			name:      "empty content",
			fake:      &fakeLlama{reply: `{"choices":[{"message":{"content":""},"finish_reason":"stop"}]}`},
			wantMatch: "empty response",
		},
		{
			name:      "truncated by the token limit",
			fake:      &fakeLlama{reply: `{"choices":[{"message":{"content":"{\"items\":["},"finish_reason":"length"}]}`},
			wantMatch: "token limit",
		},
		{
			name:      "no choices",
			fake:      &fakeLlama{reply: `{"choices":[]}`},
			wantMatch: "no choices",
		},
		{
			name:      "server error field",
			fake:      &fakeLlama{reply: `{"error":{"message":"model not loaded"}}`},
			wantMatch: "model not loaded",
		},
		{
			name:      "non-200",
			fake:      &fakeLlama{reply: `nope`, status: http.StatusServiceUnavailable},
			wantMatch: "returned 503",
		},
		{
			name:      "unparseable envelope",
			fake:      &fakeLlama{reply: `<html>proxy error</html>`},
			wantMatch: "malformed inference response",
		},
		{
			name:      "content is not JSON",
			fake:      &fakeLlama{reply: oaiReply("I cannot read that receipt.")},
			wantMatch: "unparseable JSON",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := tc.fake.server(t)
			defer srv.Close()
			_, err := NewLlamaCppExtractor(LlamaCppOptions{BaseURL: srv.URL}).Extract(context.Background(), []byte{1})
			if err == nil {
				t.Fatal("expected an error")
			}
			if !errors.Is(err, ErrUnavailable) {
				t.Errorf("error should wrap ErrUnavailable so the caller falls back to manual entry: %v", err)
			}
			if !strings.Contains(err.Error(), tc.wantMatch) {
				t.Errorf("error %q does not mention %q", err, tc.wantMatch)
			}
		})
	}
}

func TestLlamaCppTimeoutAndCancellation(t *testing.T) {
	fake := &fakeLlama{reply: oaiReply("{}"), delay: 200 * time.Millisecond}
	srv := fake.server(t)
	defer srv.Close()

	if _, err := NewLlamaCppExtractor(LlamaCppOptions{BaseURL: srv.URL, Timeout: 20 * time.Millisecond}).
		Extract(context.Background(), []byte{1}); !errors.Is(err, ErrUnavailable) {
		t.Errorf("timeout should surface as ErrUnavailable, got %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := NewLlamaCppExtractor(LlamaCppOptions{BaseURL: srv.URL}).Extract(ctx, []byte{1}); !errors.Is(err, ErrUnavailable) {
		t.Errorf("cancellation should surface as ErrUnavailable, got %v", err)
	}
}

func TestLlamaCppRejectsEmptyImageAndSendsToken(t *testing.T) {
	if _, err := NewLlamaCppExtractor(LlamaCppOptions{BaseURL: "http://127.0.0.1:1"}).
		Extract(context.Background(), nil); err == nil {
		t.Error("expected an error for an empty image")
	}

	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(oaiReply(`{"merchant":null,"items":[],"tax_lines":[],"subtotal":0,"total":0,"tax_evidence":"unknown"}`)))
	}))
	defer srv.Close()
	if _, err := NewLlamaCppExtractor(LlamaCppOptions{BaseURL: srv.URL, Token: "sekret"}).
		Extract(context.Background(), []byte{1}); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if auth != "Bearer sekret" {
		t.Errorf("Authorization = %q", auth)
	}
}

// Both backends must ask for the same thing, or results stop being comparable.
func TestBothBackendsShareTheContract(t *testing.T) {
	var ollamaSchema, llamaSchema any
	fakeO := &fakeOllama{reply: okReply(`{"merchant":null,"items":[],"tax_lines":[],"subtotal":0,"total":0,"tax_evidence":"unknown"}`)}
	srvO := fakeO.server(t)
	defer srvO.Close()
	if _, err := NewOllamaExtractor(OllamaOptions{BaseURL: srvO.URL}).Extract(context.Background(), []byte{1}); err != nil {
		t.Fatalf("ollama: %v", err)
	}
	ollamaSchema = fakeO.body["format"]

	fakeL := &fakeLlama{reply: oaiReply(`{"merchant":null,"items":[],"tax_lines":[],"subtotal":0,"total":0,"tax_evidence":"unknown"}`)}
	srvL := fakeL.server(t)
	defer srvL.Close()
	if _, err := NewLlamaCppExtractor(LlamaCppOptions{BaseURL: srvL.URL}).Extract(context.Background(), []byte{1}); err != nil {
		t.Fatalf("llamacpp: %v", err)
	}
	llamaSchema = fakeL.body["response_format"].(map[string]any)["json_schema"].(map[string]any)["schema"]

	a, _ := json.Marshal(ollamaSchema)
	b, _ := json.Marshal(llamaSchema)
	if string(a) != string(b) {
		t.Error("the two backends are sending different schemas")
	}
	// And the same prompt text.
	oPrompt := fakeO.body["messages"].([]any)[0].(map[string]any)["content"].(string)
	lParts := fakeL.body["messages"].([]any)[0].(map[string]any)["content"].([]any)
	var lPrompt string
	for _, p := range lParts {
		if part := p.(map[string]any); part["type"] == "text" {
			lPrompt = part["text"].(string)
		}
	}
	if oPrompt != lPrompt {
		t.Error("the two backends are sending different prompts")
	}
}
