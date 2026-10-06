package forgeapi_test

import (
	"testing"

	"github.com/cplieger/forgeapi"
)

func TestError_Error_renders_only_the_parts_present(t *testing.T) {
	for _, test := range []struct {
		name string
		want string
		err  forgeapi.Error
	}{
		{
			name: "local_refusal",
			err:  forgeapi.Error{Code: forgeapi.CodeRefInvalid, Message: "value is empty"},
			want: "ref_invalid: value is empty",
		},
		{
			name: "upstream_answer",
			err:  forgeapi.Error{Op: "MergePR", Code: forgeapi.CodeNotMergeable, Status: 405, Message: "not mergeable"},
			want: "MergePR: not_mergeable (status 405): not mergeable",
		},
		{
			name: "deferral_without_a_code",
			err:  forgeapi.Error{Op: "ListPRs", Message: "this read was deferred"},
			want: "ListPRs: this read was deferred",
		},
		{
			name: "throttle_without_a_code_or_message",
			err:  forgeapi.Error{Op: "ListPRs", Status: 429},
			want: "ListPRs (status 429)",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.err.Error(); got != test.want {
				t.Errorf("(%+v).Error() = %q, want %q", test.err, got, test.want)
			}
		})
	}
}
