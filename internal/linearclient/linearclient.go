// Package linearclient is a small, stdlib-only Linear GraphQL client — no dependency on the
// Linear MCP server ticketvoice already sits next to, since a plain Go hook can't reach into
// Claude Code's own MCP session credentials. It authenticates with the same kind of personal API
// key (lin_api_...) an operator already has for that MCP server, read from its own env var so the
// two never collide.
//
// Every call fails open: a missing token, a network error, a timeout, or an ambiguous GraphQL
// response all come back as "couldn't determine," never as "confirmed false." A citation check
// that can't reach Linear must skip that citation, not deny over it.
package linearclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/justinstimatze/ticketvoice/internal/attemptstate"
)

const defaultEndpoint = "https://api.linear.app/graphql"

// timeout is one tier above judgeSibling's 3-second subprocess timeout (budgetgate.go) — a
// hosted network call deserves a bit more slack than a local binary.
const timeout = 4 * time.Second

// Client is a Linear GraphQL client carrying a personal API token.
type Client struct {
	Token    string
	Endpoint string
	HTTP     *http.Client
}

// New resolves TICKETVOICE_LINEAR_TOKEN (and optionally TICKETVOICE_LINEAR_ENDPOINT) via
// loadToken(cwd), and reports ok=false when none is found anywhere. Callers must treat that
// exactly like a missing cope-gate/basanite binary: skip whatever check needed it, never deny
// on it.
func New(cwd string) (c *Client, ok bool) {
	token := loadToken(cwd)
	if token == "" {
		return nil, false
	}
	endpoint := os.Getenv("TICKETVOICE_LINEAR_ENDPOINT")
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	return &Client{Token: token, Endpoint: endpoint, HTTP: &http.Client{Timeout: timeout}}, true
}

// loadToken resolves the Linear token the same way hindcast's loadAPIKey resolves
// ANTHROPIC_API_KEY, and for the same reason: env var first, then a .env file found by walking
// up from cwd, then a global ~/.config/ticketvoice/.env — the global fallback is what lets a
// hook wired into every project's settings.json resolve a token regardless of which project's
// cwd it's currently handling a call for; a per-repo .env only helps when cwd is at/under that
// one repo.
func loadToken(cwd string) string {
	if v := os.Getenv("TICKETVOICE_LINEAR_TOKEN"); v != "" {
		return stripQuotes(v)
	}
	for dir := cwd; dir != ""; {
		if v := readEnvFrom(filepath.Join(dir, ".env")); v != "" {
			return v
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if home, err := os.UserHomeDir(); err == nil {
		if v := readEnvFrom(filepath.Join(home, ".config", "ticketvoice", ".env")); v != "" {
			return v
		}
	}
	return ""
}

func readEnvFrom(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if v, ok := strings.CutPrefix(line, "TICKETVOICE_LINEAR_TOKEN="); ok {
			return stripQuotes(strings.TrimSpace(v))
		}
	}
	return ""
}

// stripQuotes lets `TICKETVOICE_LINEAR_TOKEN="lin_api_..."` work the same as an unquoted value.
func stripQuotes(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

type gqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

type gqlError struct {
	Message    string `json:"message"`
	Extensions struct {
		Code string `json:"code"`
	} `json:"extensions"`
}

type gqlResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []gqlError      `json:"errors"`
}

// do POSTs one GraphQL query. The Authorization header carries the raw personal API key — no
// "Bearer" prefix. Bearer is for an OAuth access token, a different auth mode this client doesn't
// use; getting this wrong 401s silently (fails open), which looks like "the feature does
// nothing" rather than a clear auth error, so it's called out here rather than left to be
// rediscovered.
func (c *Client) do(ctx context.Context, query string, variables map[string]any) (*gqlResponse, error) {
	body, err := json.Marshal(gqlRequest{Query: query, Variables: variables})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.Token)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("linear: unexpected status %d", resp.StatusCode)
	}
	var out gqlResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// notFoundCode is the extensions.code Linear returns for a query naming an id that doesn't
// resolve to anything — verified live against the real API: a bogus issue id comes back
// {"data":null,"errors":[{"message":"Entity not found: Issue",...,"extensions":{"code":
// "INPUT_ERROR",...}}]}, while a real id comes back a clean {"data":{"issue":{...}}} with no
// errors at all. Any other error shape (auth, rate limit, a malformed query) must not be read as
// "doesn't exist" — only this specific code means that.
const notFoundCode = "INPUT_ERROR"

// Exists reports whether id resolves to a real Linear issue. err != nil means the call couldn't
// determine anything — network failure, timeout, non-2xx, malformed JSON, or a GraphQL error
// that isn't the verified not-found shape — and the caller must fail open on it, never treat it
// as confirmed-nonexistent.
func (c *Client) Exists(ctx context.Context, id string) (exists bool, err error) {
	resp, err := c.do(ctx, `query($id: String!) { issue(id: $id) { id } }`, map[string]any{"id": id})
	if err != nil {
		return false, err
	}
	if len(resp.Errors) > 0 {
		for _, e := range resp.Errors {
			if e.Extensions.Code == notFoundCode {
				return false, nil
			}
		}
		return false, fmt.Errorf("linear: unrecognized error response for %q: %s", id, resp.Errors[0].Message)
	}
	var data struct {
		Issue *struct {
			ID string `json:"id"`
		} `json:"issue"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		return false, err
	}
	return data.Issue != nil, nil
}

// teamKeysCacheTTL bounds how long a cached team-key list is trusted before a fresh fetch is
// attempted. Team keys change rarely; this just keeps a long-lived hook process's view from
// drifting forever, not from ever refreshing.
const teamKeysCacheTTL = 24 * time.Hour

type teamKeysCache struct {
	Keys      []string  `json:"keys"`
	FetchedAt time.Time `json:"fetched_at"`
}

func teamKeysCachePath() (string, error) {
	dir, err := attemptstate.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "linear-team-keys.json"), nil
}

func loadTeamKeysCache() (teamKeysCache, bool) {
	path, err := teamKeysCachePath()
	if err != nil {
		return teamKeysCache{}, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return teamKeysCache{}, false
	}
	var c teamKeysCache
	if json.Unmarshal(raw, &c) != nil {
		return teamKeysCache{}, false
	}
	return c, true
}

func saveTeamKeysCache(c teamKeysCache) {
	path, err := teamKeysCachePath()
	if err != nil {
		return
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, raw, 0o644)
}

// TeamKeys returns the workspace's real team keys (e.g. "ENG", "PROD"), cached for
// teamKeysCacheTTL. A stale cache is still returned if a fresh fetch fails — team keys rarely
// change, and citecheck needs this list to avoid treating ordinary jargon (UTF-8, GPT-4) as a
// ticket-id citation; falling back to slightly-stale data serves that better than skipping the
// check outright. Only a total failure (no cache and no successful fetch) returns an error.
func (c *Client) TeamKeys(ctx context.Context) ([]string, error) {
	cached, ok := loadTeamKeysCache()
	if ok && time.Since(cached.FetchedAt) < teamKeysCacheTTL {
		return cached.Keys, nil
	}

	resp, err := c.do(ctx, `query { teams { nodes { key } } }`, nil)
	if err != nil {
		if ok {
			return cached.Keys, nil
		}
		return nil, err
	}
	if len(resp.Errors) > 0 {
		if ok {
			return cached.Keys, nil
		}
		return nil, fmt.Errorf("linear: %s", resp.Errors[0].Message)
	}
	var data struct {
		Teams struct {
			Nodes []struct {
				Key string `json:"key"`
			} `json:"nodes"`
		} `json:"teams"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		if ok {
			return cached.Keys, nil
		}
		return nil, err
	}
	keys := make([]string, 0, len(data.Teams.Nodes))
	for _, n := range data.Teams.Nodes {
		keys = append(keys, n.Key)
	}
	saveTeamKeysCache(teamKeysCache{Keys: keys, FetchedAt: time.Now()})
	return keys, nil
}
