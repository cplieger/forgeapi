package github_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/forgeapi/v3/github"
	"github.com/cplieger/forgeapi/v3/github/githubtest"
	"github.com/cplieger/forgeapi/v3/internal/spec"
)

type staticToken struct{}

func (staticToken) Token(context.Context) (string, error) { return "placeholder", nil }
func (staticToken) Kind() forgeapi.CredKind               { return forgeapi.CredKindStaticPAT }
func (staticToken) State() forgeapi.CredState             { return forgeapi.CredValid }

// stub is a GitHub answering every request with one status, header and body,
// recording each request it receives and each rate limit the client counts.
type stub struct {
	client   *github.Client
	requests []string
	limited  int
	mu       sync.Mutex
}

func newStub(t *testing.T, status int, header http.Header, body []byte) *stub {
	t.Helper()
	s := &stub{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests = append(s.requests, r.Method+" "+r.URL.Path)
		s.mu.Unlock()
		maps.Copy(w.Header(), header)
		w.WriteHeader(status)
		if _, err := w.Write(body); err != nil {
			t.Errorf("Setup: writing the answer: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	counters := forgeapi.Counters{RateLimited: func(forgeapi.Family) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.limited++
	}}
	client, err := github.New(forgeapi.Connection{WebBaseURL: srv.URL, APIBaseURL: srv.URL + "/api/v3"},
		forgeapi.WithWireTransport(srv.Client().Transport),
		forgeapi.WithCredentialSource(staticToken{}),
		forgeapi.WithCounters(counters),
		forgeapi.WithPlaintextHTTP(true),
		forgeapi.WithPrivateAddresses(true),
		forgeapi.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatalf("Setup: github.New: %v", err)
	}
	s.client = client
	return s
}

// counted is the requests the stub received and the rate limits the client
// counted so far.
func (s *stub) counted() (requests []string, limited int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests), s.limited
}

// documentRoute is the request a GitHub Enterprise Server connection sends a
// GraphQL document as.
const documentRoute = "POST /api/graphql"

var limitRepo = forgeapi.RepoRef{Family: forgeapi.FamilyGitHub, Selector: "example/example", DisplayPath: "example/example"}

// TestReadLimit runs every rate-limit case through the reading its surface
// takes, and holds the cases to the expectation table: every GitHub Throttles
// row and every GitHub Figures row has a case answering as the row says.
func TestReadLimit(t *testing.T) {
	cases := githubtest.LimitCases(time.Now())
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			if c.Document {
				readsDocument(t, &c)
				return
			}
			readsREST(t, &c)
		})
	}
	for _, th := range spec.Throttles {
		if th.Product != spec.GitHub {
			continue
		}
		if !slices.ContainsFunc(cases, func(c githubtest.LimitCase) bool { return answersThrottle(&c, &th) }) {
			t.Errorf("no case answers the Throttles row %d %q at %q, document %t, message %q (%s)",
				th.Status, th.Header, th.Value, th.Document, th.Message, th.Says)
		}
	}
	for _, f := range spec.Figures {
		if f.Product != spec.GitHub {
			continue
		}
		for _, signal := range figureSignals(&f) {
			if !slices.ContainsFunc(cases, func(c githubtest.LimitCase) bool { return signal(&c) }) {
				t.Errorf("no case reads the Figures row %s from %q at %q, message %q (%s)", f.Field, f.Header, f.Value, f.Message, f.Says)
			}
		}
	}
}

// readsREST holds a REST case to ReadLimit and to the client's error on a REST
// read, which must state the same facts: the limit, the pool, the figures, the
// secondary mark and the wait.
func readsREST(t *testing.T, c *githubtest.LimitCase) {
	t.Helper()
	if got := github.ReadLimit(c.Status, c.Header, c.Body); got != c.Read {
		t.Errorf("ReadLimit = %+v, want %+v", got, c.Read)
	}
	_, err := newStub(t, c.Status, c.Header, c.Body).client.Whoami(t.Context())
	checkError(t, c, err)
}

// readsDocument holds a document case to the client's error on a document
// read: the case's figures, one rate limit counted, and a connection still
// reading documents, since a throttle is not the document's own failure.
func readsDocument(t *testing.T, c *githubtest.LimitCase) {
	t.Helper()
	s := newStub(t, c.Status, c.Header, c.Body)
	_, err := s.client.ReadPR(t.Context(), limitRepo, forgeapi.PRRef{Number: 1, Sigil: "#"})
	checkError(t, c, err)
	sent, limited := s.counted()
	if limited != 1 {
		t.Errorf("the rate-limit counter fired %d time(s), want 1", limited)
	}
	if _, err := s.client.CommitStatus(t.Context(), limitRepo, "main"); err == nil {
		t.Error("CommitStatus against the same answer = nil, want its error")
	}
	if after, _ := s.counted(); len(after) == len(sent) || after[len(sent)] != documentRoute {
		t.Errorf("the next read sent %v, want it to open with %s: the limit degraded the connection", after[len(sent):], documentRoute)
	}
}

// checkError holds the client's error to the case's kind, status and Read.
func checkError(t *testing.T, c *githubtest.LimitCase, err error) {
	t.Helper()
	fe, ok := errors.AsType[*forgeapi.Error](err)
	if !ok {
		t.Fatalf("the read answered %v, want a *forgeapi.Error", err)
	}
	if fe.Kind != c.Kind || fe.Status != c.Status {
		t.Errorf("the read = kind %v status %d, want %v, %d", fe.Kind, fe.Status, c.Kind, c.Status)
	}
	if fe.Pool != c.Read.Pool || fe.Remaining != c.Read.Remaining || !fe.Reset.Equal(c.Read.Reset) ||
		fe.Secondary != c.Read.Secondary || fe.RetryAfter != c.Read.RetryAfter {
		t.Errorf("the read = pool %q remaining %d reset %v secondary %t retry after %v, want the case's %q, %d, %v, %t, %v",
			fe.Pool, fe.Remaining, fe.Reset, fe.Secondary, fe.RetryAfter,
			c.Read.Pool, c.Read.Remaining, c.Read.Reset, c.Read.Secondary, c.Read.RetryAfter)
	}
}

// answersThrottle reports whether c is an answer th names: its status, its
// surface, its header at its value and its message phrase, read as a limit.
func answersThrottle(c *githubtest.LimitCase, th *spec.Throttle) bool {
	if c.Status != th.Status || c.Document != th.Document || c.Kind != forgeapi.KindRateLimited {
		return false
	}
	if th.Header != "" && (c.Header.Get(th.Header) == "" || th.Value != "" && c.Header.Get(th.Header) != th.Value) {
		return false
	}
	return th.Message == "" || carries(c, th.Message)
}

// figureSignals is a check per way f reads its figure, each reporting whether
// a case's Read takes the figure from that signal: the header for a pool, a
// remaining figure or a reset, and for the secondary mark each of its two
// signals on its own.
func figureSignals(f *spec.Figure) []func(*githubtest.LimitCase) bool {
	raw := func(c *githubtest.LimitCase) string { return c.Header.Get(f.Header) }
	switch f.Field {
	case "Pool":
		return []func(*githubtest.LimitCase) bool{func(c *githubtest.LimitCase) bool {
			return raw(c) != "" && c.Read.Pool == raw(c)
		}}
	case "Remaining":
		return []func(*githubtest.LimitCase) bool{func(c *githubtest.LimitCase) bool {
			return raw(c) != "" && strconv.Itoa(c.Read.Remaining) == raw(c)
		}}
	case "Reset":
		return []func(*githubtest.LimitCase) bool{func(c *githubtest.LimitCase) bool {
			return raw(c) != "" && strconv.FormatInt(c.Read.Reset.Unix(), 10) == raw(c)
		}}
	case "Secondary":
		return []func(*githubtest.LimitCase) bool{
			func(c *githubtest.LimitCase) bool { return c.Read.Secondary && raw(c) != "" && !carries(c, f.Message) },
			func(c *githubtest.LimitCase) bool { return c.Read.Secondary && raw(c) == "" && carries(c, f.Message) },
		}
	}
	return []func(*githubtest.LimitCase) bool{func(*githubtest.LimitCase) bool { return false }}
}

// carries reports whether c's message, a REST body's message member or one of
// a document envelope's errors, carries phrase in any case.
func carries(c *githubtest.LimitCase, phrase string) bool {
	var body struct {
		Message string `json:"message"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if json.Unmarshal(c.Body, &body) != nil {
		return false
	}
	messages := []string{body.Message}
	for _, e := range body.Errors {
		messages = append(messages, e.Message)
	}
	return slices.ContainsFunc(messages, func(m string) bool {
		return strings.Contains(strings.ToLower(m), strings.ToLower(phrase))
	})
}

// TestReadLimitReadsEveryAnswer holds ReadLimit to the figures of an answer
// that is no limit, a success's included, and to the one secondary match: the
// phrase outside the message member, or in a body that is not JSON, is none.
func TestReadLimitReadsEveryAnswer(t *testing.T) {
	reset := time.Unix(1790000000, 0).UTC()
	figures := http.Header{
		"X-Ratelimit-Limit": {"5000"}, "X-Ratelimit-Remaining": {"4999"}, "X-Ratelimit-Used": {"1"},
		"X-Ratelimit-Reset": {"1790000000"}, "X-Ratelimit-Resource": {"core"},
	}
	unlimited := github.RateLimit{Remaining: 4999, Limit: 5000, Reset: reset, Pool: "core"}
	for _, test := range []struct {
		header http.Header
		want   github.RateLimit
		name   string
		body   string
		status int
	}{
		{name: "a_success", status: http.StatusOK, header: figures, body: `{"login":"octocat"}`, want: unlimited},
		{
			name: "the_phrase_outside_the_message_member", status: http.StatusForbidden, header: figures,
			body: `{"message":"Forbidden","documentation_url":"https://docs.github.com/secondary rate limit"}`, want: unlimited,
		},
		{
			name: "the_phrase_in_an_envelope_error", status: http.StatusForbidden, header: figures,
			body: `{"errors":[{"message":"You have exceeded a secondary rate limit."}]}`, want: unlimited,
		},
		{name: "the_phrase_in_a_body_that_is_not_json", status: http.StatusForbidden, header: figures, body: "secondary rate limit", want: unlimited},
		{
			name: "the_message_in_any_case", status: http.StatusForbidden, header: figures,
			body: `{"message":"You have exceeded a SECONDARY RATE LIMIT."}`,
			want: github.RateLimit{Limited: true, Secondary: true, Remaining: 4999, Limit: 5000, Reset: reset, Pool: "core"},
		},
		{
			name: "a_reset_with_no_remaining_figure", status: http.StatusOK, header: http.Header{"X-Ratelimit-Reset": {"1790000000"}},
			body: `{}`, want: github.RateLimit{Remaining: forgeapi.BudgetRemainingUnknown, Reset: reset},
		},
		{
			name: "a_reset_beside_an_unreadable_remaining_figure", status: http.StatusOK,
			header: http.Header{"X-Ratelimit-Remaining": {"many"}, "X-Ratelimit-Reset": {"1790000000"}},
			body:   `{}`, want: github.RateLimit{Remaining: forgeapi.BudgetRemainingUnknown, Reset: reset},
		},
		{
			name: "an_answer_with_no_figures", status: http.StatusTooManyRequests, header: http.Header{}, body: `{}`,
			want: github.RateLimit{Limited: true, Remaining: forgeapi.BudgetRemainingUnknown},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := github.ReadLimit(test.status, test.header, []byte(test.body)); got != test.want {
				t.Errorf("ReadLimit = %+v, want %+v", got, test.want)
			}
		})
	}
}
