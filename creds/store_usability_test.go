package creds_test

import (
	"slices"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
)

// A zero-valued record is unmarked, so the kind and the expiry decide for every
// record built without a mark, which is every record a connect saves.
func TestRecord_zero_value_is_unmarked(t *testing.T) {
	if got := (creds.Record{}).Usability; got != creds.UsabilityUnmarked {
		t.Errorf("creds.Record{}.Usability = %v, want %v", got, creds.UsabilityUnmarked)
	}
}

func TestFileStore_answers_a_marked_record_with_its_mark(t *testing.T) {
	for _, mark := range []creds.Usability{markSpent, markReconnectRequired} {
		t.Run(markNames[mark], func(t *testing.T) {
			store, dir := openStore(t)
			save(t, store, "conn", withMark(rotating(forgeapi.FamilyGitHub, "https://forge.example", 8*time.Hour, 8*time.Hour), mark))

			if got := load(t, reopen(t, dir), "conn").Usability; got != mark {
				t.Errorf("the reopened store's record is %s, want %s: the mark is written to the store so a reader in another process honours it",
					markNames[got], markNames[mark])
			}
		})
	}
}

// The member is omitted when the record is unmarked, which is also how every file
// written before the member existed reads.
func TestFileStore_writes_no_usability_member_for_an_unmarked_record(t *testing.T) {
	store, dir := openStore(t)
	save(t, store, "conn", withMark(rotating(forgeapi.FamilyGitLab, "https://forge.example", 2*time.Hour, 2*time.Hour), markUnmarked))

	if got := fileMarks(t, dir); len(got) != 0 {
		t.Errorf("%s holds usability member(s) %v for an unmarked record, want none", credentialFile, got)
	}
}

func TestFileStore_spells_each_mark_as_the_format_rule_names_it(t *testing.T) {
	tests := []struct {
		spelling string
		mark     creds.Usability
	}{
		{mark: markSpent, spelling: `"spent"`},
		{mark: markReconnectRequired, spelling: `"reconnect_required"`},
	}
	for _, tc := range tests {
		t.Run(markNames[tc.mark], func(t *testing.T) {
			store, dir := openStore(t)
			save(t, store, "conn", withMark(rotating(forgeapi.FamilyGitHub, "https://forge.example", 8*time.Hour, 8*time.Hour), tc.mark))

			if got := fileMarks(t, dir); !slices.Equal(got, []string{tc.spelling}) {
				t.Errorf("%s holds usability member(s) %v, want [%s]", credentialFile, got, tc.spelling)
			}
		})
	}
}

// A record without the member is how a file written before the member reads, so it
// reads as it did then: unmarked, and the kind and the expiry decide.
func TestFileStore_reads_a_record_without_the_member_as_unmarked(t *testing.T) {
	store, dir := openStore(t)
	ep := newEndpoint(t, unreachable(t))
	save(t, store, "conn", withMark(rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, 8*time.Hour), markSpent))
	rewriteMarks(t, dir, "", true)

	if got := load(t, reopen(t, dir), "conn").Usability; got != markUnmarked {
		t.Errorf("a record without the usability member reads as %s, want unmarked", markNames[got])
	}
	token, err := source(t, reopen(t, dir), "conn", ep).Token(t.Context())
	if err != nil || token != "token-old" {
		t.Errorf("Token() over a fresh record without the member = (%q, %v), want (%q, nil)", token, err, "token-old")
	}
}

// A consumer can build a record holding a value outside the members, and an unknown
// mark must not read as usable.
func TestFileStore_reads_a_mark_outside_the_members_as_reconnect_required(t *testing.T) {
	store, dir := openStore(t)
	ep := newEndpoint(t, unreachable(t))
	save(t, store, "conn", withMark(rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, 8*time.Hour), creds.Usability(7)))

	if got := fileMarks(t, dir); !slices.Equal(got, []string{`"reconnect_required"`}) {
		t.Errorf("%s holds usability member(s) %v for mark 7, want [\"reconnect_required\"]", credentialFile, got)
	}
	token, err := source(t, reopen(t, dir), "conn", ep).Token(t.Context())
	if code := codeOf(err); code != forgeapi.CodeReconnectRequired || token != "" {
		t.Errorf("Token() over mark 7 = (%q, %v), want no token and code %q", token, err, forgeapi.CodeReconnectRequired)
	}
}

// The unmarked zero is the member that lets the kind and the expiry decide, so a
// present member holding anything this library does not write must not read as
// usable: the zero member's own spelling, which an unmarked record omits, a null
// and a value that is not a spelling at all among them.
func TestFileStore_reads_a_spelling_it_does_not_write_as_reconnect_required(t *testing.T) {
	for name, spelling := range map[string]any{
		"exhausted": "exhausted", "Spent": "Spent", "empty": "", "unmarked": "unmarked",
		"null": nil, "number": 1,
	} {
		t.Run("spelling_"+name, func(t *testing.T) {
			store, dir := openStore(t)
			ep := newEndpoint(t, unreachable(t))
			save(t, store, "conn", withMark(rotating(forgeapi.FamilyGitLab, ep.srv.URL, 2*time.Hour, 2*time.Hour), markSpent))
			rewriteMarks(t, dir, spelling, false)

			if got := load(t, reopen(t, dir), "conn").Usability; got != markReconnectRequired {
				t.Errorf("usability %v reads as %s, want reconnect required", spelling, markNames[got])
			}
			token, err := source(t, reopen(t, dir), "conn", ep).Token(t.Context())
			if code := codeOf(err); code != forgeapi.CodeReconnectRequired || token != "" {
				t.Errorf("Token() over usability %v = (%q, %v), want no token and code %q", spelling, token, err, forgeapi.CodeReconnectRequired)
			}
		})
	}
}
