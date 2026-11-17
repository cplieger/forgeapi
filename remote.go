package forgeapi

import (
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxRemoteBytes bounds a remote before it is parsed. git's own URL buffers are
// unbounded, so the bound is this library's, chosen far above any forge's path limit.
const maxRemoteBytes = 2048

// Remote is the repository a git remote names, in one canonical spelling.
type Remote struct {
	// Host is lowercased, with no user and no port; an IPv6 literal keeps its brackets.
	Host string
	// Path is the repository path with its case kept, no ".git" and no surrounding "/".
	Path string
	// WebOrigin is "https://host[:port]" or "http://host[:port]" for an http or https
	// remote, lowercased with its port kept, and "" for an ssh or scp remote.
	WebOrigin string
}

// ParseRemote reads a git remote, in its URL or scp form, as the repository it names:
// every spelling of one repository answers one [Remote.Key]. It answers false for a
// remote with no host (a local path), one past 2,048 bytes, git's
// <transport>::<address> helper form, and one it cannot name canonically: invalid
// UTF-8, an ASCII control, "?", "#", "%" or "\" in the host or path, an empty, "." or
// ".." segment, a host that does not read back as a URL host, or a key that would not
// read back as itself through its https form.
func ParseRemote(raw string) (Remote, bool) {
	if len(raw) > maxRemoteBytes {
		return Remote{}, false
	}
	s := strings.TrimSpace(raw)
	if remoteHelper(s) {
		return Remote{}, false
	}
	parts, ok := splitRemote(s)
	if !ok {
		return Remote{}, false
	}
	host, p := parts.host, trimRepoPath(parts.path)
	if !readable(host) || !readable(p) || !cleanPath(p) {
		return Remote{}, false
	}
	host = strings.ToLower(host)
	key := host + "/" + p
	// A read-back trims whitespace from the ends of the key and strips a ".git".
	if !urlHost(host) || strings.TrimSpace(key) != key || strings.HasSuffix(p, ".git") {
		return Remote{}, false
	}
	r := Remote{Host: host, Path: p}
	if parts.scheme == "https" || parts.scheme == "http" {
		r.WebOrigin = parts.scheme + "://" + host
		if parts.port != "" {
			r.WebOrigin += ":" + parts.port
		}
	}
	return r, true
}

// Key is the repository's canonical name, Host and Path joined by "/".
func (r Remote) Key() string { return r.Host + "/" + r.Path }

// Owner is Path up to its last "/" ("group/sub" for "group/sub/repo"), and "" for a
// one-segment path.
func (r Remote) Owner() string {
	i := strings.LastIndexByte(r.Path, '/')
	if i < 0 {
		return ""
	}
	return r.Path[:i]
}

// Name is Path's last segment, never "" for a Remote ParseRemote answered.
func (r Remote) Name() string { return r.Path[strings.LastIndexByte(r.Path, '/')+1:] }

type remoteParts struct {
	host, path, scheme, port string
}

func splitRemote(s string) (remoteParts, bool) {
	if !strings.Contains(s, "://") {
		host, p, ok := scpForm(s)
		return remoteParts{host: host, path: p}, ok
	}
	u, err := url.Parse(s)
	if err != nil {
		return remoteParts{}, false
	}
	host := u.Hostname()
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return remoteParts{host: host, path: u.Path, scheme: u.Scheme, port: u.Port()}, true
}

// remoteHelper reports git's <transport>::<address> form, which git hands to a
// git-remote-<transport> program instead of reading it as an address
// (https://git-scm.com/docs/gitremote-helpers): a run of URL scheme characters,
// possibly empty, then "::".
func remoteHelper(s string) bool {
	i := 0
	for i < len(s) && schemeByte(s[i], i == 0) {
		i++
	}
	return strings.HasPrefix(s[i:], "::")
}

func schemeByte(b byte, first bool) bool {
	alnum := 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z' || '0' <= b && b <= '9'
	return alnum || !first && (b == '+' || b == '-' || b == '.')
}

// scpForm splits git's [user@]host:path, which git recognizes only when no slash
// comes before the first colon; a one-letter host is a Windows drive. A host in
// brackets ends at its "]", so an IPv6 literal's colons stay in it.
func scpForm(s string) (host, p string, ok bool) {
	c := strings.IndexByte(s, ':')
	if c < 0 || strings.ContainsRune(s[:c], '/') {
		return "", "", false
	}
	start := strings.LastIndexByte(s[:c], '@') + 1
	if s[start] == '[' {
		end := strings.IndexByte(s[start:], ']')
		if end < 0 {
			return "", "", false
		}
		c = start + end + 1
		if c == len(s) || s[c] != ':' {
			return "", "", false
		}
	}
	host = s[start:c]
	if len(host) < 2 {
		return "", "", false
	}
	return host, s[c+1:], true
}

// trimRepoPath strips slashes from both ends and one trailing ".git", the suffix git
// drops from a clone's name, so name.git.git stays name.git.
func trimRepoPath(p string) string {
	p = strings.TrimSuffix(strings.Trim(p, "/"), ".git")
	return strings.TrimRight(p, "/")
}

func readable(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsFunc(s, badRemoteRune)
}

func cleanPath(p string) bool {
	for seg := range strings.SplitSeq(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// urlHost reports whether host reads back unchanged as the host of a URL, so a host
// taken from either remote form passes by the URL form's rule. A URL host holds no
// whitespace, though url.Parse lets a non-ASCII space through.
func urlHost(host string) bool {
	u, err := url.Parse("https://" + host + "/")
	return host != "" && err == nil && u.Host == host && !strings.ContainsFunc(host, unicode.IsSpace)
}

// badRemoteRune refuses what a read-back or a canonical path would change: a read-back
// cuts the path at "?" or "#", decodes "%" into another path and fails on an ASCII
// control, and Windows reads "\" in a canonical path as a separator.
func badRemoteRune(r rune) bool {
	return r < 0x20 || r == 0x7f || strings.ContainsRune(`?#%\`, r)
}
