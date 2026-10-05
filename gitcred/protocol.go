package gitcred

import (
	"bufio"
	"errors"
	"io"
	"net/url"
	"strings"

	"github.com/cplieger/forgeapi/internal/transport"
)

// maxBlock bounds the attribute block a helper reads. Git writes a few short
// lines and, on newer versions, the authentication headers the remote answered.
const maxBlock = 64 << 10

var (
	errBlockTooLarge = errors.New("gitcred: the attribute block git wrote exceeds the helper's bound")
	errBlockUnread   = errors.New("gitcred: the attribute block git wrote could not be read")
)

// attributes is git's attribute block, each key holding the last value written
// for it, which is how git reads a repeated key.
type attributes map[string]string

// readAttributes reads one block: key=value lines ending at an empty line or at
// the end of the input. A line with no separator carries nothing and is skipped.
func readAttributes(in io.Reader) (attributes, error) {
	r := bufio.NewReader(io.LimitReader(in, maxBlock+1))
	attrs := attributes{}
	read := 0
	for {
		line, err := r.ReadString('\n')
		read += len(line)
		if read > maxBlock {
			return nil, errBlockTooLarge
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, errBlockUnread
		}
		line = strings.TrimSuffix(line, "\n")
		if line == "" {
			return attrs, nil
		}
		if key, value, ok := strings.Cut(line, "="); ok {
			attrs[key] = value
		}
		if err != nil {
			return attrs, nil
		}
	}
}

// origin is what a remote and a web base URL are matched on.
type origin struct {
	scheme string
	host   string
	port   string
}

// defaultPorts are the effective ports of a URL that names none.
var defaultPorts = map[string]string{"http": "80", "https": "443"}

func (o origin) matches(other origin) bool {
	return o.scheme == other.scheme && o.port == other.port && strings.EqualFold(o.host, other.host)
}

// remoteOrigin is the origin of git's protocol and host attributes, the host
// carrying a port where the remote URL named one.
func remoteOrigin(protocol, host string) (origin, bool) {
	if protocol == "" || host == "" {
		return origin{}, false
	}
	u := url.URL{Host: host}
	return originOf(protocol, u.Hostname(), u.Port()), true
}

// baseOrigin is the origin of a record's web base URL. A base of any form but an
// origin and a path owns no origin, the form a connection is refused without.
func baseOrigin(base string) (origin, bool) {
	u, ok := transport.ParseOriginAndPath(base)
	if !ok || u.Host == "" {
		return origin{}, false
	}
	return originOf(u.Scheme, u.Hostname(), u.Port()), true
}

func originOf(scheme, host, port string) origin {
	scheme = strings.ToLower(scheme)
	if port == "" {
		port = defaultPorts[scheme]
	}
	return origin{scheme: scheme, host: host, port: port}
}
