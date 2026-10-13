package transport

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/cplieger/forgeapi"
)

// heldOrigin is an origin that, while hold is set, parks each request it receives
// until release is closed, after signalling received.
type heldOrigin struct {
	*origin
	received chan struct{}
	release  chan struct{}
	hold     atomic.Bool
}

func newHeldOrigin(body, etag string) *heldOrigin {
	return &heldOrigin{origin: newOrigin(body, etag), received: make(chan struct{}, 1), release: make(chan struct{})}
}

func (h *heldOrigin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.hold.CompareAndSwap(true, false) {
		h.received <- struct{}{}
		<-h.release
	}
	h.origin.ServeHTTP(w, r)
}

// readAcrossClose starts a read, closes c once the instance has received it, then
// lets the instance answer, and reports what the read returned.
func readAcrossClose(t *testing.T, c *Conn, h *heldOrigin) (account, error) {
	t.Helper()
	h.hold.Store(true)
	type result struct {
		err error
		got account
	}
	done := make(chan result, 1)
	go func() {
		got, _, err := readOn(t.Context(), c, nil)
		done <- result{got: got, err: err}
	}()
	<-h.received
	c.Close()
	close(h.release)
	r := <-done
	return r.got, r.err
}

// Close empties the cache, and a read already in flight when it does keeps nothing
// once it is answered, while a read begun after it is kept as before.
func TestAReadInFlightAcrossCloseKeepsNothing(t *testing.T) {
	h := newHeldOrigin(`{"login":"octo"}`, `"v1"`)
	srv := httptest.NewServer(h)
	defer srv.Close()
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL})
	got, err := readAcrossClose(t, c, h)
	if err != nil || got.Login != "octo" {
		t.Fatalf("the read in flight across Close = %+v, %v, want the instance's answer", got, err)
	}
	if held := c.BudgetState().ReadCache.Bytes; held != 0 {
		t.Errorf("after a read answered across Close the cache holds %d bytes, want none", held)
	}
	if _, _, err := read(t, c, nil); err != nil {
		t.Fatal(err)
	}
	if c.BudgetState().ReadCache.Bytes == 0 {
		t.Errorf("a read begun after Close kept nothing, want its answer kept")
	}
}

// A read that carried the held answer's validator into Close is not served from an
// entry Close released: it is sent once more without a validator, and kept nowhere.
func TestANotModifiedAnswerAcrossCloseIsReadInFull(t *testing.T) {
	h := newHeldOrigin(`{"login":"octo"}`, `"v1"`)
	srv := httptest.NewServer(h)
	defer srv.Close()
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL})
	if _, _, err := read(t, c, nil); err != nil {
		t.Fatal(err)
	}
	got, err := readAcrossClose(t, c, h)
	if err != nil || got.Login != "octo" {
		t.Fatalf("the read revalidated across Close = %+v, %v, want the instance's answer", got, err)
	}
	sent := h.requests()
	if len(sent) != 3 || sent[1].Get("If-None-Match") != `"v1"` || sent[2].Get("If-None-Match") != "" {
		t.Errorf("the read across Close sent %d request(s), want the conditional one then one with no validator", len(sent))
	}
	if held := c.BudgetState().ReadCache.Bytes; held != 0 {
		t.Errorf("after a read revalidated across Close the cache holds %d bytes, want none", held)
	}
}
