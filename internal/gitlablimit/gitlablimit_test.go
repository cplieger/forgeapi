package gitlablimit

import (
	"net/http"
	"testing"
)

// TestTheBudgetSignalIsReadFromEveryResponse holds the signal this product rides on
// every response, a refusal included, which is what makes the governor's figure
// cost-free: a reserve held against a figure nobody read would read as full at the
// moment the quota was gone.
func TestTheBudgetSignalIsReadFromEveryResponse(t *testing.T) {
	header := http.Header{
		"Ratelimit-Remaining": {"4990"},
		"Ratelimit-Reset":     {"1790000000"},
	}
	remaining, reset, ok := Signal(header)
	if !ok {
		t.Fatal("Signal read no figure from the headers this product sends on every response")
	}
	if remaining != 4990 {
		t.Errorf("Signal = remaining %d, want 4990", remaining)
	}
	if reset.IsZero() || reset.Unix() != 1790000000 {
		t.Errorf("Signal = reset %v, want the epoch second the header carries", reset)
	}
	if _, _, ok := Signal(http.Header{}); ok {
		t.Error("Signal read a figure from a response carrying no header, so a connection that has seen no answer would publish a budget it never read")
	}
	alone := http.Header{}
	alone.Set("RateLimit-Reset", "1790000000")
	if _, reset, ok := Signal(alone); ok || reset.Unix() != 1790000000 {
		t.Errorf("Signal(a reset alone) = reset %v, ok %t, want the epoch second the header names and no remaining figure", reset, ok)
	}
}

// TestThePoolIsTheThrottleTheResponseNames holds the pool reader to the two headers
// this product sends on every response, the throttle's name and its whole limit.
func TestThePoolIsTheThrottleTheResponseNames(t *testing.T) {
	name, limit := Pool(http.Header{"Ratelimit-Name": {"burst_traffic_per_user_free_plan"}, "Ratelimit-Limit": {"100"}})
	if name != "burst_traffic_per_user_free_plan" || limit != 100 {
		t.Errorf("Pool(the recorded headers) = %q, %d, want burst_traffic_per_user_free_plan and 100", name, limit)
	}
	if name, limit := Pool(http.Header{}); name != "" || limit != 0 {
		t.Errorf("Pool(no headers) = %q, %d, want the unnamed pool with no limit", name, limit)
	}
}
