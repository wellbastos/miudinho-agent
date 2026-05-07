package githubissues

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestSanitizeSlug(t *testing.T) {
	if got := sanitizeSlug("  Meu_Time@Prod  "); got != "meu-time-prod" {
		t.Fatalf("unexpected sanitized slug: %q", got)
	}
}

func TestSplitCSV(t *testing.T) {
	got := splitCSV(" alpha, beta ,, gamma ")
	want := []string{"alpha", "beta", "gamma"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected split result: got=%v want=%v", got, want)
	}
}

func TestRepositoryAndTeamMentions(t *testing.T) {
	client := &Client{
		owner:      "wellbastos",
		repoPrefix: "apps-",
		product:    " Meu_Produto ",
		teams:      []string{"sq-sre_admin", " dev-platform "},
	}

	if got := client.Repository("ignored"); got != "apps-meu-produto" {
		t.Fatalf("unexpected repository: %q", got)
	}

	wantMentions := []string{"@wellbastos/sq-sre-admin", "@wellbastos/dev-platform"}
	if got := client.TeamMentions(); !reflect.DeepEqual(got, wantMentions) {
		t.Fatalf("unexpected team mentions: got=%v want=%v", got, wantMentions)
	}
}

func TestRepositoryUsesPrefixFromEnvConfig(t *testing.T) {
	client := &Client{
		repoPrefix: "incidents-",
		product:    " checkout ",
	}

	if got := client.Repository("ignored"); got != "incidents-checkout" {
		t.Fatalf("unexpected repository with custom prefix: %q", got)
	}
}

func TestCreateIssueSendsExpectedPayloadAndHeaders(t *testing.T) {
	var method string
	var path string
	var auth string
	var body map[string]any

	client := &Client{
		baseURL: "https://github.example",
		httpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			method = r.Method
			path = r.URL.Path
			auth = r.Header.Get("Authorization")
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request body: %v", err)
			}

			respBody, err := json.Marshal(Issue{
				Number:  42,
				HTMLURL: "https://github.example/issues/42",
				State:   "open",
			})
			if err != nil {
				t.Fatalf("marshal response body: %v", err)
			}
			return &http.Response{
				StatusCode: http.StatusCreated,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(string(respBody))),
			}, nil
		})},
		token: "token-123",
		owner: "wellbastos",
	}

	issue, err := client.CreateIssue(context.Background(), "apps-miudinho", "incident title", "incident body", []string{"incident", "sre"})
	if err != nil {
		t.Fatalf("CreateIssue returned error: %v", err)
	}

	if method != http.MethodPost {
		t.Fatalf("unexpected method: %s", method)
	}
	if path != "/repos/wellbastos/apps-miudinho/issues" {
		t.Fatalf("unexpected path: %s", path)
	}
	if auth != "Bearer token-123" {
		t.Fatalf("unexpected authorization header: %q", auth)
	}
	if issue.Number != 42 {
		t.Fatalf("unexpected issue number: %d", issue.Number)
	}
	if got := body["title"]; got != "incident title" {
		t.Fatalf("unexpected title payload: %#v", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
