package gitea

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
)

// mergeAnswering is a harness whose merge route answers status with the empty body
// every product of this family answers a merge with, and records the form it received.
func mergeAnswering(t *testing.T, status int) (*harness, *map[string]any) {
	t.Helper()
	form := &map[string]any{}
	in := newInstance()
	in.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		in.mu.Lock()
		in.requests = append(in.requests, r.Method+" "+r.URL.EscapedPath())
		in.mu.Unlock()
		if err := json.Unmarshal([]byte(readAll(t, r)), form); err != nil {
			t.Errorf("decoding the merge form: %v", err)
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(in.server.Close)
	return newHarnessOver(t, in, time.Now), form
}

// mergeAnsweringByForm is a harness whose merge route answers bare to a form without
// merge_when_checks_succeed and flagged to one carrying it, and records every form.
func mergeAnsweringByForm(t *testing.T, bare, flagged int) (*harness, *[]map[string]any) {
	t.Helper()
	forms := &[]map[string]any{}
	in := newInstance()
	in.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		form := map[string]any{}
		if err := json.Unmarshal([]byte(readAll(t, r)), &form); err != nil {
			t.Errorf("decoding the merge form: %v", err)
		}
		in.mu.Lock()
		in.requests = append(in.requests, r.Method+" "+r.URL.EscapedPath())
		*forms = append(*forms, form)
		in.mu.Unlock()
		if form["merge_when_checks_succeed"] == true {
			w.WriteHeader(flagged)
			return
		}
		w.WriteHeader(bare)
	}))
	t.Cleanup(in.server.Close)
	return newHarnessOver(t, in, time.Now), forms
}

// TestAutoMergeAsksTheInstanceToMergeWhenChecksSucceed holds `AutoMerge` to ADR-0103's
// Decision on this family, a merge asked to wait completes now where its requirements
// are met and is armed otherwise: the merge form without the flag goes first, its 405
// is the route's not-mergeable-now answer, and the form carrying
// merge_when_checks_succeed follows, whose 201 is the scheduled merge, measured on
// Gitea 28.0.0, Forgejo 16.0.5 and Forgejo 15.0.9 alike, which reads as enqueued.
func TestAutoMergeAsksTheInstanceToMergeWhenChecksSucceed(t *testing.T) {
	h, forms := mergeAnsweringByForm(t, http.StatusMethodNotAllowed, http.StatusCreated)
	got, err := h.client.MergePR(t.Context(), testRef(), testPR(), forgeapi.MergeRequest{HeadSHA: testHeadSHA, AutoMerge: true})
	if err != nil {
		t.Fatalf("MergePR(AutoMerge) on a pull request not mergeable now = %v, want the scheduled merge", err)
	}
	if got.State != forgeapi.MergeOutcomeEnqueued || got.Code != "" || got.QueueState != forgeapi.QueueNone {
		t.Errorf("MergePR(AutoMerge) on a pull request not mergeable now = %v / %q / %v, want %v with no code and %v",
			got.State, got.Code, got.QueueState, forgeapi.MergeOutcomeEnqueued, forgeapi.QueueNone)
	}
	if len(*forms) != 2 || (*forms)[0]["merge_when_checks_succeed"] != nil || (*forms)[1]["merge_when_checks_succeed"] != true {
		t.Errorf("MergePR(AutoMerge) on a pull request not mergeable now sent the forms %v, want the flagless form then one with merge_when_checks_succeed true", *forms)
	}
}

// TestAnAutoMergeOnAPullRequestMergeableNowMergesIt holds the other arm of ADR-0103's
// Decision: where the flagless form merges, the merge is done and the flag that would
// schedule it is never sent, so Forgejo, which holds a scheduled merge for the next
// commit status, never waits on one.
func TestAnAutoMergeOnAPullRequestMergeableNowMergesIt(t *testing.T) {
	h, forms := mergeAnsweringByForm(t, http.StatusOK, http.StatusCreated)
	got, err := h.client.MergePR(t.Context(), testRef(), testPR(), forgeapi.MergeRequest{HeadSHA: testHeadSHA, AutoMerge: true})
	if err != nil {
		t.Fatalf("MergePR(AutoMerge) on a pull request mergeable now = %v, want the merge", err)
	}
	if got.State != forgeapi.MergeOutcomeMerged {
		t.Errorf("MergePR(AutoMerge) on a pull request mergeable now = %v, want %v", got.State, forgeapi.MergeOutcomeMerged)
	}
	if len(*forms) != 1 || (*forms)[0]["merge_when_checks_succeed"] != nil {
		t.Errorf("MergePR(AutoMerge) on a pull request mergeable now sent the forms %v, want the flagless form alone", *forms)
	}
}

// TestAMergeNotAskedToWaitCarriesNoSchedulingFlag holds the default: the form carries no
// merge_when_checks_succeed key at all, and the instance's 200 is the merge done.
func TestAMergeNotAskedToWaitCarriesNoSchedulingFlag(t *testing.T) {
	h, form := mergeAnswering(t, http.StatusOK)
	got, err := h.client.MergePR(t.Context(), testRef(), testPR(), forgeapi.MergeRequest{HeadSHA: testHeadSHA})
	if err != nil {
		t.Fatalf("MergePR answered 200 = %v, want the merge", err)
	}
	if got.State != forgeapi.MergeOutcomeMerged {
		t.Errorf("MergePR answered 200 = %v, want %v", got.State, forgeapi.MergeOutcomeMerged)
	}
	if _, ok := (*form)["merge_when_checks_succeed"]; ok {
		t.Errorf("MergePR without AutoMerge sent the form %v, want no merge_when_checks_succeed key", *form)
	}
}
