package transport

import (
	"crypto/sha256"
	"encoding/base64"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cplieger/forgeapi"
)

// pageSeparator divides the fields of a continuation. No decimal and no character
// of the base64url alphabet is it.
const pageSeparator = "."

// callDigestBytes is how much of a call's hash its continuations carry: enough that
// two calls one caller makes never share it, and short enough that a continuation
// stays far inside the cursor's byte cap whatever the repository's, the owner's or
// the connection's length.
const callDigestBytes = 12

// PageCall is one list call as every continuation it mints names it: the family, the
// connection, the operation, the repository, the resolved state filter, the scope and
// any since bound, folded into a fixed-length digest, and the page bound. A continuation that
// names another call names no position in this one, so both resume methods refuse it
// before any request.
type PageCall struct {
	digest string
	bound  int
}

// PageCall names the call one list operation is making on this connection. op is the
// operation as its role method spells it; repo is the repository the call addresses,
// the zero reference on a list no repository addresses. The repository and an owner
// fold as the derived repository identifier folds them, and the API base folds as
// [connectionOf] states, so two spellings of one call name one call.
func (c *Conn) PageCall(op string, repo forgeapi.RepoRef, set *forgeapi.ListSettings) PageCall {
	return newPageCall(c.family, connectionOf(c.apiBase), op, repo, set)
}

// connectionOf is the API base a continuation names: the scheme and the host
// lowercased, the scheme's default port dropped and a trailing slash trimmed. The path
// stays, since two instances one host serves under two relative roots are two
// connections.
func connectionOf(base *url.URL) string {
	scheme := strings.ToLower(base.Scheme)
	host := strings.ToLower(base.Hostname())
	if port := base.Port(); port != "" && port != defaultPort(scheme) {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return scheme + "://" + host + strings.TrimSuffix(base.EscapedPath(), "/")
}

// newPageCall hashes the call's fields, each length-prefixed so no two lists of
// fields share an encoding.
func newPageCall(family forgeapi.Family, connection, op string, repo forgeapi.RepoRef, set *forgeapi.ListSettings) PageCall {
	scope := "author"
	if set.OwnerSet {
		// The owner's shared form is ASCII, so this is the derived identifier's fold.
		scope = "owner:" + strings.ToLower(set.Owner)
	}
	fields := []string{family.String(), connection, op, repo.Encode(), set.State.String(), scope}
	if set.SinceSet {
		// Appended only under a bound, so every continuation minted without one
		// keeps the digest it had before the bound existed.
		fields = append(fields, "since:"+set.Since.UTC().Format(time.RFC3339Nano))
	}
	var named []byte
	for _, field := range fields {
		named = strconv.AppendInt(named, int64(len(field)), 10)
		named = append(named, ':')
		named = append(named, field...)
	}
	sum := sha256.Sum256(named)
	return PageCall{
		digest: base64.RawURLEncoding.EncodeToString(sum[:callDigestBytes]),
		bound:  set.PageBound,
	}
}

// Mint is the page-numbered continuation resuming this call at position: prefix,
// then the position's numbers in order, the page bound and the call's digest,
// separated.
func (p PageCall) Mint(prefix string, position ...int) forgeapi.Cursor {
	wide := make([]int64, len(position))
	for i, n := range position {
		wide[i] = int64(n)
	}
	return p.MintWide(prefix, wide...)
}

// MintWide is [PageCall.Mint] over 64-bit numbers, for a position naming a value an
// int may not hold, such as a run id.
func (p PageCall) MintWide(prefix string, position ...int64) forgeapi.Cursor {
	c := []byte(prefix)
	for _, n := range position {
		c = strconv.AppendInt(c, n, 10)
		c = append(c, pageSeparator...)
	}
	c = strconv.AppendInt(c, int64(p.bound), 10)
	c = append(c, pageSeparator...)
	c = append(c, p.digest...)
	return forgeapi.Cursor(c)
}

// Resume reads the count numbers of the position a continuation this call minted
// under prefix names. It refuses, with [forgeapi.CodeCursorInvalid], another
// encoding, another call's continuation, and one minted at another page bound, whose
// message names the bound the walk began at, since that bound alone resumes it.
// Whether each number is one the family mints is the family's to judge.
func (p PageCall) Resume(c forgeapi.Cursor, prefix string, count int) ([]int, error) {
	wide, err := p.ResumeWide(c, prefix, count)
	if err != nil {
		return nil, err
	}
	position := make([]int, count)
	for i, n := range wide {
		if int64(int(n)) != n {
			return nil, Local(forgeapi.CodeCursorInvalid, "continuation is not one of this library's own")
		}
		position[i] = int(n)
	}
	return position, nil
}

// ResumeWide is [PageCall.Resume] over 64-bit numbers, reading what
// [PageCall.MintWide] minted.
func (p PageCall) ResumeWide(c forgeapi.Cursor, prefix string, count int) ([]int64, error) {
	fields, err := p.fields(c, prefix, count+1)
	if err != nil {
		return nil, err
	}
	bound, err := strconv.Atoi(fields[count])
	if err != nil || bound < 1 {
		return nil, Local(forgeapi.CodeCursorInvalid, "continuation names no page bound")
	}
	if bound != p.bound {
		return nil, Local(forgeapi.CodeCursorInvalid,
			"continuation was minted at a page bound of "+strconv.Itoa(bound)+
				", and a page number counts pages of one size, so it resumes at that bound alone; a walk at another bound starts again from the first page")
	}
	position := make([]int64, count)
	for i := range count {
		n, err := strconv.ParseInt(fields[i], 10, 64)
		if err != nil {
			return nil, Local(forgeapi.CodeCursorInvalid, "continuation is not one of this library's own")
		}
		position[i] = n
	}
	return position, nil
}

// MintPosition is the continuation resuming this call at an upstream connection's own
// position: prefix, then the position's fields in order and the call's digest,
// separated. Each field is the family's own encoding and carries no separator. It
// carries no page bound, since a connection position resumes at the row after the
// last one served whatever page size the next call asks.
func (p PageCall) MintPosition(prefix string, position ...string) forgeapi.Cursor {
	c := []byte(prefix)
	for _, field := range position {
		c = append(c, field...)
		c = append(c, pageSeparator...)
	}
	c = append(c, p.digest...)
	return forgeapi.Cursor(c)
}

// ResumePosition reads the count fields of the position a continuation this call
// minted with [PageCall.MintPosition] under prefix names, refusing another encoding
// and another call's continuation with [forgeapi.CodeCursorInvalid]. Whether each
// field is one the family mints is the family's to judge.
func (p PageCall) ResumePosition(c forgeapi.Cursor, prefix string, count int) ([]string, error) {
	return p.fields(c, prefix, count)
}

// fields splits a continuation minted under prefix into the count fields before its
// digest, refusing one of another shape or whose digest names another call.
func (p PageCall) fields(c forgeapi.Cursor, prefix string, count int) ([]string, error) {
	rest, ok := strings.CutPrefix(string(c), prefix)
	fields := strings.Split(rest, pageSeparator)
	if !ok || len(fields) != count+1 {
		return nil, Local(forgeapi.CodeCursorInvalid, "continuation is not one of this library's own")
	}
	if fields[count] != p.digest {
		return nil, Local(forgeapi.CodeCursorInvalid,
			"continuation was minted by another list call, of another family, connection, operation, repository, state filter, scope or since bound, so it names no position in this one")
	}
	return fields[:count], nil
}
