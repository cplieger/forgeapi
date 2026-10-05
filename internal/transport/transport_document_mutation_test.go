package transport

import (
	"net/http"
	"testing"

	"github.com/cplieger/forgeapi"
)

// documentWrite is a GraphQL mutation, which travels as the same POST a query does
// and changes something on the instance.
func documentWrite() *Request {
	return &Request{
		Op: "MergePR", Method: http.MethodPost, Document: true, Mutation: true,
		Body: map[string]any{"query": `mutation Example { addStar(input: {starrableId: "x"}) { clientMutationId } }`},
	}
}

// TestADocumentMutationIsRefusedOnAClientThatRefusesMutations holds the mutation gate
// to what the document does rather than to the arm it travels on: the read-only
// posture refuses every mutating operation, and a GraphQL mutation changes the instance
// as a REST write does, so it is refused before anything is sent.
func TestADocumentMutationIsRefusedOnAClientThatRefusesMutations(t *testing.T) {
	srv, seen := answering(t)
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL}, forgeapi.WithMutations(false))
	write := documentWrite()
	write.AbsoluteURL = srv.URL + "/graphql"
	_, err := c.Do(t.Context(), write)
	var fe *forgeapi.Error
	if !asError(err, &fe) || fe.Code != forgeapi.CodeMutationsDisabled {
		t.Errorf("Do(a document mutation) on a read-only client = %v, want code %q", err, forgeapi.CodeMutationsDisabled)
	}
	if got := seen.Load(); got != 0 {
		t.Errorf("the instance saw %d request(s) for a refused document mutation, want 0", got)
	}
}

// TestADocumentMutationIsSentOnce holds a document mutation to the single attempt
// every write gets: a replayed write is a duplicate the inter-mutation interval cannot
// undo, so a gateway answer to the first attempt is the answer rather than a retry.
func TestADocumentMutationIsSentOnce(t *testing.T) {
	srv, seen := answering(t, http.StatusBadGateway)
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL}, forgeapi.WithRetries(1))
	write := documentWrite()
	write.AbsoluteURL = srv.URL + "/graphql"
	if _, err := c.Do(t.Context(), write); err == nil {
		t.Errorf("Do(a document mutation whose only attempt answered 502) = nil, want the failure")
	}
	if got := seen.Load(); got != 1 {
		t.Errorf("the instance saw %d request(s) for a document mutation answered 502, want 1: a write is never replayed", got)
	}
}
