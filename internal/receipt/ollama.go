package receipt

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Extractor reads a receipt image and returns the facts printed on it.
type Extractor interface {
	Extract(ctx context.Context, image []byte) (Extraction, error)
}

// ErrUnavailable means the inference service could not be reached or did not
// answer in time. Callers fall back to pre-filled manual entry.
var ErrUnavailable = errors.New("receipt inference unavailable")

// OllamaOptions configures the client. BaseURL empty disables the feature.
type OllamaOptions struct {
	BaseURL string
	Model   string
	Token   string
	NumCtx  int
	Timeout time.Duration
}

// OllamaExtractor talks to Ollama's native /api/chat.
//
// Deliberately not the OpenAI-compatible /v1 shim: Ollama ignores
// response_format:json_schema there (ollama#10001) and returns unconstrained
// output that merely looks like JSON. Only the native top-level "format"
// parameter compiles the schema into a real grammar.
type OllamaExtractor struct {
	opts   OllamaOptions
	client *http.Client
}

func NewOllamaExtractor(opts OllamaOptions) *OllamaExtractor {
	if opts.NumCtx <= 0 {
		opts.NumCtx = 32768
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 120 * time.Second
	}
	return &OllamaExtractor{
		opts:   opts,
		client: &http.Client{Timeout: opts.Timeout},
	}
}

type ollamaRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Think    bool            `json:"think"`
	Stream   bool            `json:"stream"`
	Format   json.RawMessage `json:"format"`
	Options  ollamaOptions   `json:"options"`
}

type ollamaMessage struct {
	Role    string   `json:"role"`
	Content string   `json:"content"`
	Images  []string `json:"images,omitempty"`
}

type ollamaOptions struct {
	Temperature float64 `json:"temperature"`
	NumCtx      int     `json:"num_ctx"`
}

type ollamaResponse struct {
	Message struct {
		Content  string `json:"content"`
		Thinking string `json:"thinking"`
	} `json:"message"`
	Error           string `json:"error"`
	PromptEvalCount int    `json:"prompt_eval_count"`
	EvalCount       int    `json:"eval_count"`
}

func (o *OllamaExtractor) Extract(ctx context.Context, image []byte) (Extraction, error) {
	if len(image) == 0 {
		return Extraction{}, errors.New("empty image")
	}

	body, err := json.Marshal(ollamaRequest{
		Model: o.opts.Model,
		Messages: []ollamaMessage{{
			Role:    "user",
			Content: extractPrompt,
			// Raw base64, no data: URI prefix -- that is the OpenAI shape and
			// Ollama's native API rejects it.
			Images: []string{base64.StdEncoding.EncodeToString(image)},
		}},
		// Measured: qwen3.8:27b honours this (thinking_chars=0). Other vision
		// models on Ollama ignore it and never emit output, so a model swap must
		// re-verify before anything else.
		Think:  false,
		Stream: false,
		Format: json.RawMessage(extractSchema),
		Options: ollamaOptions{
			Temperature: 0,
			NumCtx:      o.opts.NumCtx,
		},
	})
	if err != nil {
		return Extraction{}, err
	}

	endpoint := strings.TrimSuffix(o.opts.BaseURL, "/") + "/api/chat"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Extraction{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if o.opts.Token != "" {
		req.Header.Set("Authorization", "Bearer "+o.opts.Token)
	}

	res, err := o.client.Do(req)
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if res.StatusCode != http.StatusOK {
		return Extraction{}, fmt.Errorf("%w: inference returned %d", ErrUnavailable, res.StatusCode)
	}

	var parsed ollamaResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Extraction{}, fmt.Errorf("%w: malformed inference response", ErrUnavailable)
	}
	if parsed.Error != "" {
		return Extraction{}, fmt.Errorf("%w: %s", ErrUnavailable, parsed.Error)
	}
	if strings.TrimSpace(parsed.Message.Content) == "" {
		// A model that emits only thinking lands here. Naming it saves an hour.
		if parsed.Message.Thinking != "" {
			return Extraction{}, fmt.Errorf("%w: model emitted only reasoning, no content (thinking is not suppressed for %q)",
				ErrUnavailable, o.opts.Model)
		}
		return Extraction{}, fmt.Errorf("%w: empty response", ErrUnavailable)
	}

	var ex Extraction
	if err := json.Unmarshal([]byte(parsed.Message.Content), &ex); err != nil {
		return Extraction{}, fmt.Errorf("%w: model returned unparseable JSON", ErrUnavailable)
	}
	if ex.TaxEvidence == "" {
		ex.TaxEvidence = EvidenceUnknown
	}
	return ex, nil
}
