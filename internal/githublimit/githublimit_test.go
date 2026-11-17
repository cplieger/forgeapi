package githublimit

import (
	"net/http"
	"testing"
)

// TestTheBudgetSignalIsReadFromTheResponseHeaders holds the signal reader to the two
// headers this product sends on both surfaces, and to answering NOTHING where the
// response carried none, which is what keeps the neutral value honest on a connection
// that has sent nothing.
func TestTheBudgetSignalIsReadFromTheResponseHeaders(t *testing.T) {
	header := http.Header{}
	header.Set("X-RateLimit-Remaining", "4990")
	header.Set("X-RateLimit-Reset", "1790000000")
	remaining, reset, ok := Signal(header)
	if !ok || remaining != 4990 {
		t.Errorf("Signal(the recorded headers) = %d, %v, want 4990 and a window", remaining, ok)
	}
	if reset.IsZero() || reset.Unix() != 1790000000 {
		t.Errorf("Signal(the recorded headers) = reset %v, want the epoch second the header names", reset)
	}
	if _, _, ok := Signal(http.Header{}); ok {
		t.Error("Signal(no headers) reported a figure, want none: a connection that has sent nothing has no budget to report")
	}
}

// TestThePoolIsTheResourceTheResponseNames holds the pool reader to the two headers
// this product sends on every response, the resource the request drew on and its
// whole limit.
func TestThePoolIsTheResourceTheResponseNames(t *testing.T) {
	name, limit := Pool(http.Header{"X-Ratelimit-Resource": {"graphql"}, "X-Ratelimit-Limit": {"5000"}})
	if name != "graphql" || limit != 5000 {
		t.Errorf("Pool(the recorded headers) = %q, %d, want graphql and 5000", name, limit)
	}
	if name, limit := Pool(http.Header{}); name != "" || limit != 0 {
		t.Errorf("Pool(no headers) = %q, %d, want the unnamed pool with no limit", name, limit)
	}
}
