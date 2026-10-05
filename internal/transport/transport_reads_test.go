package transport

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
)

// documentRead is a GraphQL query, which travels as a POST and reads.
func documentRead() *Request {
	return &Request{
		Op: "ListMyPRs", Method: http.MethodPost, Document: true,
		Body: map[string]any{"query": "query Example { viewer { login } }"},
	}
}

// answering is an instance that counts what arrives and answers each request with
// the next status of statuses, then 200 once they run out.
func answering(t *testing.T, statuses ...int) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	seen := &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := seen.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if int(n) <= len(statuses) {
			w.WriteHeader(statuses[n-1])
		}
		if _, err := io.WriteString(w, `{"data":{}}`); err != nil {
			t.Errorf("Setup: writing the answer: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, seen
}

// TestADocumentReadIsSentOnAClientThatRefusesMutations holds the mutation gate to
// what an operation does rather than to the verb it travels on: a GraphQL query is a
// POST, and the read-only posture names the mutating operations, so a document read
// reaches the instance while a REST POST is refused before anything is sent.
func TestADocumentReadIsSentOnAClientThatRefusesMutations(t *testing.T) {
	srv, seen := answering(t)
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL}, forgeapi.WithMutations(false))
	read := documentRead()
	read.AbsoluteURL = srv.URL + "/graphql"
	if _, err := c.Do(t.Context(), read); err != nil {
		t.Fatalf("Do(a document read) on a read-only client = %v, want nil: a query mutates nothing", err)
	}
	if got := seen.Load(); got != 1 {
		t.Errorf("the instance saw %d request(s) for the document read, want 1", got)
	}

	_, err := c.Do(t.Context(), &Request{Op: "CreateIssue", Method: http.MethodPost, Path: "/x", Body: map[string]any{"a": 1}})
	var fe *forgeapi.Error
	if !asError(err, &fe) || fe.Code != forgeapi.CodeMutationsDisabled {
		t.Errorf("Do(a REST POST) on a read-only client = %v, want code %q", err, forgeapi.CodeMutationsDisabled)
	}
	if got := seen.Load(); got != 1 {
		t.Errorf("the instance saw %d request(s) after the refused mutation, want 1: the refusal sends nothing", got)
	}
}

// TestADocumentReadTakesTheReadPath holds the two properties the read path has and the
// mutation path does not: a transient failure is retried, and no inter-mutation
// interval holds the next read back.
func TestADocumentReadTakesTheReadPath(t *testing.T) {
	t.Run("retried", func(t *testing.T) {
		srv, seen := answering(t, http.StatusBadGateway)
		c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL}, forgeapi.WithRetries(1))
		read := documentRead()
		read.AbsoluteURL = srv.URL + "/graphql"
		if _, err := c.Do(t.Context(), read); err != nil {
			t.Fatalf("Do(a document read whose first attempt answered 502) = %v, want nil after the retry", err)
		}
		if got := seen.Load(); got != 2 {
			t.Errorf("the instance saw %d attempt(s), want 2: a query is safe to repeat", got)
		}
	})
	t.Run("not_paced", func(t *testing.T) {
		srv, _ := answering(t)
		c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL},
			forgeapi.WithMutationInterval(2*time.Second),
			forgeapi.WithOperationTimeout(time.Second),
		)
		for i := range 2 {
			read := documentRead()
			read.AbsoluteURL = srv.URL + "/graphql"
			if _, err := c.Do(t.Context(), read); err != nil {
				t.Fatalf("Do(document read %d) inside a 2s mutation interval = %v, want nil: the interval paces mutations", i+1, err)
			}
		}
	})
}
