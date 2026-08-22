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

// fakeOllama captures the outgoing request so the wire format can be asserted,
// then replies with whatever the test needs.
type fakeOllama struct {
	path    string
	body    map[string]any
	reply   string
	status  int
	delay   time.Duration
	rawBody []byte
}

func (f *fakeOllama) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.path = r.URL.Path
		f.rawBody, _ = io.ReadAll(r.Body)
		_ = json.Unmarshal(f.rawBody, &f.body)
		if f.delay > 0 {
			time.Sleep(f.delay)
		}
		if f.status != 0 {
			w.WriteHeader(f.status)
		}
		_, _ = w.Write([]byte(f.reply))
	}))
}

func okReply(content string) string {
	b, _ := json.Marshal(map[string]any{
		"message": map[string]any{"role": "assistant", "content": content},
	})
	return string(b)
}

func TestOllamaExtractRequestShape(t *testing.T) {
	fake := &fakeOllama{reply: okReply(`{"merchant":"Target","items":[],"tax_lines":[],"subtotal":0,"total":0,"tax_evidence":"unknown"}`)}
	srv := fake.server(t)
	defer srv.Close()

	image := []byte{0xFF, 0xD8, 0xFF, 0xD9}
	ex := NewOllamaExtractor(OllamaOptions{BaseURL: srv.URL, Model: "qwen3.8:27b", NumCtx: 4096})
	if _, err := ex.Extract(context.Background(), image); err != nil {
		t.Fatalf("Extract: %v", err)
	}

	// Ollama ignores response_format on /v1, so the native path is mandatory.
	if fake.path != "/api/chat" {
		t.Errorf("path = %q, want /api/chat", fake.path)
	}
	// Measured: thinking must be off or the model never emits content.
	if think, ok := fake.body["think"].(bool); !ok || think {
		t.Errorf("think = %v, want false", fake.body["think"])
	}
	if _, ok := fake.body["format"]; !ok {
		t.Error("format schema missing: output would be unconstrained")
	}
	if stream, ok := fake.body["stream"].(bool); !ok || stream {
		t.Errorf("stream = %v, want false", fake.body["stream"])
	}
	opts, ok := fake.body["options"].(map[string]any)
	if !ok {
		t.Fatal("options missing")
	}
	if temp, _ := opts["temperature"].(float64); temp != 0 {
		t.Errorf("temperature = %v, want 0", opts["temperature"])
	}
	// Ollama truncates silently at ~4096, so this must be explicit.
	if ctx, _ := opts["num_ctx"].(float64); int(ctx) != 4096 {
		t.Errorf("num_ctx = %v, want 4096", opts["num_ctx"])
	}

	msgs, ok := fake.body["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf("messages = %v", fake.body["messages"])
	}
	msg := msgs[0].(map[string]any)
	imgs, ok := msg["images"].([]any)
	if !ok || len(imgs) != 1 {
		t.Fatalf("images = %v", msg["images"])
	}
	got := imgs[0].(string)
	// Raw base64 only. A data: URI is the OpenAI shape and fails here.
	if strings.HasPrefix(got, "data:") {
		t.Error("image carries a data: URI prefix; Ollama needs raw base64")
	}
	if got != base64.StdEncoding.EncodeToString(image) {
		t.Error("image payload does not round-trip")
	}
}

func TestOllamaExtractParsesExtraction(t *testing.T) {
	// The real response shape observed from qwen3.8:27b on the Target receipt.
	content := `{
	  "merchant":"Columbia","purchased_at":null,
	  "items":[
	    {"position":1,"line_text":"203800178 GATORADE TF $6.99","description":"GATORADE","amount":6.99,"taxable":true,"marker":"TF"},
	    {"position":2,"line_text":"072080526 Bodum T $29.99","description":"Bodum","amount":29.99,"taxable":true,"marker":"T"}
	  ],
	  "adjustments":[],
	  "tax_lines":[{"label":"MD TAX","amount":4.24,"base":70.66}],
	  "subtotal":36.98,"total":41.22,"tax_evidence":"per_line_flags"}`
	fake := &fakeOllama{reply: okReply(content)}
	srv := fake.server(t)
	defer srv.Close()

	ex, err := NewOllamaExtractor(OllamaOptions{BaseURL: srv.URL, Model: "m"}).
		Extract(context.Background(), []byte{1})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(ex.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(ex.Items))
	}
	if ex.Items[0].Description != "GATORADE" || ex.Items[0].Amount != 6.99 {
		t.Errorf("first item = %+v", ex.Items[0])
	}
	if ex.TaxLines[0].Base == nil || *ex.TaxLines[0].Base != 70.66 {
		t.Error("printed tax base lost: it is the strongest taxability signal")
	}
	if ex.TaxEvidence != EvidencePerLineFlags {
		t.Errorf("tax_evidence = %q", ex.TaxEvidence)
	}
}

func TestOllamaExtractFailureModes(t *testing.T) {
	cases := []struct {
		name      string
		fake      *fakeOllama
		wantMatch string
	}{
		{
			name:      "thinking only, no content",
			fake:      &fakeOllama{reply: `{"message":{"role":"assistant","content":"","thinking":"Okay, the user wants..."}}`},
			wantMatch: "only reasoning",
		},
		{
			name:      "empty response",
			fake:      &fakeOllama{reply: `{"message":{"role":"assistant","content":""}}`},
			wantMatch: "empty response",
		},
		{
			name:      "ollama error field",
			fake:      &fakeOllama{reply: `{"error":"model requires more system memory"}`},
			wantMatch: "more system memory",
		},
		{
			name:      "non-200",
			fake:      &fakeOllama{reply: `nope`, status: http.StatusInternalServerError},
			wantMatch: "returned 500",
		},
		{
			name:      "unparseable envelope",
			fake:      &fakeOllama{reply: `<html>gateway</html>`},
			wantMatch: "malformed inference response",
		},
		{
			name:      "content is not JSON",
			fake:      &fakeOllama{reply: okReply("I could not read that receipt.")},
			wantMatch: "unparseable JSON",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := tc.fake.server(t)
			defer srv.Close()
			_, err := NewOllamaExtractor(OllamaOptions{BaseURL: srv.URL, Model: "m"}).
				Extract(context.Background(), []byte{1})
			if err == nil {
				t.Fatal("expected an error")
			}
			if !errors.Is(err, ErrUnavailable) {
				t.Errorf("error should wrap ErrUnavailable so callers fall back to manual entry: %v", err)
			}
			if !strings.Contains(err.Error(), tc.wantMatch) {
				t.Errorf("error %q does not mention %q", err, tc.wantMatch)
			}
		})
	}
}

func TestOllamaExtractTimeoutIsUnavailable(t *testing.T) {
	fake := &fakeOllama{reply: okReply("{}"), delay: 200 * time.Millisecond}
	srv := fake.server(t)
	defer srv.Close()

	_, err := NewOllamaExtractor(OllamaOptions{
		BaseURL: srv.URL, Model: "m", Timeout: 20 * time.Millisecond,
	}).Extract(context.Background(), []byte{1})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("timeout should surface as ErrUnavailable, got %v", err)
	}
}

func TestOllamaExtractRespectsContextCancellation(t *testing.T) {
	fake := &fakeOllama{reply: okReply("{}"), delay: time.Second}
	srv := fake.server(t)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := NewOllamaExtractor(OllamaOptions{BaseURL: srv.URL, Model: "m"}).
		Extract(ctx, []byte{1})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("cancellation should surface as ErrUnavailable, got %v", err)
	}
}

func TestOllamaExtractRejectsEmptyImage(t *testing.T) {
	_, err := NewOllamaExtractor(OllamaOptions{BaseURL: "http://127.0.0.1:1", Model: "m"}).
		Extract(context.Background(), nil)
	if err == nil {
		t.Fatal("expected an error for an empty image")
	}
}

func TestOllamaExtractSendsBearerToken(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(okReply(`{"merchant":null,"items":[],"tax_lines":[],"subtotal":0,"total":0,"tax_evidence":"unknown"}`)))
	}))
	defer srv.Close()

	_, err := NewOllamaExtractor(OllamaOptions{BaseURL: srv.URL, Model: "m", Token: "sekret"}).
		Extract(context.Background(), []byte{1})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if auth != "Bearer sekret" {
		t.Errorf("Authorization = %q, want %q", auth, "Bearer sekret")
	}
}

func TestOllamaExtractDefaultsMissingEvidence(t *testing.T) {
	fake := &fakeOllama{reply: okReply(`{"merchant":null,"items":[],"tax_lines":[],"subtotal":1,"total":1,"tax_evidence":""}`)}
	srv := fake.server(t)
	defer srv.Close()

	ex, err := NewOllamaExtractor(OllamaOptions{BaseURL: srv.URL, Model: "m"}).
		Extract(context.Background(), []byte{1})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if ex.TaxEvidence != EvidenceUnknown {
		t.Errorf("tax_evidence = %q, want %q", ex.TaxEvidence, EvidenceUnknown)
	}
}

// The schema is the contract with the model; a typo silently unconstrains output.
func TestExtractSchemaIsValidJSON(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(extractSchema), &schema); err != nil {
		t.Fatalf("extractSchema is not valid JSON: %v", err)
	}
	req, ok := schema["required"].([]any)
	if !ok {
		t.Fatal("schema has no required list")
	}
	need := map[string]bool{"items": false, "tax_lines": false, "subtotal": false, "total": false, "tax_evidence": false}
	for _, r := range req {
		if s, ok := r.(string); ok {
			if _, tracked := need[s]; tracked {
				need[s] = true
			}
		}
	}
	for k, found := range need {
		if !found {
			t.Errorf("%q must be required or reconciliation cannot run", k)
		}
	}
	props := schema["properties"].(map[string]any)
	if _, has := props["confidence"]; has {
		t.Error("confidence must stay out of the schema: it measured 1.0 on a 50%-wrong extraction")
	}

	tl := props["tax_lines"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	if _, has := tl["base"]; !has {
		t.Error("tax_lines.base missing: it is the strongest taxable-set signal")
	}
}

// Each of these rules exists because its absence produced a specific wrong answer.
func TestExtractPromptKeepsHardWonRules(t *testing.T) {
	cases := []struct{ needle, why string }{
		{"Repeated items are normal", "three Baked Potato lines are three items"},
		{"quantity", "a leading quantity was being read as the price"},
		{"[0.00]", "zero-priced modifiers were emitted as items"},
		{"18% 75.78", "a tip guide was read as a charge"},
		{"GROCERY", "department headers were emitted as items"},
		{"6.00000 on $70.66", "the printed tax base is the strongest taxability signal"},
		{"NO arithmetic", "the server does every calculation, in cents"},
	}
	for _, tc := range cases {
		if !strings.Contains(extractPrompt, tc.needle) {
			t.Errorf("prompt no longer mentions %q (%s)", tc.needle, tc.why)
		}
	}
	// Long prompts measurably diluted the reading step: at 12 rules the model
	// transcribed 15 of 30 lines, at 8 rules it read all of them.
	if n := strings.Count(extractPrompt, "\n"); n > 24 {
		t.Errorf("prompt is %d lines; keeping it short is what made the reading step work", n)
	}
}
