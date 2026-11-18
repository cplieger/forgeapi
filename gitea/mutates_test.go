package gitea

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/forgeapi/v3"
)

// marked is an attempt observer recording what forgeapi.Mutates read on each
// request it passes on.
type marked struct {
	seen []bool
	mu   sync.Mutex
}

func (m *marked) observe(next http.RoundTripper) http.RoundTripper {
	return roundTrip(func(r *http.Request) (*http.Response, error) {
		m.mu.Lock()
		m.seen = append(m.seen, forgeapi.Mutates(r))
		m.mu.Unlock()
		return next.RoundTrip(r)
	})
}

func (m *marked) all() []bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.seen)
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// An auto-merge the instance cannot merge now sends the merge twice, the second
// asking it to merge when checks succeed, and each send is a write.
func TestAnAutoMergeThatSchedulesMarksBothItsSends(t *testing.T) {
	seen := &marked{}
	h, forms := mergeAnsweringByForm(t, http.StatusMethodNotAllowed, http.StatusCreated, forgeapi.WithAttemptObserver(seen.observe))
	if _, err := h.client.MergePR(t.Context(), testRef(), testPR(), forgeapi.MergeRequest{Intent: forgeapi.IntentNoSquash, HeadSHA: testHeadSHA, AutoMerge: true}); err != nil {
		t.Fatalf("MergePR(AutoMerge) on a pull request not mergeable now = %v, want the scheduled merge", err)
	}
	if len(*forms) != 2 {
		t.Fatalf("Setup: MergePR(AutoMerge) sent %d form(s), want 2", len(*forms))
	}
	if got := seen.all(); !slices.Equal(got, []bool{true, true}) {
		t.Errorf("Mutates over the attempts of a scheduled auto-merge = %v, want [true true]", got)
	}
}

// A cancel with nothing scheduled meets the route's 404 and reads the pull request,
// and only the DELETE is a write.
func TestACancelMarksItsDeleteAndNotTheReadAfterIt(t *testing.T) {
	const cancel, read = "DELETE /api/v1/repos/example/example/pulls/1/merge", "GET /api/v1/repos/example/example/pulls/1"
	in := newInstance()
	in.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.EscapedPath()
		in.mu.Lock()
		in.requests = append(in.requests, key)
		in.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		answer := `{"number":1,"state":"open"}`
		switch {
		case key == cancel:
			w.WriteHeader(http.StatusNotFound)
			answer = `{"message":"The target couldn't be found."}`
		case key != read:
			w.WriteHeader(http.StatusInternalServerError)
		}
		if _, err := w.Write([]byte(answer)); err != nil {
			t.Errorf("Setup: writing the answer for %s: %v", key, err)
		}
	}))
	t.Cleanup(in.server.Close)
	seen := &marked{}
	h := newHarnessOver(t, in, time.Now, forgeapi.WithAttemptObserver(seen.observe))
	if err := h.client.CancelAutoMerge(t.Context(), testRef(), testPR()); err != nil {
		t.Fatalf("CancelAutoMerge with nothing scheduled = %v, want nil", err)
	}
	if got, want := in.arrived(), []string{cancel, read}; !slices.Equal(got, want) {
		t.Errorf("CancelAutoMerge with nothing scheduled sent %v, want %v", got, want)
	}
	if got := seen.all(); !slices.Equal(got, []bool{true, false}) {
		t.Errorf("Mutates over the attempts of a cancel with nothing scheduled = %v, want [true false]", got)
	}
}
