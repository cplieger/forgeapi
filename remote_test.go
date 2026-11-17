package forgeapi_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi/v3"
)

func TestParseRemote(t *testing.T) {
	tests := []struct {
		name, raw, want string
	}{
		{"scp form", "git@github.com:cplieger/toolbelt.git", "github.com/cplieger/toolbelt"},
		{"https", "https://github.com/cplieger/toolbelt.git", "github.com/cplieger/toolbelt"},
		{"no .git", "https://github.com/cplieger/toolbelt", "github.com/cplieger/toolbelt"},
		{"mixed-case host", "https://GitHub.COM/cplieger/toolbelt", "github.com/cplieger/toolbelt"},
		{"ssh with port and trailing slash", "ssh://git@github.com:22/cplieger/toolbelt.git/", "github.com/cplieger/toolbelt"},
		{"user and password", "https://u:secret@github.com/cplieger/toolbelt/", "github.com/cplieger/toolbelt"},
		{"surrounding space", " git@github.com:cplieger/toolbelt.git\n", "github.com/cplieger/toolbelt"},
		{"nested owner path", "https://gitlab.example.com/team/sub/docs.git", "gitlab.example.com/team/sub/docs"},
		{"owner case kept", "git@GitLab.example.com:Team/Docs.git", "gitlab.example.com/Team/Docs"},
		{"scp form without user", "example.org:repos/x.git", "example.org/repos/x"},
		{".git as the last segment", "git@github.com:cplieger/toolbelt/.git", "github.com/cplieger/toolbelt"},
		{"non-ASCII host", "git@bücher.example:team/docs.git", "bücher.example/team/docs"},
		{"space inside an scp name", "git@github.com:o/my repo", "github.com/o/my repo"},
		{"escaped space inside an https name", "https://github.com/o/my%20repo", "github.com/o/my repo"},
		{"Azure DevOps https names with spaces", "https://dev.azure.com/org/My%20Project/_git/My%20Repo", "dev.azure.com/org/My Project/_git/My Repo"},
		{"Azure DevOps scp names with spaces", "git@ssh.dev.azure.com:v3/org/My Project/My Repo", "ssh.dev.azure.com/v3/org/My Project/My Repo"},
		{"space segment", "git@github.com:o/ /r", "github.com/o/ /r"},
		{"no-break space segment", "git@github.com:o/\u00a0/r", "github.com/o/\u00a0/r"},
		{"query dropped", "https://github.com/a/b?x=1", "github.com/a/b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := forgeapi.ParseRemote(tt.raw)
			if !ok || got.Key() != tt.want {
				t.Errorf("ParseRemote(%q) = %+v, %v; want key %q", tt.raw, got, ok, tt.want)
			}
		})
	}
}

func TestParseRemoteRefusals(t *testing.T) {
	tests := []struct{ name, raw string }{
		{"empty", ""},
		{"absolute path", "/srv/git/toolbelt.git"},
		{"relative path", "../toolbelt"},
		{"file URL", "file:///srv/git/toolbelt.git"},
		{"Windows path", `C:\repos\toolbelt`},
		{"Windows path with slashes", "C:/repos/toolbelt"},
		{"host only", "https://github.com/"},
		{"host and .git only", "https://github.com/.git"},
		{"dot-dot segment", "https://github.com/a/../b"},
		{"slash before the colon", "dir/with:colon"},
		{"non-UTF-8 scp host", "git@a\x86:o/r"},
		{"non-UTF-8 https host", "https://a\x86/o/r"},
		{"non-UTF-8 escaped https host", "https://a%86/o/r"},
		{"non-UTF-8 scp path", "git@github.com:o/r\x86"},
		{"non-UTF-8 escaped https path", "https://github.com/o/r%86"},
		{"space before .git", "git@github.com:o/r .git"},
		{"escaped trailing space", "https://github.com/o/r%20"},
		{"escaped trailing no-break space", "https://github.com/o/r%C2%A0/"},
		{"space path", "a1: /"},
		{"repeated .git", "https://github.com/cplieger/toolbelt.git.git"},
		{".git after a slash", "git@github.com:cplieger/toolbelt.git/.git"},
		{"control byte in the host", "https://git\x01hub.com/a/b"},
		{"escaped NUL in the path", "https://github.com/a/b%00c"},
		{"escaped escape sequence in the path", "https://github.com/a/%1b]0;pwned%07b"},
		{"escaped DEL in the path", "https://github.com/a/b%7f"},
		{"escaped backslash in the path", "https://github.com/a%5cb/c"},
		{"escaped question mark in the path", "https://github.com/a/b%3Fc"},
		{"backslash in an scp path", "git@github.com:a/b\\c.git"},
		{"NUL in an scp path", "git@github.com:a/b\x00c.git"},
		{"IPv6 zone", "https://[fe80::1%25eth0]/o/r"},
		{"unclosed IPv6 bracket", "git@[::1:o/r"},
		{"IPv6 bracket not followed by its colon", "git@[::1]x:o/r"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, ok := forgeapi.ParseRemote(tt.raw); ok {
				t.Errorf("ParseRemote(%q) = %+v, true; want refused", tt.raw, got)
			}
		})
	}
}

func TestParseRemoteHostsAgreeAcrossForms(t *testing.T) {
	tests := []struct {
		host string
		ok   bool
	}{
		{"a]b", true},
		{"[ab]", false},
		{"a{b", false},
		{"a}b", false},
		{"a|b", false},
		{"a^b", false},
		{"a`b", false},
		{"a b", false},
		{"a\u00a0b", false},
		{"a\x86", false},
		{"a\u0085b", false},
		{"[::1]", true},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q", tt.host), func(t *testing.T) {
			want := ""
			if tt.ok {
				want = tt.host + "/o/r"
			}
			for _, raw := range []string{"git@" + tt.host + ":o/r", "https://" + tt.host + "/o/r"} {
				got, ok := forgeapi.ParseRemote(raw)
				if ok != tt.ok || (ok && got.Key() != want) {
					t.Errorf("ParseRemote(%q) = %+v, %v; want key %q, %v", raw, got, ok, want, tt.ok)
				}
			}
		})
	}
}

func TestParseRemoteSpellingsShareOneKey(t *testing.T) {
	const want = "github.com/cplieger/toolbelt"
	for _, raw := range []string{
		"git@github.com:cplieger/toolbelt.git",
		"https://github.com/cplieger/toolbelt.git",
		"https://github.com/cplieger/toolbelt",
		"https://GITHUB.com/cplieger/toolbelt.git",
		"ssh://git@github.com/cplieger/toolbelt",
		"ssh://git@github.com:2222/cplieger/toolbelt.git",
		"git://github.com/cplieger/toolbelt.git",
	} {
		if got, ok := forgeapi.ParseRemote(raw); !ok || got.Key() != want {
			t.Errorf("ParseRemote(%q) = %+v, %v; want key %q", raw, got, ok, want)
		}
	}
}

func TestParseRemoteOneSegment(t *testing.T) {
	tests := []struct{ name, raw string }{
		{"scp form", "git@nas:repo"},
		{"ssh URL", "ssh://host/repo.git"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, ok := forgeapi.ParseRemote(tt.raw)
			if !ok || r.Owner() != "" || r.Name() != "repo" || r.Path != "repo" {
				t.Errorf("ParseRemote(%q) = %+v, %v with owner %q and name %q; want path, name repo and no owner",
					tt.raw, r, ok, r.Owner(), r.Name())
			}
		})
	}
}

func TestParseRemoteOwnerAndNameJoinToPath(t *testing.T) {
	tests := []struct{ segments, raw, owner, name string }{
		{"one", "git@nas:repo", "", "repo"},
		{"two", "https://github.com/cplieger/forgeapi", "cplieger", "forgeapi"},
		{"three", "https://gitlab.com/group/sub/project.git", "group/sub", "project"},
	}
	for _, tt := range tests {
		t.Run(tt.segments, func(t *testing.T) {
			r, ok := forgeapi.ParseRemote(tt.raw)
			if !ok || r.Owner() != tt.owner || r.Name() != tt.name {
				t.Fatalf("ParseRemote(%q) = %+v, %v with owner %q and name %q; want owner %q and name %q",
					tt.raw, r, ok, r.Owner(), r.Name(), tt.owner, tt.name)
			}
			if joined := joinOwnerName(r); joined != r.Path {
				t.Errorf("owner and name join to %q, want the path %q", joined, r.Path)
			}
		})
	}
}

// TestParseRemoteMarotteCases holds the rules marotte's Git panel reads a remote
// by: the remote-helper form refused, the web origin with its port, and no web
// origin for an ssh or scp remote.
func TestParseRemoteMarotteCases(t *testing.T) {
	tests := []struct {
		name, raw     string
		ok            bool
		key, web, own string
	}{
		{name: "https", raw: "https://github.com/cplieger/marotte.git", ok: true, key: "github.com/cplieger/marotte", web: "https://github.com", own: "cplieger"},
		{name: "scheme and host lowercased", raw: "HTTPS://GitHub.COM/a/b", ok: true, key: "github.com/a/b", web: "https://github.com", own: "a"},
		{name: "port kept in the web origin", raw: "http://127.0.0.1:3000/alice/app.git", ok: true, key: "127.0.0.1/alice/app", web: "http://127.0.0.1:3000", own: "alice"},
		{name: "IPv6 web origin keeps its brackets and port", raw: "https://[FE80::1]:8443/o/r", ok: true, key: "[fe80::1]/o/r", web: "https://[fe80::1]:8443", own: "o"},
		{name: "userinfo dropped", raw: "https://bob:secret@gitlab.com/group/sub/project.git", ok: true, key: "gitlab.com/group/sub/project", web: "https://gitlab.com", own: "group/sub"},
		{name: "space trimmed", raw: "  https://git.example.test/team/thing.git\n", ok: true, key: "git.example.test/team/thing", web: "https://git.example.test", own: "team"},
		{name: "port is not part of the key", raw: "https://git.example.test:8443/team/thing.git", ok: true, key: "git.example.test/team/thing", web: "https://git.example.test:8443", own: "team"},
		{name: "scp form has no web origin", raw: "git@github.com:cplieger/marotte.git", ok: true, key: "github.com/cplieger/marotte", own: "cplieger"},
		{name: "ssh URL has no web origin", raw: "ssh://git@gitlab.com/group/project.git", ok: true, key: "gitlab.com/group/project", own: "group"},
		{name: "IPv6 ssh URL keeps its brackets", raw: "ssh://git@[::1]:22/x/y", ok: true, key: "[::1]/x/y", own: "x"},
		{name: "IPv6 scp form keeps its brackets", raw: "git@[::1]:x/y.git", ok: true, key: "[::1]/x/y", own: "x"},
		{name: "git protocol has no web origin", raw: "git://git.example.test/team/thing.git", ok: true, key: "git.example.test/team/thing", own: "team"},
		{name: "single segment is a repository with no owner", raw: "https://github.com/onlyowner", ok: true, key: "github.com/onlyowner", web: "https://github.com"},
		{name: "remote helper", raw: "ext::sh -c whoami"},
		{name: "remote helper before an scp form", raw: "ext::foo@host:pwn"},
		{name: "remote helper before a URL", raw: "fd::https://github.com/a/b"},
		{name: "remote helper with an empty transport", raw: "::github.com/a/b"},
		{name: "a transport name never starts with a plus", raw: "+h::o/r", ok: true, key: "+h/:o/r", own: ":o"},
		{name: "empty user", raw: "@host:path", ok: true, key: "host/path"},
		{name: "missing host", raw: "user@:path"},
		{name: "slash in the user", raw: "user@ho/st:path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, ok := forgeapi.ParseRemote(tt.raw)
			key := ""
			if ok {
				key = r.Key()
			}
			if ok != tt.ok || key != tt.key || r.WebOrigin != tt.web || r.Owner() != tt.own {
				t.Errorf("ParseRemote(%q) = %+v, %v with key %q and owner %q; want %v, key %q, web origin %q, owner %q",
					tt.raw, r, ok, key, r.Owner(), tt.ok, tt.key, tt.web, tt.own)
			}
		})
	}
}

func TestParseRemoteBound(t *testing.T) {
	const prefix = "https://github.com/o/"
	atBound := prefix + strings.Repeat("r", 2048-len(prefix))
	if _, ok := forgeapi.ParseRemote(atBound); !ok {
		t.Errorf("a remote of 2,048 bytes was refused")
	}
	if r, ok := forgeapi.ParseRemote(atBound + "r"); ok {
		t.Errorf("a remote of 2,049 bytes parsed as %q", r.Key())
	}
	if r, ok := forgeapi.ParseRemote(" " + atBound); ok {
		t.Errorf("a remote of 2,049 bytes with its leading space parsed as %q", r.Key())
	}
}

func joinOwnerName(r forgeapi.Remote) string {
	if r.Owner() == "" {
		return r.Name()
	}
	return r.Owner() + "/" + r.Name()
}
