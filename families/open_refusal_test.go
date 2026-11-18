package families_test

import (
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync"
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

// A read the governor deferred asked the instance nothing, so it says nothing about any
// family: the walk stops there and answers the deferral, where reading it as GitHub's
// absence would ask the Gitea family next and answer family_undetected with its 404.
func TestOpenAnswersADeferredReadUnchanged(t *testing.T) {
	srv, conns := trackedInstance(t, detectedProducts["GitHub"], nil)
	reset := time.Now().Add(time.Hour).Truncate(time.Second)
	var mu sync.Mutex
	var sent []string
	lowPool := forgeapi.WithRequestObserver(func(next http.RoundTripper) http.RoundTripper {
		return roundTripFunc(func(r *http.Request) (*http.Response, error) {
			mu.Lock()
			sent = append(sent, r.Method+" "+r.URL.EscapedPath())
			mu.Unlock()
			resp, err := next.RoundTrip(r)
			if err == nil && r.URL.EscapedPath() == "/api/v3/meta" {
				resp.Header.Set("X-Ratelimit-Limit", "5000")
				resp.Header.Set("X-Ratelimit-Remaining", "200")
				resp.Header.Set("X-Ratelimit-Reset", strconv.FormatInt(reset.Unix(), 10))
				resp.Header.Set("X-Ratelimit-Resource", "core")
			}
			return resp, err
		})
	})
	opts := append(ownPoolOptions(), forgeapi.WithMutationReserve(250), lowPool)
	core, family, err := families.Open(t.Context(), forgeapi.Connection{WebBaseURL: srv.URL}, opts...)
	if core != nil || family != forgeapi.FamilyUnknown {
		t.Errorf("families.Open with GitHub's versions read deferred = (%v, %v), want no client and %v", core, family, forgeapi.FamilyUnknown)
	}
	var fe *forgeapi.Error
	if !errors.As(err, &fe) {
		t.Fatalf("families.Open with GitHub's versions read deferred = %v, want a *forgeapi.Error", err)
	}
	if fe.Code != "" || fe.Kind != forgeapi.KindRateLimited || fe.Status != 0 {
		t.Errorf("families.Open with GitHub's versions read deferred = code %q kind %v status %d, want no code, %v and no status",
			fe.Code, fe.Kind, fe.Status, forgeapi.KindRateLimited)
	}
	if fe.Pool != "core" || fe.Remaining != 200 || !fe.Reset.Equal(reset) {
		t.Errorf("families.Open with GitHub's versions read deferred = pool %q remaining %d reset %v, want core, 200 and %v",
			fe.Pool, fe.Remaining, fe.Reset, reset)
	}
	mu.Lock()
	got := slices.Clone(sent)
	mu.Unlock()
	if want := []string{"GET /api/v4/metadata", "GET /api/v3/meta"}; !slices.Equal(got, want) {
		t.Errorf("families.Open with GitHub's versions read deferred sent %v, want %v: no Gitea route after the deferral", got, want)
	}
	if n := conns.settlesAt(0); n != 0 {
		t.Errorf("families.Open with GitHub's versions read deferred left %d connections open, want 0", n)
	}
}
