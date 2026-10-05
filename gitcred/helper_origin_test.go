package gitcred_test

import (
	"testing"

	"github.com/cplieger/forgeapi"
)

// A record's web base is an origin and a path, the form a connection is refused
// without. Userinfo is no part of the host (RFC 3986 section 3.2.1), so a base
// carrying one names a host before the "@" a reader takes for the forge and a host
// after it a URL parser reaches; a record of that form, or one carrying a query or
// a fragment, owns no origin, and its token is answered to neither host.
func TestGet_answers_no_origin_for_a_record_whose_base_is_not_an_origin_and_a_path(t *testing.T) {
	tests := []struct {
		name  string
		base  string
		hosts []string
	}{
		{name: "userinfo_naming_another_host", base: "https://forge.example:443@evil.example", hosts: []string{"evil.example", "forge.example"}},
		{name: "userinfo", base: "https://user@forge.example", hosts: []string{"forge.example"}},
		{name: "empty_userinfo", base: "https://@forge.example", hosts: []string{"forge.example"}},
		{name: "query", base: "https://forge.example/?x=1", hosts: []string{"forge.example"}},
		{name: "empty_query", base: "https://forge.example/?", hosts: []string{"forge.example"}},
		{name: "fragment", base: "https://forge.example/#f", hosts: []string{"forge.example"}},
		{name: "empty_fragment", base: "https://forge.example/#", hosts: []string{"forge.example"}},
	}
	for _, tc := range tests {
		for _, host := range tc.hosts {
			t.Run(tc.name+"_asked_for_"+host, func(t *testing.T) {
				store, _ := openStore(t)
				save(t, store, "conn", record(forgeapi.FamilyGitHub, tc.base))
				c := &tally{}

				got := serve(t, helper(store, c), "get", attributes("https", host))
				checkDeclined(t, got, c, "unowned_origin")
			})
		}
	}
}

// A path is a relative root, which a self-hosted instance may serve under, so a
// base carrying one owns its scheme, host and port.
func TestGet_answers_the_origin_of_a_record_whose_base_carries_a_path(t *testing.T) {
	store, _ := openStore(t)
	save(t, store, "conn", record(forgeapi.FamilyGitLab, "https://forge.example/gitlab"))
	c := &tally{}

	got := serve(t, helper(store, c), "get", attributes("https", "forge.example"))
	if got.attrs["password"] != "token-current" {
		t.Errorf("get for https://forge.example against a base under /gitlab = %v, want the token", got.attrs)
	}
}
