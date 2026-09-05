package linearclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	t.Setenv("TICKETVOICE_STATE_DIR", t.TempDir())
	return &Client{Token: "lin_api_test", Endpoint: srv.URL, HTTP: srv.Client()}
}

// The exact shape a bogus id returns, captured live against the real API this session.
const notFoundBody = `{"errors":[{"message":"Entity not found: Issue","path":["issue"],"extensions":{"type":"invalid input","code":"INPUT_ERROR","statusCode":400,"userError":true}}],"data":null}`

// The exact shape a real id returns, captured live against the real API this session.
const foundBody = `{"data":{"issue":{"id":"85686a48-f55a-4d7e-a3ca-6909204d8acc"}}}`

func TestExistsConfirmedNonexistent(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(notFoundBody))
	})
	exists, err := c.Exists(context.Background(), "ZZZZ-99999999")
	if err != nil {
		t.Fatalf("a confirmed not-found response must not be an error, got %v", err)
	}
	if exists {
		t.Fatal("want exists=false for a confirmed-nonexistent id")
	}
}

func TestExistsConfirmedReal(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(foundBody))
	})
	exists, err := c.Exists(context.Background(), "ABC-892")
	if err != nil || !exists {
		t.Fatalf("want exists=true, nil err for a real id, got exists=%v err=%v", exists, err)
	}
}

// An error shape that ISN'T the verified not-found code (auth, rate limit, anything else) must
// never be read as "confirmed nonexistent" — that would misclassify a transient failure as a
// wrong reference and wrongly deny.
func TestExistsUnrecognizedErrorFailsOpen(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"errors":[{"message":"Authentication required","extensions":{"code":"AUTHENTICATION_ERROR"}}],"data":null}`))
	})
	exists, err := c.Exists(context.Background(), "ABC-1")
	if err == nil {
		t.Fatal("an unrecognized error shape must come back as an error, not a verdict")
	}
	if exists {
		t.Fatal("must not report exists=true on an error path")
	}
}

func TestExistsNetworkFailureFailsOpen(t *testing.T) {
	c := &Client{Token: "x", Endpoint: "http://127.0.0.1:1", HTTP: &http.Client{Timeout: 200 * time.Millisecond}}
	exists, err := c.Exists(context.Background(), "ABC-1")
	if err == nil {
		t.Fatal("an unreachable endpoint must be an error, not a verdict")
	}
	if exists {
		t.Fatal("must not report exists=true on a network failure")
	}
}

func TestExistsNonOKStatusFailsOpen(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	exists, err := c.Exists(context.Background(), "ABC-1")
	if err == nil || exists {
		t.Fatalf("a rate-limit response must fail open, got exists=%v err=%v", exists, err)
	}
}

// isolateHome points os.UserHomeDir() (env var HOME on unix) at an empty temp dir, so a test
// asserting "no token anywhere" isn't silently made true or false by whatever this developer's
// actual ~/.config/ticketvoice/.env happens to contain.
func isolateHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

func TestNewRequiresToken(t *testing.T) {
	isolateHome(t)
	t.Setenv("TICKETVOICE_LINEAR_TOKEN", "")
	if _, ok := New(""); ok {
		t.Fatal("New must report ok=false with no token set anywhere")
	}
	t.Setenv("TICKETVOICE_LINEAR_TOKEN", "lin_api_x")
	c, ok := New("")
	if !ok || c.Endpoint != defaultEndpoint {
		t.Fatalf("want ok=true with the default endpoint, got ok=%v endpoint=%q", ok, c.Endpoint)
	}
}

// The global ~/.config/ticketvoice/.env fallback is the one that matters most for a hook wired
// into every project's settings.json: it's what lets the same token resolve no matter which
// project's cwd the hook is currently handling a call for.
func TestNewFallsBackToGlobalConfigFile(t *testing.T) {
	isolateHome(t)
	t.Setenv("TICKETVOICE_LINEAR_TOKEN", "")
	home := os.Getenv("HOME")
	dir := filepath.Join(home, ".config", "ticketvoice")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("TICKETVOICE_LINEAR_TOKEN=\"lin_api_global\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, ok := New(filepath.Join(t.TempDir(), "some", "project", "subdir"))
	if !ok {
		t.Fatal("want the global config file to resolve a token from an unrelated cwd")
	}
}

// A .env found by walking up from cwd takes priority over the global fallback — a project-local
// override should win where one exists.
func TestNewPrefersCwdEnvOverGlobalConfigFile(t *testing.T) {
	isolateHome(t)
	t.Setenv("TICKETVOICE_LINEAR_TOKEN", "")
	home := os.Getenv("HOME")
	globalDir := filepath.Join(home, ".config", "ticketvoice")
	if err := os.MkdirAll(globalDir, 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(globalDir, ".env"), []byte("TICKETVOICE_LINEAR_TOKEN=global\n"), 0o600)

	project := t.TempDir()
	sub := filepath.Join(project, "sub", "dir")
	os.MkdirAll(sub, 0o755)
	os.WriteFile(filepath.Join(project, ".env"), []byte("TICKETVOICE_LINEAR_TOKEN=local\n"), 0o600)

	c, ok := New(sub)
	if !ok || c.Token != "local" {
		t.Fatalf("want the project-local .env to win, got ok=%v token=%q", ok, c.Token)
	}
}

func TestAuthorizationHeaderHasNoBearerPrefix(t *testing.T) {
	var gotAuth string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(foundBody))
	})
	c.Exists(context.Background(), "ABC-1")
	if gotAuth != "lin_api_test" {
		t.Fatalf("want the raw token with no Bearer prefix, got %q", gotAuth)
	}
}

func TestTeamKeysFetchesAndCaches(t *testing.T) {
	calls := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte(`{"data":{"teams":{"nodes":[{"key":"ABC"},{"key":"ENG"}]}}}`))
	})
	keys, err := c.TeamKeys(context.Background())
	if err != nil || len(keys) != 2 {
		t.Fatalf("want 2 keys, got %v err=%v", keys, err)
	}
	// Second call within the TTL must not hit the network again.
	if _, err := c.TeamKeys(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("want the cache to serve the second call, got %d network calls", calls)
	}
}

func TestTeamKeysFallsBackToStaleCacheOnFetchFailure(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TICKETVOICE_STATE_DIR", dir)
	stale := teamKeysCache{Keys: []string{"ABC"}, FetchedAt: time.Now().Add(-48 * time.Hour)}
	raw, _ := json.Marshal(stale)
	path := filepath.Join(dir, "linear-team-keys.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	c := newTestClientNoStateReset(t, dir, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	keys, err := c.TeamKeys(context.Background())
	if err != nil || len(keys) != 1 || keys[0] != "ABC" {
		t.Fatalf("want the stale cache as a fallback, got keys=%v err=%v", keys, err)
	}
}

func newTestClientNoStateReset(t *testing.T, dir string, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	t.Setenv("TICKETVOICE_STATE_DIR", dir)
	return &Client{Token: "lin_api_test", Endpoint: srv.URL, HTTP: srv.Client()}
}
