package forgeapi_test

import (
	"testing"

	"github.com/cplieger/forgeapi"
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
