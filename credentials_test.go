package forgeapi_test

import (
	"testing"

	"github.com/cplieger/forgeapi/v2"
)

// A kind's spelling is what a log line carries and what a stored record's file
// names it by, so each member keeps the spelling its godoc publishes.
func TestCredKind_String_spells_each_member(t *testing.T) {
	for kind, want := range map[forgeapi.CredKind]string{
		forgeapi.CredKindUnknown:         "unknown",
		forgeapi.CredKindStaticPAT:       "static_pat",
		forgeapi.CredKindRotatingOAuth:   "rotating_oauth",
		forgeapi.CredKindAppInstallation: "app_installation",
	} {
		if got := kind.String(); got != want {
			t.Errorf("CredKind(%d).String() = %q, want %q", int(kind), got, want)
		}
	}
}
