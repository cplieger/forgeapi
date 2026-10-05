package gitcred_test

import (
	"strings"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
)

// An expired token is "expired" only while the server can still renew it. A static
// token, a rotating one with no refresh token, one whose refresh token has lapsed,
// and a family with no token endpoint have nothing to renew them, so the remedy the
// user reads is to connect again.
func TestGet_declines_an_expired_token_nothing_can_renew_as_reconnect_required(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	tests := []struct {
		edit func(*creds.Record)
		name string
	}{
		{name: "static", edit: func(r *creds.Record) {
			r.Kind, r.RefreshToken, r.RefreshExpiry = forgeapi.CredKindStaticPAT, "", time.Time{}
		}},
		{name: "rotating_without_a_refresh_token", edit: func(r *creds.Record) {
			r.RefreshToken, r.RefreshExpiry = "", time.Time{}
		}},
		{name: "refresh_token_lapsed", edit: func(r *creds.Record) { r.RefreshExpiry = past }},
		{name: "family_without_a_token_endpoint", edit: func(r *creds.Record) { r.Family = forgeapi.FamilyGitea }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store, _ := openStore(t)
			rec := record(forgeapi.FamilyGitLab, forge)
			rec.Issued, rec.Expiry = past.Add(-time.Hour), past
			tc.edit(&rec)
			save(t, store, "conn", rec)
			c := &tally{}

			got := serve(t, helper(store, c), "get", attributes("https", "forge.example"))
			checkDeclined(t, got, c, "reconnect_required")
			checkOneLine(t, got.diag)
			if !strings.Contains(got.diag, "forge.example") {
				t.Errorf("the decline line %q names no instance, want the one to connect again", got.diag)
			}
		})
	}
}

// The username is the family's, so a record naming no family this build knows,
// which is how a file naming an unknown family reads, has no credential git can use.
func TestGet_declines_a_record_of_no_known_family(t *testing.T) {
	store, _ := openStore(t)
	save(t, store, "conn", record(forgeapi.FamilyUnknown, forge))
	c := &tally{}

	got := serve(t, helper(store, c), "get", attributes("https", "forge.example"))
	checkDeclined(t, got, c, "reconnect_required")
}

// A port both URLs name explicitly still belongs to one scheme, and a token for
// an https origin must not be sent in the clear.
func TestGet_declines_another_scheme_on_the_same_explicit_port(t *testing.T) {
	store, _ := openStore(t)
	save(t, store, "conn", record(forgeapi.FamilyGitLab, "https://forge.example:8443"))
	c := &tally{}

	got := serve(t, helper(store, c), "get", attributes("http", "forge.example:8443"))
	checkDeclined(t, got, c, "unowned_origin")
}

// git's protocol carries a value on one line, so a token or an account name
// holding a newline would write attributes of its own, a url among them, which git
// reads as the credential's origin.
func TestGet_declines_a_value_the_protocol_cannot_carry(t *testing.T) {
	const injected = "\nurl=https://other.example"
	tests := []struct {
		edit   func(*creds.Record)
		name   string
		family forgeapi.Family
	}{
		{name: "token", family: forgeapi.FamilyGitHub, edit: func(r *creds.Record) { r.Token += injected }},
		{name: "account", family: forgeapi.FamilyGitea, edit: func(r *creds.Record) { r.Account += injected }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store, _ := openStore(t)
			rec := record(tc.family, forge)
			tc.edit(&rec)
			save(t, store, "conn", rec)
			c := &tally{}

			got := serve(t, helper(store, c), "get", attributes("https", "forge.example"))
			if u, ok := got.attrs["url"]; ok {
				t.Errorf("get wrote url=%q, want no attribute the record supplied", u)
			}
			checkDeclined(t, got, c, "reconnect_required")
		})
	}
}
