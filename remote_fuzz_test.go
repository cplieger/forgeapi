package forgeapi_test

import (
	"testing"

	"github.com/cplieger/forgeapi/v3"
)

// FuzzParseRemote holds the canonical form: a key read back through its https
// form gives itself, with that form's web origin, and owner and name join back
// to the path.
func FuzzParseRemote(f *testing.F) {
	for _, s := range []string{
		"git@github.com:cplieger/toolbelt.git",
		"https://u:p@GitHub.com:443/a/b/c.git/",
		"ssh://[::1]:22/x/y",
		"git@[::1]:x/y",
		"host:~u/r",
		"file:///x",
		"a{b:o/r",
		"h1:o/r.git.git",
		"https://dev.azure.com/org/My%20Project/_git/My%20Repo",
		"git@nas:repo",
		"ext::sh -c whoami",
		"http://127.0.0.1:3000/alice/app.git",
		"https://github.com/a/%1b]0;pwned%07b",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		r, ok := forgeapi.ParseRemote(raw)
		if !ok {
			return
		}
		if r.Name() == "" || joinOwnerName(r) != r.Path {
			t.Errorf("ParseRemote(%q) = %+v: owner %q and name %q do not join to its path", raw, r, r.Owner(), r.Name())
		}
		again, ok := forgeapi.ParseRemote("https://" + r.Key())
		if !ok || again.Key() != r.Key() || again.WebOrigin != "https://"+r.Host {
			t.Errorf("ParseRemote(%q) has key %q, but its https form gives %+v, %v", raw, r.Key(), again, ok)
		}
	})
}
