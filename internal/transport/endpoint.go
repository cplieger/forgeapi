package transport

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/go/httpclient"
	"github.com/cplieger/go/masked"
)

// maxEndpointBytes bounds one OAuth endpoint answer, which is a handful of short
// fields on every product in scope.
const maxEndpointBytes = 64 << 10

// endpointCredential is what an OAuth endpoint request authenticates with, which
// is nothing: those endpoints identify the application by the client id in the
// form, and a bearer token there would hand one credential to the request that
// exists to mint another.
type endpointCredential struct{}

func (endpointCredential) Token(context.Context) (string, error) { return "", nil }
func (endpointCredential) Kind() forgeapi.CredKind               { return forgeapi.CredKindUnknown }
func (endpointCredential) State() forgeapi.CredState             { return forgeapi.CredUnknown }

// OpenEndpoint builds the request core for one connection's OAuth endpoints, which
// live under its web base, so its API root plays no part. It carries the
// connection's trust material, proxy, address policy, port allowlist and headers
// exactly as [Open] does, and no credential. Its one exchange is [Conn.PostForm].
func OpenEndpoint(conn *forgeapi.Connection, set *forgeapi.Settings, family forgeapi.Family) (*Conn, error) {
	c := *conn
	c.APIBaseURL = c.WebBaseURL
	s := *set
	s.Credential = endpointCredential{}
	s.ReadCacheBytes = 0
	return Open(&c, &s, &Options{Family: family})
}

// PostForm sends one form-encoded POST to path under the web base and answers the
// response whatever its status: an OAuth endpoint answers its outcomes in the body,
// at 200 on one product and 400 on another. An unreadable body answers the status
// and headers beside the failure, since a token may have been issued; every other
// failure answers no response. It takes one attempt, since a lost answer spent the
// code or refresh token it carried, and meets neither the mutation gate nor the
// interval, which govern forge objects. Every text it answers is quoted without
// secrets, the form's values that are credentials; its other values are kept.
func (c *Conn) PostForm(ctx context.Context, op, path string, form url.Values, secrets ...masked.Secret) (*Response, error) {
	target := strings.TrimSuffix(c.web.String(), "/") + path
	req := &Request{Op: op, Method: http.MethodPost, AbsoluteURL: target}
	ctx, cancel := c.withDeadline(ctx)
	defer cancel()
	ctx = withRecord(ctx, op)
	started := time.Now()
	q := c.quotes.with(secrets...)
	if err := c.acquire(ctx, req); err != nil {
		return nil, c.transportError(ctx, req, err, started, q)
	}
	defer c.release(req)

	payload := []byte(form.Encode())
	r, err := http.NewRequestWithContext(withQuoter(ctx, q), http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return nil, c.transportError(ctx, req, err, started, q)
	}
	r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(payload)), nil }
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Accept", "application/json")
	r.Header.Set("User-Agent", userAgent)
	for _, h := range c.headers {
		r.Header.Set(h.Name, h.Value.Reveal())
	}
	//nolint:bodyclose // httpclient.ReadLimitedBody below always closes the body, which the analyzer cannot follow
	resp, err := c.mutate.Do(r)
	if err != nil {
		return nil, c.transportError(ctx, req, err, started, q)
	}
	body, err := httpclient.ReadLimitedBody(resp.Body, maxEndpointBytes)
	if err != nil {
		lost := &Response{Header: resp.Header, Status: resp.StatusCode, quote: q}
		return lost, c.bodyError(ctx, req, lost, err, started)
	}
	return &Response{
		Header: resp.Header, Body: body, Status: resp.StatusCode, quote: q,
		Redirected: followed(resp, target), Ended: ended(resp, target),
	}, nil
}

// RetryAfter is the wait a response's Retry-After header asks for, uncapped,
// and zero where the header is absent or malformed.
func RetryAfter(header http.Header) time.Duration {
	return httpclient.ParseRetryAfterResponse(&http.Response{Header: header})
}
