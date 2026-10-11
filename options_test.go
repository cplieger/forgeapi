package forgeapi_test

import (
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
)

func TestResolveList_answers_the_first_open_page_without_options(t *testing.T) {
	got, err := forgeapi.ResolveList()
	if err != nil {
		t.Fatalf("ResolveList() = %v, want the defaults", err)
	}
	want := forgeapi.ListSettings{PageBound: 100, State: forgeapi.ListStateOpen}
	if got != want {
		t.Errorf("ResolveList() = %+v, want %+v", got, want)
	}
}

func TestResolveList_applies_the_last_of_repeated_options(t *testing.T) {
	got, err := forgeapi.ResolveList(
		forgeapi.WithPageBound(5),
		forgeapi.WithAfter("first"),
		forgeapi.WithPageBound(7),
		forgeapi.WithAfter("second"),
	)
	if err != nil {
		t.Fatalf("ResolveList(...) = %v, want the settings", err)
	}
	if got.PageBound != 7 || got.After != "second" {
		t.Errorf("ResolveList(...) = page bound %d after %q, want 7 and %q", got.PageBound, got.After, "second")
	}
}

func TestResolveList_refuses_an_option_it_cannot_send(t *testing.T) {
	for _, test := range []struct {
		name string
		code string
		opt  forgeapi.ListOption
	}{
		{name: "zero_page_bound", opt: forgeapi.WithPageBound(0), code: forgeapi.CodePageBoundInvalid},
		{name: "negative_page_bound", opt: forgeapi.WithPageBound(-1), code: forgeapi.CodePageBoundInvalid},
		{name: "unknown_state", opt: forgeapi.WithState(forgeapi.ListStateUnknown), code: forgeapi.CodeListStateInvalid},
		{name: "empty_owner", opt: forgeapi.WithOwner(""), code: forgeapi.CodeListOwnerInvalid},
		{name: "zero_since", opt: forgeapi.WithSince(time.Time{}), code: forgeapi.CodeListSinceInvalid},
		{name: "malformed_cursor", opt: forgeapi.WithAfter("a/b"), code: forgeapi.CodeCursorInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := forgeapi.ResolveList(test.opt)
			if problem := localRefusal(err, test.code); problem != nil {
				t.Errorf("ResolveList(%s): %v", test.name, problem)
			}
		})
	}
}

func TestWithSince_records_the_bound_in_UTC_without_a_monotonic_reading(t *testing.T) {
	zone := time.FixedZone("UTC+2", 2*60*60)
	bound := time.Date(2026, 3, 4, 7, 8, 9, 10, zone)
	got, err := forgeapi.ResolveList(forgeapi.WithSince(time.Now()), forgeapi.WithSince(bound))
	if err != nil {
		t.Fatalf("ResolveList(WithSince(%v)) = %v, want the settings", bound, err)
	}
	want := time.Date(2026, 3, 4, 5, 8, 9, 0, time.UTC)
	if !got.SinceSet || got.Since != want {
		t.Errorf("ResolveList(WithSince(%v)) = since %v (set %t), want %v set: the last bound, in UTC at whole seconds with no monotonic reading", bound, got.Since, got.SinceSet, want)
	}
	if got.Since.Location() != time.UTC {
		t.Errorf("ResolveList(WithSince(%v)) = location %v, want UTC", bound, got.Since.Location())
	}
}
