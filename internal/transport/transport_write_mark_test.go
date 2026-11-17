package transport

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/forgeapi/v3/internal/callmark"
)

// marks records, per attempt the observer saw, whether forgeapi.Mutates reads it
// as a write.
type marks struct {
	seen []bool
	mu   sync.Mutex
}

func (m *marks) observer(next http.RoundTripper) http.RoundTripper {
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		m.mu.Lock()
		m.seen = append(m.seen, forgeapi.Mutates(r))
		m.mu.Unlock()
		return next.RoundTrip(r)
	})
}

func (m *marks) all() []bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.seen)
}

// askedCredential records whether the context it is asked for a token on carries
// the write mark, which a source minting over the wire would carry onto its mint.
type askedCredential struct{ marked *atomic.Bool }

func (c askedCredential) Token(ctx context.Context) (string, error) {
	if callmark.IsWrite(ctx) {
		c.marked.Store(true)
	}
	return "transport-placeholder", nil
}
func (askedCredential) Kind() forgeapi.CredKind   { return forgeapi.CredKindStaticPAT }
func (askedCredential) State() forgeapi.CredState { return forgeapi.CredValid }

func answeringServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, `{}`); err != nil {
			t.Errorf("Setup: writing the answer: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestEachAttemptIsMarkedAWriteExactlyWhereItChangesForgeState(t *testing.T) {
	for _, test := range []struct {
		req  Request
		name string
		want bool
	}{
		{name: "a_rest_read", req: Request{Op: "ReadPR", Method: http.MethodGet, Path: "/pulls/1"}},
		{name: "a_rest_write", req: Request{Op: "ClosePR", Method: http.MethodPatch, Path: "/pulls/1"}, want: true},
		{name: "a_rest_delete", req: Request{Op: "RemoveLabels", Method: http.MethodDelete, Path: "/issues/1/labels/x"}, want: true},
		{name: "a_document_query", req: Request{Op: "ListPRs", Method: http.MethodPost, Path: "/graphql", Document: true}},
		{name: "a_document_mutation", req: Request{Op: "MergePR", Method: http.MethodPost, Path: "/graphql", Document: true, Mutation: true}, want: true},
		{name: "a_credential_mint", req: Request{Op: "Mint", Method: http.MethodPost, Path: "/app/installations/1/access_tokens", Credential: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := &marks{}
			c := openTestConn(t, forgeapi.Connection{WebBaseURL: answeringServer(t).URL}, forgeapi.WithAttemptObserver(m.observer))
			req := test.req
			if _, err := c.Do(Call(t.Context(), req.Op), &req); err != nil {
				t.Fatalf("%s %s = %v, want it sent", req.Method, req.Path, err)
			}
			if got := m.all(); !slices.Equal(got, []bool{test.want}) {
				t.Errorf("Mutates over the attempts of %s %s = %v, want [%t]", req.Method, req.Path, got, test.want)
			}
		})
	}
}

func TestARetriedReadIsNeverMarked(t *testing.T) {
	var answered atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if answered.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
		}
		if _, err := io.WriteString(w, `{}`); err != nil {
			t.Errorf("Setup: writing the answer: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	m := &marks{}
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL}, forgeapi.WithRetries(1), forgeapi.WithAttemptObserver(m.observer))
	if _, err := c.Do(Call(t.Context(), "ReadPR"), &Request{Op: "ReadPR", Method: http.MethodGet, Path: "/pulls/1"}); err != nil {
		t.Fatalf("a read whose first attempt answered 502 = %v, want the retry's answer", err)
	}
	if got := m.all(); !slices.Equal(got, []bool{false, false}) {
		t.Errorf("Mutates over a retried read's attempts = %v, want [false false]", got)
	}
}

// A mutating operation's own read is part of its write for the reserve, and still
// changes nothing, so the observer reads it as a read.
func TestAWritesOwnReadIsNotMarked(t *testing.T) {
	m := &marks{}
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: answeringServer(t).URL}, forgeapi.WithAttemptObserver(m.observer))
	ctx := CallWrite(t.Context(), "MergePR")
	if _, err := c.Do(ctx, &Request{Op: "MergePR", Method: http.MethodGet, Path: "/pulls/1"}); err != nil {
		t.Fatalf("the merge's read = %v, want it sent", err)
	}
	if _, err := c.Do(ctx, &Request{Op: "MergePR", Method: http.MethodPut, Path: "/pulls/1/merge"}); err != nil {
		t.Fatalf("the merge = %v, want it sent", err)
	}
	if got := m.all(); !slices.Equal(got, []bool{false, true}) {
		t.Errorf("Mutates over a merge's read then its write = %v, want [false true]", got)
	}
}

func TestTheCredentialIsAskedOnAnUnmarkedContext(t *testing.T) {
	marked := &atomic.Bool{}
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: answeringServer(t).URL}, forgeapi.WithCredentialSource(askedCredential{marked: marked}))
	if _, err := c.Do(Call(t.Context(), "ClosePR"), &Request{Op: "ClosePR", Method: http.MethodPatch, Path: "/pulls/1"}); err != nil {
		t.Fatalf("the write = %v, want it sent", err)
	}
	if marked.Load() {
		t.Error("the credential source was asked for the write's token on a context marked as a write, want it unmarked: a token it mints over the wire is not a write")
	}
}

func TestAReadOnlyClientMarksNothing(t *testing.T) {
	m := &marks{}
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: answeringServer(t).URL},
		forgeapi.WithMutations(false), forgeapi.WithAttemptObserver(m.observer))
	if _, err := c.Do(Call(t.Context(), "ClosePR"), &Request{Op: "ClosePR", Method: http.MethodPatch, Path: "/pulls/1"}); err == nil {
		t.Fatal("a write on a read-only client = nil error, want the refusal")
	}
	if _, err := c.Do(Call(t.Context(), "ReadPR"), &Request{Op: "ReadPR", Method: http.MethodGet, Path: "/pulls/1"}); err != nil {
		t.Fatalf("a read on a read-only client = %v, want it sent", err)
	}
	if got := m.all(); !slices.Equal(got, []bool{false}) {
		t.Errorf("Mutates over a read-only client's attempts = %v, want [false], the read alone", got)
	}
}
