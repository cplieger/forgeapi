package transport

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/httpx/v5"
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
	return Open(&c, &s, Options{Family: family})
}

// PostForm sends one form-encoded POST to path under the web base and answers the
// response WHATEVER its status: an OAuth endpoint answers its outcomes in the body,
// at 200 on one product and at 400 on another, so the caller reads the body.
//
// A body that could not be read, the connection dropping mid-body or the body
// passing the endpoint's bound, answers the response's status and headers with no
// body beside the failure, because a token endpoint's success says the instance
// issued a token whether or not its body arrived. Every other failure answers no
// response.
//
// It takes one attempt, because a token request that reached the instance and lost
// its answer has already spent the code or the refresh token it carried. It meets
// neither the mutation gate nor the mutation interval, which govern forge objects
// rather than the connection's own credential.
func (c *Conn) PostForm(ctx context.Context, op, path string, form url.Values) (*Response, error) {
	target := strings.TrimSuffix(c.web.String(), "/") + path
	req := &Request{Op: op, Method: http.MethodPost, AbsoluteURL: target}
	ctx, cancel := c.withDeadline(ctx)
	defer cancel()
	ctx = withRecord(ctx, op)
	started := time.Now()
	if err := c.acquire(ctx, req); err != nil {
		return nil, c.transportError(ctx, req, err, started)
	}
	defer c.release(req)

	payload := []byte(form.Encode())
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return nil, c.transportError(ctx, req, err, started)
	}
	r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(payload)), nil }
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Accept", "application/json")
	r.Header.Set("User-Agent", userAgent)
	for _, h := range c.headers {
		r.Header.Set(h.Name, h.Value)
	}
	//nolint:bodyclose // the close is the defer below, which the analyzer cannot follow across the error branch
	resp, err := c.mutate.Do(r)
	if err != nil {
		return nil, c.transportError(ctx, req, err, started)
	}
	defer httpx.DrainClose(resp.Body)
	body, err := httpx.ReadLimitedBody(resp.Body, maxEndpointBytes)
	if err != nil {
		lost := &Response{Header: resp.Header, Status: resp.StatusCode, quote: c.quoter("")}
		return lost, c.transportError(ctx, req, err, started)
	}
	return &Response{
		Header: resp.Header, Body: body, Status: resp.StatusCode, quote: c.quoter(""),
		Redirected: followed(resp, target), Ended: ended(resp, target),
	}, nil
}

// Sanitize bounds and rune-sanitizes untrusted upstream text for a package that
// carries it on an error or a log line without importing the sanitizer itself.
func Sanitize(s string) string { return sanitize(s) }
