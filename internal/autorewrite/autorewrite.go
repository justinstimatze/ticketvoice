// Package autorewrite makes one bounded, hand-rolled Anthropic API call to fix flagged Linear
// prose (over-budget length, a cope voice/structure hit, a basanite vocabulary tic) instead of
// denying the write and waiting on the calling agent to retry. Deliberately no SDK dependency —
// matches basanite/internal/judge/cell.go's precedent, which hand-rolls the same forced-tool-use
// pattern over net/http specifically to avoid github.com/anthropics/anthropic-sdk-go's ~50
// transitive packages (AWS SDK v2, gRPC, protobuf, OpenTelemetry, ...) in a project that's
// otherwise stdlib-only.
//
// This package only produces a candidate rewrite — it does not decide whether the candidate is
// good enough. That's the caller's job (main.go re-validates the candidate against every check
// the original text went through). Fails open on every error path: a missing key, a timeout, a
// malformed response, or a response with no tool_use block all return an error, never a panic and
// never a fabricated rewrite.
package autorewrite

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/justinstimatze/ticketvoice/internal/tokensrc"
)

const (
	defaultModel    = "claude-sonnet-5"
	defaultEndpoint = "https://api.anthropic.com/v1/messages"

	// timeout is one tier above linearclient's 4-second hosted-call convention — this is real
	// generation work (rewriting prose), not a lookup, so it gets more slack, but still a hard
	// ceiling: a PreToolUse hook's 600s default budget has plenty of room, but this call must not
	// be the thing that makes a single flagged write feel slow.
	timeout = 8 * time.Second

	// maxTokens is sized for a full rewritten ticket body, not cell.go's max_tokens: 512 (which
	// was sized for a one-word classification verdict) — a real ticket description can run several
	// hundred words even after a trim.
	maxTokens = 2048

	anthropicVersion = "2023-06-01"
)

// systemPrompt is the fixed instruction block, cached ephemeral on every call — see cell.go's
// identical pattern. Not expected to actually pay off given call frequency (each hook invocation is
// its own process, and flagged writes aren't frequent enough to reliably land within a 5-minute
// cache window), but it's free and matches the house default of caching a repeated system block.
const systemPrompt = `You rewrite a Linear ticket's prose to fix specific, named problems, without
changing what it says. Keep every concrete fact — file:line references, commit SHAs, ticket ids,
numbers, dates — exactly as they appear in the original text. Never invent a new ticket id, file
path, or commit SHA that isn't already present. Match a terse, concrete house voice: no filler, no
hedging, no restating what a reader can already see. Call the rewrite tool exactly once with the
full corrected text.`

// Client makes bounded, single-turn, forced-tool-use rewrite calls.
type Client struct {
	http     *http.Client
	apiKey   string
	model    string
	endpoint string
}

// New resolves ANTHROPIC_API_KEY via tokensrc.Resolve(cwd, "ANTHROPIC_API_KEY") — the same
// env → cwd .env → ~/.config/ticketvoice/.env chain linearclient uses for the Linear token — and
// TICKETVOICE_REWRITE_MODEL for a model override. ok=false when no key resolves anywhere; the
// caller must treat that exactly like a nil linearclient.Client: skip, never deny on it.
func New(cwd string) (c *Client, ok bool) {
	key := tokensrc.Resolve(cwd, "ANTHROPIC_API_KEY")
	if key == "" {
		return nil, false
	}
	model := os.Getenv("TICKETVOICE_REWRITE_MODEL")
	if model == "" {
		model = defaultModel
	}
	endpoint := os.Getenv("TICKETVOICE_ANTHROPIC_ENDPOINT")
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	return &Client{
		http:     &http.Client{Timeout: timeout},
		apiKey:   key,
		model:    model,
		endpoint: endpoint,
	}, true
}

// rewriteToolSchema is the single tool this call forces — a rewrite is structured output, never
// free text, so there's no stray commentary to strip out of the response.
var rewriteToolSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"rewritten": map[string]any{
			"type":        "string",
			"description": "The full corrected ticket text, ready to post as-is.",
		},
	},
	"required": []string{"rewritten"},
}

// Rewrite asks the model to fix exactly the named violations in text and returns the rewritten
// prose. No retry — any transport error, timeout, non-2xx status, malformed JSON, or a response
// with no "rewrite" tool_use block returns an error, and the caller falls through to the existing
// deny-and-retry path with the original text untouched.
func (c *Client) Rewrite(ctx context.Context, kind, text string, violations []string) (string, error) {
	var userPrompt bytes.Buffer
	fmt.Fprintf(&userPrompt, "This %s was flagged for:\n\n", kind)
	for _, v := range violations {
		fmt.Fprintf(&userPrompt, "%s\n\n", v)
	}
	fmt.Fprintf(&userPrompt, "Original text:\n\n%s", text)

	body := map[string]any{
		// No temperature: current-generation models (claude-sonnet-5 included) reject sampling
		// params entirely — "temperature is deprecated for this model," confirmed live against the
		// real API. cell.go's identical-looking request (its Haiku model predates this) is not a
		// safe copy for that one field.
		"model":      c.model,
		"max_tokens": maxTokens,
		"system": []map[string]any{{
			"type":          "text",
			"text":          systemPrompt,
			"cache_control": map[string]any{"type": "ephemeral"},
		}},
		"messages": []map[string]any{{"role": "user", "content": userPrompt.String()}},
		"tools": []map[string]any{{
			"name":         "rewrite",
			"description":  "Record the corrected ticket text.",
			"input_schema": rewriteToolSchema,
		}},
		"tool_choice": map[string]any{"type": "tool", "name": "rewrite"},
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(buf))
	if err != nil {
		return "", err
	}
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("content-type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var out struct {
		Content []struct {
			Type  string          `json:"type"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Error != nil {
		return "", fmt.Errorf("anthropic: %s", out.Error.Message)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("anthropic: unexpected status %d", resp.StatusCode)
	}
	for _, block := range out.Content {
		if block.Type != "tool_use" || block.Name != "rewrite" {
			continue
		}
		var parsed struct {
			Rewritten string `json:"rewritten"`
		}
		if err := json.Unmarshal(block.Input, &parsed); err != nil {
			return "", err
		}
		if parsed.Rewritten == "" {
			return "", fmt.Errorf("anthropic: rewrite tool returned empty text")
		}
		return parsed.Rewritten, nil
	}
	return "", fmt.Errorf("anthropic: no rewrite tool_use in response (status %d)", resp.StatusCode)
}
