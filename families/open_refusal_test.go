package families_test

import (
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/forgeapi/v3/families"
)

// heldRefusal stands in for a consumer's own refusal type, which the call's error
// must reach through errors.As.
type heldRefusal struct{}

func (*heldRefusal) Error() string { return "the pool is held" }

// countingInstance answers every request with status and header, counting them.
func countingInstance(t *testing.T, status int, header http.Header) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var sent atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sent.Add(1)
		maps.Copy(w.Header(), header)
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, &sent
}

// An attempt the hook refused sent nothing, so it says nothing about any family: the
// walk stops there and answers the refusal unchanged, where reading it as one family's
// absence would tell the caller to correct the instance URL.
func TestOpenAnswersARefusedAttemptUnchanged(t *testing.T) {
	srv, sent := countingInstance(t, http.StatusInternalServerError, nil)
	var attempts atomic.Int32
	hook := forgeapi.WithAttemptObserver(func(http.RoundTripper) http.RoundTripper {
		return roundTripFunc(func(*http.Request) (*http.Response, error) {
			attempts.Add(1)
			return nil, &heldRefusal{}
		})
	})
	core, family, err := families.Open(t.Context(), forgeapi.Connection{WebBaseURL: srv.URL}, append(openOptions(srv), hook)...)
	if core != nil || family != forgeapi.FamilyUnknown {
		t.Errorf("families.Open with every attempt refused = (%v, %v), want no client and %v", core, family, forgeapi.FamilyUnknown)
	}
	var fe *forgeapi.Error
	if !errors.As(err, &fe) || fe.Code != forgeapi.CodeAttemptRefused {
		t.Fatalf("families.Open with every attempt refused = %v, want code %q", err, forgeapi.CodeAttemptRefused)
	}
	if _, ok := errors.AsType[*heldRefusal](err); !ok {
		t.Errorf("families.Open's error %v does not reach the hook's own refusal through errors.As", err)
	}
	if n := attempts.Load(); n != 1 {
		t.Errorf("the hook saw %d attempt(s), want 1: the walk stops at the first refusal", n)
	}
	if n := sent.Load(); n != 0 {
		t.Errorf("the instance saw %d request(s), want 0", n)
	}
}

// A throttled last read leaves Open's refusal carrying that answer's own figures, as
// the family's error did, so a consumer holds on what the instance stated.
func TestOpenCarriesTheLastReadsFigures(t *testing.T) {
	header := http.Header{"Ratelimit": {"r=0;t=60"}, "Retry-After": {"30"}}
	srv, _ := countingInstance(t, http.StatusTooManyRequests, header)
	before := time.Now()
	_, _, err := families.Open(t.Context(), forgeapi.Connection{WebBaseURL: srv.URL}, openOptions(srv)...)
	var fe *forgeapi.Error
	if !errors.As(err, &fe) || fe.Code != forgeapi.CodeFamilyUndetected {
		t.Fatalf("families.Open against a throttle = %v, want code %q", err, forgeapi.CodeFamilyUndetected)
	}
	if fe.Kind != forgeapi.KindRateLimited || fe.Status != http.StatusTooManyRequests {
		t.Errorf("families.Open against a throttle = kind %v status %d, want %v and 429", fe.Kind, fe.Status, forgeapi.KindRateLimited)
	}
	if fe.Remaining != 0 || fe.Reset.Before(before.Add(time.Minute)) || fe.RetryAfter != 30*time.Second {
		t.Errorf("families.Open against a throttle = remaining %d reset %v retry-after %v, want 0, a minute after the answer and 30s",
			fe.Remaining, fe.Reset, fe.RetryAfter)
	}
}
