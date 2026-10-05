package gitcred_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
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
