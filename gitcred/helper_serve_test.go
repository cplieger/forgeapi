package gitcred_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/gitcred"
)

// Two connections may share an origin, and git must get the same one on every
// invocation rather than whichever the store listed first.
func TestGet_answers_the_first_connection_by_key_where_several_share_the_origin(t *testing.T) {
	store, _ := openStore(t)
	for _, key := range []string{"conn-b", "conn-c", "conn-a"} {
		rec := record(forgeapi.FamilyGitHub, forge)
		rec.Token = "token-of-" + key
		save(t, store, key, rec)
	}
	c := &tally{}

	got := serve(t, helper(store, c), "get", attributes("https", "forge.example"))
	if got.attrs["password"] != "token-of-conn-a" {
		t.Errorf("get with three connections at one origin = password %q, want %q", got.attrs["password"], "token-of-conn-a")
	}
}

func TestServe_answers_nothing_under_a_cancelled_context(t *testing.T) {
	store, _ := openStore(t)
	save(t, store, "conn", record(forgeapi.FamilyGitHub, forge))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	var out, diag bytes.Buffer
	err := helper(store, &tally{}).Serve(ctx, "get", strings.NewReader(attributes("https", "forge.example")), &out, &diag)
	if !errors.Is(err, context.Canceled) || out.Len() != 0 {
		t.Errorf("Serve(get) under a cancelled context = (%d bytes, %v), want (none, context.Canceled)", out.Len(), err)
	}
}

func TestServe_refuses_an_attribute_block_over_its_bound(t *testing.T) {
	store, _ := openStore(t)
	save(t, store, "conn", record(forgeapi.FamilyGitHub, forge))
	block := "protocol=https\nhost=forge.example\nwwwauth[]=" + strings.Repeat("x", 1<<20) + "\n\n"

	var out, diag bytes.Buffer
	err := helper(store, &tally{}).Serve(t.Context(), "get", strings.NewReader(block), &out, &diag)
	if err == nil {
		t.Error("Serve(get) over a 1 MiB attribute block = nil error, want a refusal")
	}
	if out.Len() != 0 {
		t.Errorf("Serve(get) over a 1 MiB attribute block wrote %d bytes to git, want none", out.Len())
	}
}

func TestErase_of_an_origin_no_connection_owns_counts_nothing(t *testing.T) {
	store, _ := openStore(t)
	save(t, store, "conn", record(forgeapi.FamilyGitLab, forge))
	c := &tally{}

	serve(t, helper(store, c), "erase", attributes("https", "elsewhere.example", "username=oauth2", "password=other"))
	if e := c.erased(); len(e) != 0 {
		t.Errorf("HelperErase reported %v for an origin no connection owns, want nothing", e)
	}
}

// Git's block is read up to 64 KiB, newer versions sending the authentication
// headers the remote answered.
func TestServe_reads_an_attribute_block_up_to_its_bound(t *testing.T) {
	const bound = 64 << 10
	head := "protocol=https\nhost=forge.example\nwwwauth[]="
	for name, test := range map[string]struct {
		size    int
		refused bool
	}{
		"at_the_bound":     {size: bound},
		"one_byte_past_it": {size: bound + 1, refused: true},
	} {
		t.Run(name, func(t *testing.T) {
			store, _ := openStore(t)
			save(t, store, "conn", record(forgeapi.FamilyGitHub, forge))
			block := head + strings.Repeat("x", test.size-len(head)-2) + "\n\n"

			got := serve(t, helper(store, &tally{}), "get", block)
			if refused := got.err != nil; refused != test.refused {
				t.Errorf("Serve(get) over a %d-byte block = error %v, want refused %v", len(block), got.err, test.refused)
			}
			if answered := got.attrs["password"] == "token-current"; answered == test.refused {
				t.Errorf("Serve(get) over a %d-byte block answered %v, want answered %v", len(block), got.attrs, !test.refused)
			}
		})
	}
}

// The helper reports to the logger it was built with, never to the process default.
func TestGet_logs_a_decline_to_the_logger_it_was_given(t *testing.T) {
	store, _ := openStore(t)
	rec := record(forgeapi.FamilyGitLab, forge)
	rec.Kind = forgeapi.CredKindUnknown
	save(t, store, "conn", rec)
	var logged bytes.Buffer
	h := gitcred.New(store, forgeapi.WithLogger(slog.New(slog.NewTextHandler(&logged, nil))))

	serve(t, h, "get", attributes("https", "forge.example"))
	line := logged.String()
	if !strings.Contains(line, `msg="forgeapi git credential declined"`) || !strings.Contains(line, " reason=reconnect_required") {
		t.Errorf("the helper's logger recorded %q, want the decline with its reason", line)
	}
}
