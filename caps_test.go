package forgeapi_test

import (
	"testing"

	"github.com/cplieger/forgeapi/v3"
)

func TestEvidenceSource_String_renders_the_wire_spelling(t *testing.T) {
	for _, test := range []struct {
		source forgeapi.EvidenceSource
		want   string
	}{
		{source: forgeapi.EvidenceUnknown, want: "unknown"},
		{source: forgeapi.EvidenceProbe, want: "probe"},
		{source: forgeapi.EvidenceResponseHeader, want: "response-header"},
	} {
		t.Run(test.want, func(t *testing.T) {
			if got := test.source.String(); got != test.want {
				t.Errorf("EvidenceSource(%q).String() = %q, want %q", string(test.source), got, test.want)
			}
		})
	}
}

func TestProduct_String_renders_each_product(t *testing.T) {
	for product, want := range map[forgeapi.Product]string{
		forgeapi.ProductUnknown: "unknown",
		forgeapi.ProductGitHub:  "github",
		forgeapi.ProductGitLab:  "gitlab",
		forgeapi.ProductGitea:   "gitea",
		forgeapi.ProductForgejo: "forgejo",
		forgeapi.Product(99):    "unknown",
	} {
		if got := product.String(); got != want {
			t.Errorf("Product(%d).String() = %q, want %q", int(product), got, want)
		}
	}
}

func TestRerunScope_String_renders_each_member(t *testing.T) {
	for scope, want := range map[forgeapi.RerunScope]string{
		forgeapi.RerunScopeUnknown:    "unknown",
		forgeapi.RerunScopeFailedJobs: "failed_jobs",
		forgeapi.RerunScopeWholeRun:   "whole_run",
		forgeapi.RerunScope(9):        "unknown",
	} {
		if got := scope.String(); got != want {
			t.Errorf("RerunScope(%d).String() = %q, want %q", int(scope), got, want)
		}
	}
}

func TestCheckState_String_renders_each_member(t *testing.T) {
	for state, want := range map[forgeapi.CheckState]string{
		forgeapi.CheckUnknown:  "unknown",
		forgeapi.CheckPassing:  "passing",
		forgeapi.CheckFailing:  "failing",
		forgeapi.CheckPending:  "pending",
		forgeapi.CheckNeutral:  "neutral",
		forgeapi.CheckNone:     "none",
		forgeapi.CheckState(9): "unknown",
	} {
		if got := state.String(); got != want {
			t.Errorf("CheckState(%d).String() = %q, want %q", int(state), got, want)
		}
	}
}

func TestCheckNone_is_appended_after_every_earlier_member(t *testing.T) {
	if forgeapi.CheckNone != forgeapi.CheckNeutral+1 {
		t.Errorf("CheckNone = %d, want %d: appended after neutral, so every earlier member keeps its value",
			int(forgeapi.CheckNone), int(forgeapi.CheckNeutral+1))
	}
}

func TestRunTrigger_String_renders_each_trigger(t *testing.T) {
	for trigger, want := range map[forgeapi.RunTrigger]string{
		forgeapi.TriggerUnknown:     "unknown",
		forgeapi.TriggerPush:        "push",
		forgeapi.TriggerPullRequest: "pull_request",
		forgeapi.TriggerSchedule:    "schedule",
		forgeapi.TriggerManual:      "manual",
		forgeapi.TriggerOther:       "other",
		forgeapi.RunTrigger(-1):     "unknown",
	} {
		if got := trigger.String(); got != want {
			t.Errorf("RunTrigger(%d).String() = %q, want %q", int(trigger), got, want)
		}
	}
}
