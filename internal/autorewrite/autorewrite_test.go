package autorewrite

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func isolateHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &Client{http: srv.Client(), apiKey: "sk-ant-test", model: defaultModel, endpoint: srv.URL}
}

func toolUseResponse(rewritten string) string {
	input, _ := json.Marshal(map[string]string{"rewritten": rewritten})
	body, _ := json.Marshal(map[string]any{
		"content": []map[string]any{
			{"type": "tool_use", "name": "rewrite", "input": json.RawMessage(input)},
		},
	})
	return string(body)
}

func TestNewRequiresKey(t *testing.T) {
	isolateHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	if _, ok := New(""); ok {
		t.Fatal("New must report ok=false with no key set anywhere")
	}
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-x")
	c, ok := New("")
	if !ok || c.model != defaultModel {
		t.Fatalf("want ok=true with the default model, got ok=%v model=%q", ok, c.model)
	}
}

func TestNewHonorsModelOverride(t *testing.T) {
	isolateHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-x")
	t.Setenv("TICKETVOICE_REWRITE_MODEL", "claude-haiku-4-5")
	c, ok := New("")
	if !ok || c.model != "claude-haiku-4-5" {
		t.Fatalf("want the overridden model, got ok=%v model=%q", ok, c.model)
	}
}

func TestRewriteSucceeds(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(toolUseResponse("This is 12 words of prose, well inside the budget now.")))
	})
	got, err := c.Rewrite(context.Background(), "issue description", "original over-budget text", []string{"over budget"})
	if err != nil {
		t.Fatalf("want a clean rewrite, got err=%v", err)
	}
	if got != "This is 12 words of prose, well inside the budget now." {
		t.Fatalf("want the tool's rewritten text, got %q", got)
	}
}

func TestRewriteFailsOpenOnTimeout(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.Write([]byte(toolUseResponse("too slow")))
	})
	c.http.Timeout = 10 * time.Millisecond
	_, err := c.Rewrite(context.Background(), "issue description", "text", nil)
	if err == nil {
		t.Fatal("a timed-out call must return an error, not a rewrite")
	}
}

func TestRewriteFailsOpenOnMalformedToolInput(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"content":[{"type":"tool_use","name":"rewrite","input":"not an object"}]}`))
	})
	_, err := c.Rewrite(context.Background(), "issue description", "text", nil)
	if err == nil {
		t.Fatal("malformed tool input must return an error, not panic or return a bogus rewrite")
	}
}

func TestRewriteFailsOpenOnNoToolUse(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"content":[{"type":"text","text":"I decline to use the tool."}]}`))
	})
	_, err := c.Rewrite(context.Background(), "issue description", "text", nil)
	if err == nil {
		t.Fatal("a response with no rewrite tool_use must return an error")
	}
}

func TestRewriteFailsOpenOnEmptyRewrite(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(toolUseResponse("")))
	})
	_, err := c.Rewrite(context.Background(), "issue description", "text", nil)
	if err == nil {
		t.Fatal("an empty rewritten string must return an error, not a blank body")
	}
}

func TestRewriteFailsOpenOnAPIError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	})
	_, err := c.Rewrite(context.Background(), "issue description", "text", nil)
	if err == nil {
		t.Fatal("an API error response must return an error, not a rewrite")
	}
}

func TestNewMakesNoNetworkCallWhenKeyMissing(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
	}))
	t.Cleanup(srv.Close)

	isolateHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("TICKETVOICE_ANTHROPIC_ENDPOINT", srv.URL)
	if _, ok := New(""); ok {
		t.Fatal("want ok=false with no key set")
	}
	if calls != 0 {
		t.Fatalf("New with no key must never touch the network, got %d calls", calls)
	}
}
