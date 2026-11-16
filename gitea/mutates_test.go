package gitea

import (
	"net/http"
	"slices"
	"sync"
	"testing"

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
	if _, err := h.client.MergePR(t.Context(), testRef(), testPR(), forgeapi.MergeRequest{HeadSHA: testHeadSHA, AutoMerge: true}); err != nil {
		t.Fatalf("MergePR(AutoMerge) on a pull request not mergeable now = %v, want the scheduled merge", err)
	}
	if len(*forms) != 2 {
		t.Fatalf("Setup: MergePR(AutoMerge) sent %d form(s), want 2", len(*forms))
	}
	if got := seen.all(); !slices.Equal(got, []bool{true, true}) {
		t.Errorf("Mutates over the attempts of a scheduled auto-merge = %v, want [true true]", got)
	}
}
