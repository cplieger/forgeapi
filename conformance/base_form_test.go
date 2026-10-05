package conformance

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
	"github.com/cplieger/forgeapi/families"
	"github.com/cplieger/forgeapi/internal/spec"
)

// A base URL is an origin and a path: the scheme and host every request reaches,
// and the relative root an instance may serve under. Userinfo is no part of the
// host (RFC 3986 section 3.2.1), so a base carrying one names two hosts, the one a
// reader takes for the forge before the "@" and the one a request reaches after
// it; a query or a fragment is no part of a root a request path is joined to.

// trap is a TLS instance standing for the host after the "@", counting every
// request that reaches it.
type trap struct {
	srv  *httptest.Server
	hits atomic.Int64
}

func newTrap(t *testing.T) *trap {
	t.Helper()
	tr := &trap{}
	tr.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		tr.hits.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(tr.srv.Close)
	return tr
}

// host is the trap's host and port, the part of a base after its "@".
func (tr *trap) host() string { return strings.TrimPrefix(tr.srv.URL, "https://") }

// malformedBases are the base forms refused at entry, each built around the trap so
// a base that slipped through would send its request there.
func malformedBases(tr *trap) []struct{ name, base string } {
	h := tr.host()
	return []struct{ name, base string }{
		{name: "userinfo_naming_another_host", base: "https://github.com:443@" + h},
		{name: "userinfo", base: "https://user@" + h},
		{name: "userinfo_with_a_password", base: "https://user:secret@" + h},
		{name: "empty_userinfo", base: "https://@" + h},
		{name: "query", base: "https://" + h + "/?x=1"},
		{name: "empty_query", base: "https://" + h + "/?"},
		{name: "fragment", base: "https://" + h + "/#f"},
		{name: "empty_fragment", base: "https://" + h + "/#"},
	}
}

// checkBaseRefused holds one entry to the malformed-base refusal and the trap to
// silence.
func checkBaseRefused(t *testing.T, tr *trap, what string, err error) {
	t.Helper()
	fe, ok := errors.AsType[*forgeapi.Error](err)
	if !ok || fe.Code != forgeapi.CodeConnectionInvalid {
		t.Errorf("%s = error %v, want a *forgeapi.Error with code %q", what, err, forgeapi.CodeConnectionInvalid)
	}
	if n := tr.hits.Load(); n != 0 {
		t.Errorf("%s sent %d request(s) to the host after the base's userinfo or before its query, want none", what, n)
	}
}

func TestEveryConstructorRefusesABaseThatIsNotAnOriginAndAPath(t *testing.T) {
	for _, p := range spec.Products {
		for _, field := range []string{"WebBaseURL", "APIBaseURL"} {
			t.Run(string(p)+"_"+field, func(t *testing.T) {
				tr := newTrap(t)
				for _, form := range malformedBases(tr) {
					conn := forgeapi.Connection{WebBaseURL: form.base, APIBaseURL: tr.srv.URL + apiRoot(p)}
					if field == "APIBaseURL" {
						conn = forgeapi.Connection{WebBaseURL: tr.srv.URL, APIBaseURL: form.base}
					}
					client, err := guard(func() (any, error) { return newClient(p, conn, offlineOptions(tr.srv)...) })
					if caps, built := client.(forgeapi.Capabilities); err == nil && built {
						// The client built: one read shows where its requests go.
						_, _ = guard(func() (any, error) { return caps.ConnectionCaps(t.Context()) })
					}
					checkBaseRefused(t, tr, "New on "+string(p)+" with "+field+" "+form.name, err)
				}
			})
		}
	}
}

// The form is refused before the postures: a plaintext base carrying userinfo, on a
// connection that did not opt in to plaintext, answers connection_invalid and not
// plaintext_refused.
func TestEveryConstructorRefusesAUserinfoBaseBeforeThePlaintextPosture(t *testing.T) {
	for _, p := range spec.Products {
		t.Run(string(p), func(t *testing.T) {
			tr := newTrap(t)
			opts := append(offlineOptions(tr.srv), forgeapi.WithPlaintextHTTP(false))
			conn := forgeapi.Connection{WebBaseURL: "http://user@" + tr.host(), APIBaseURL: tr.srv.URL + apiRoot(p)}
			_, err := guard(func() (any, error) { return newClient(p, conn, opts...) })
			checkBaseRefused(t, tr, "New on "+string(p)+" with web base http://user@ and no plaintext opt-in", err)
		})
	}
}

// A path is a relative root, which a self-hosted instance may serve under.
func TestEveryConstructorAcceptsABaseCarryingAPath(t *testing.T) {
	for _, p := range spec.Products {
		t.Run(string(p), func(t *testing.T) {
			tr := newTrap(t)
			conn := forgeapi.Connection{WebBaseURL: tr.srv.URL + "/gitlab", APIBaseURL: tr.srv.URL + "/gitlab" + apiRoot(p)}
			if _, err := guard(func() (any, error) { return newClient(p, conn, offlineOptions(tr.srv)...) }); err != nil {
				t.Errorf("New on %s with a base under /gitlab = error %v, want a client", p, err)
			}
		})
	}
}

func TestFamilyDetectionRefusesABaseThatIsNotAnOriginAndAPath(t *testing.T) {
	for _, field := range []string{"WebBaseURL", "APIBaseURL"} {
		t.Run(field, func(t *testing.T) {
			tr := newTrap(t)
			for _, form := range malformedBases(tr) {
				conn := forgeapi.Connection{WebBaseURL: form.base}
				if field == "APIBaseURL" {
					conn = forgeapi.Connection{WebBaseURL: tr.srv.URL, APIBaseURL: form.base}
				}
				_, _, err := families.Open(t.Context(), conn, offlineOptions(tr.srv)...)
				checkBaseRefused(t, tr, "families.Open with "+field+" "+form.name, err)
			}
		})
	}
}

// A device grant's endpoints live under the web base, so the grant's start is the
// same entry.
func TestADeviceGrantRefusesAWebBaseThatIsNotAnOriginAndAPath(t *testing.T) {
	for _, family := range []forgeapi.Family{forgeapi.FamilyGitHub, forgeapi.FamilyGitLab} {
		t.Run(family.String(), func(t *testing.T) {
			tr := newTrap(t)
			for _, form := range malformedBases(tr) {
				conn := forgeapi.Connection{WebBaseURL: form.base}
				_, err := creds.StartDeviceGrant(t.Context(), conn, family, creds.GrantRequest{ClientID: "example-client-id"}, offlineOptions(tr.srv)...)
				checkBaseRefused(t, tr, "StartDeviceGrant on "+family.String()+" with "+form.name, err)
			}
		})
	}
}
