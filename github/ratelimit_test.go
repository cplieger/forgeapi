package github_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/forgeapi/v3/github"
	"github.com/cplieger/forgeapi/v3/github/githubtest"
)

type staticToken struct{}

func (staticToken) Token(context.Context) (string, error) { return "placeholder", nil }
func (staticToken) Kind() forgeapi.CredKind               { return forgeapi.CredKindStaticPAT }
func (staticToken) State() forgeapi.CredState             { return forgeapi.CredValid }

// whoamiAgainst is the error Whoami answers where GitHub answers every request
// with status, header and body.
func whoamiAgainst(t *testing.T, status int, header http.Header, body []byte) error {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		maps.Copy(w.Header(), header)
		w.WriteHeader(status)
		if _, err := w.Write(body); err != nil {
			t.Errorf("Setup: writing the answer: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	client, err := github.New(forgeapi.Connection{WebBaseURL: srv.URL, APIBaseURL: srv.URL + "/api/v3"},
		forgeapi.WithWireTransport(srv.Client().Transport),
		forgeapi.WithCredentialSource(staticToken{}),
		forgeapi.WithPlaintextHTTP(true),
		forgeapi.WithPrivateAddresses(true),
		forgeapi.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatalf("Setup: github.New: %v", err)
	}
	_, err = client.Whoami(t.Context())
	return err
}

// TestReadLimit runs every rate-limit case through ReadLimit and through the
// client's error on a REST read, which must state the same facts: the limit,
// the pool, the figures, the secondary mark and the wait.
func TestReadLimit(t *testing.T) {
	for _, c := range githubtest.LimitCases(time.Now()) {
		t.Run(c.Name, func(t *testing.T) {
			if got := github.ReadLimit(c.Status, c.Header, c.Body); got != c.Read {
				t.Errorf("ReadLimit = %+v, want %+v", got, c.Read)
			}
			fe, ok := errors.AsType[*forgeapi.Error](whoamiAgainst(t, c.Status, c.Header, c.Body))
			if !ok {
				t.Fatal("Whoami answered no *forgeapi.Error")
			}
			if fe.Kind != c.Kind || fe.Status != c.Status {
				t.Errorf("Whoami = kind %v status %d, want %v, %d", fe.Kind, fe.Status, c.Kind, c.Status)
			}
			if fe.Pool != c.Read.Pool || fe.Remaining != c.Read.Remaining || !fe.Reset.Equal(c.Read.Reset) ||
				fe.Secondary != c.Read.Secondary || fe.RetryAfter != c.Read.RetryAfter {
				t.Errorf("Whoami = pool %q remaining %d reset %v secondary %t retry after %v, want ReadLimit's %q, %d, %v, %t, %v",
					fe.Pool, fe.Remaining, fe.Reset, fe.Secondary, fe.RetryAfter,
					c.Read.Pool, c.Read.Remaining, c.Read.Reset, c.Read.Secondary, c.Read.RetryAfter)
			}
		})
	}
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
