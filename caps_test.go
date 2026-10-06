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
