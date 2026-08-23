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

// LlamaCppExtractor talks to llama.cpp's llama-server over its
// OpenAI-compatible endpoint.
//
// This is the preferred backend, for two measured reasons.
//
// Accuracy: llama-server run with --jinja applies the model's own chat template,
// where Ollama substitutes a generic one. On identical images, prompt and schema,
// that difference decided whether extraction worked at all -- a 14-item
// restaurant check went from 9 items to all 14, and a 6-item hardware receipt
// from 1 to all 6.
//
// Correctness: Ollama would sporadically answer with a previous image's contents,
// apparently matching a cached prompt prefix without accounting for the image, and
// it survived an upgrade to the latest release. Here caching can simply be turned
// off per request.
type LlamaCppExtractor struct {
	opts   LlamaCppOptions
	client *http.Client
}

// LlamaCppOptions configures the client. BaseURL is the server root; the
// OpenAI-compatible path is appended.
type LlamaCppOptions struct {
	BaseURL string
	Model   string
	Token   string
	Timeout time.Duration
}

func NewLlamaCppExtractor(opts LlamaCppOptions) *LlamaCppExtractor {
	if opts.Timeout <= 0 {
		opts.Timeout = 240 * time.Second
	}
	if opts.Model == "" {
		opts.Model = "qwen3.8-27b"
	}
	return &LlamaCppExtractor{opts: opts, client: &http.Client{Timeout: opts.Timeout}}
}

type oaiRequest struct {
	Model             string            `json:"model"`
	Messages          []oaiMessage      `json:"messages"`
	Temperature       float64           `json:"temperature"`
	CachePrompt       bool              `json:"cache_prompt"`
	ChatTemplateKwarg map[string]any    `json:"chat_template_kwargs,omitempty"`
	ResponseFormat    oaiResponseFormat `json:"response_format"`
}

type oaiMessage struct {
	Role    string       `json:"role"`
	Content []oaiContent `json:"content"`
}

type oaiContent struct {
	Type     string       `json:"type"`
	Text     string       `json:"text,omitempty"`
	ImageURL *oaiImageURL `json:"image_url,omitempty"`
}

type oaiImageURL struct {
	URL string `json:"url"`
}

type oaiResponseFormat struct {
	Type       string        `json:"type"`
	JSONSchema oaiJSONSchema `json:"json_schema"`
}

type oaiJSONSchema struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict"`
	Schema json.RawMessage `json:"schema"`
}

type oaiResponse struct {
	Choices []struct {
		Message struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (l *LlamaCppExtractor) Extract(ctx context.Context, image []byte) (Extraction, error) {
	if len(image) == 0 {
		return Extraction{}, errors.New("empty image")
	}

	body, err := json.Marshal(oaiRequest{
		Model: l.opts.Model,
		Messages: []oaiMessage{{
			Role: "user",
			Content: []oaiContent{
				{Type: "text", Text: extractPrompt},
				// A data: URI here, unlike Ollama's native API which wants bare base64.
				{Type: "image_url", ImageURL: &oaiImageURL{
					URL: "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(image),
				}},
			},
		}},
		Temperature: 0,
		// Off deliberately. A stale prompt cache is exactly how the previous
		// backend came to answer with the wrong receipt, and a scan is far too
		// slow for cache reuse to matter next to getting the right answer.
		CachePrompt: false,
		// The documented way to stop a Qwen model reasoning before it answers.
		// Extraction is perception, not deliberation, so every thinking token is
		// latency spent for nothing.
		ChatTemplateKwarg: map[string]any{"enable_thinking": false},
		ResponseFormat: oaiResponseFormat{
			Type: "json_schema",
			JSONSchema: oaiJSONSchema{
				Name:   "receipt",
				Strict: true,
				Schema: json.RawMessage(extractSchema),
			},
		},
	})
	if err != nil {
		return Extraction{}, err
	}

	endpoint := strings.TrimSuffix(l.opts.BaseURL, "/") + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Extraction{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if l.opts.Token != "" {
		req.Header.Set("Authorization", "Bearer "+l.opts.Token)
	}

	res, err := l.client.Do(req)
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

	var parsed oaiResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Extraction{}, fmt.Errorf("%w: malformed inference response", ErrUnavailable)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return Extraction{}, fmt.Errorf("%w: %s", ErrUnavailable, parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return Extraction{}, fmt.Errorf("%w: no choices in response", ErrUnavailable)
	}

	content := strings.TrimSpace(parsed.Choices[0].Message.Content)
	if content == "" {
		if parsed.Choices[0].Message.ReasoningContent != "" {
			return Extraction{}, fmt.Errorf("%w: model emitted only reasoning, no content (thinking is not suppressed for %q)",
				ErrUnavailable, l.opts.Model)
		}
		return Extraction{}, fmt.Errorf("%w: empty response", ErrUnavailable)
	}
	// A truncated answer is unparseable JSON anyway, but say why rather than
	// blaming the model's formatting.
	if parsed.Choices[0].FinishReason == "length" {
		return Extraction{}, fmt.Errorf("%w: response hit the token limit before finishing", ErrUnavailable)
	}

	var ex Extraction
	if err := json.Unmarshal([]byte(content), &ex); err != nil {
		return Extraction{}, fmt.Errorf("%w: model returned unparseable JSON", ErrUnavailable)
	}
	if ex.TaxEvidence == "" {
		ex.TaxEvidence = EvidenceUnknown
	}
	return ex, nil
}
