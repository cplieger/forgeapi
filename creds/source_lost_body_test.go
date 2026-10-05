package creds_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
)

// lostBodyEndpoint is a token endpoint whose every answer is cut off by the
// connection closing: with status above zero it sends that status, headers
// declaring the whole of body and the first half of body; with status zero it
// closes before any status line.
func lostBodyEndpoint(t *testing.T, status int, body string) *endpoint {
	t.Helper()
	ep := &endpoint{}
	ep.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			t.Errorf("Setup: reading the request body: %v", err)
		}
		form, err := url.ParseQuery(string(raw))
		if err != nil {
			t.Errorf("Setup: the request body %q is not a form: %v", raw, err)
		}
		ep.mu.Lock()
		ep.seen = append(ep.seen, exchange{header: r.Header.Clone(), form: form, query: r.URL.Query(), method: r.Method, path: r.URL.Path})
		ep.mu.Unlock()
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Errorf("Setup: the test server's writer cannot be hijacked")
			return
		}
		conn, out, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("Setup: hijacking the connection: %v", err)
			return
		}
		defer func() {
			if err := conn.Close(); err != nil {
				t.Errorf("Setup: closing the hijacked connection: %v", err)
			}
		}()
		if status == 0 {
			return
		}
		if _, err := fmt.Fprintf(out, "HTTP/1.1 %d %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s",
			status, http.StatusText(status), len(body), body[:len(body)/2]); err != nil {
			t.Errorf("Setup: writing the cut answer: %v", err)
		}
		if err := out.Flush(); err != nil {
			t.Errorf("Setup: flushing the cut answer: %v", err)
		}
	}))
	t.Cleanup(ep.srv.Close)
	return ep
}

// A success says the instance issued a pair, and both products spend the old pair
// the moment they issue a new one, so a success whose body did not arrive spent the
// stored pair: the record says so and nothing is handed over again.
func TestSource_writes_spent_for_a_success_whose_body_did_not_arrive(t *testing.T) {
	for _, arm := range refreshArms {
		t.Run(arm.family.String(), func(t *testing.T) {
			ep := lostBodyEndpoint(t, http.StatusOK, arm.rotated)
			store, dir := openStore(t)
			save(t, store, "conn", rotating(arm.family, ep.srv.URL, 8*time.Hour, time.Minute))
			src := source(t, store, "conn", ep)

			if token, err := src.Token(t.Context()); err == nil || token != "" {
				t.Errorf("Token() over a success whose body was cut off = (%q, %v), want no token and an error: the stored token was spent by the refresh", token, err)
			}
			checkMarkStored(t, dir, markSpent)
			checkDeclinedFor(t, src, ep, len(ep.requests()), markSpent)
		})
	}
}

// An instance that answered a failure status, or answered nothing, issued no pair,
// so whatever happened to the body the stored pair is live: nothing is marked, the
// still-valid token stays in use, and the next call refreshes again.
func TestSource_reads_a_refresh_that_issued_nothing_as_unspent_whatever_reached_its_body(t *testing.T) {
	answers := map[string]int{"failure_status_cut_off": http.StatusServiceUnavailable, "no_status": 0}
	for _, arm := range refreshArms {
		for name, status := range answers {
			t.Run(arm.family.String()+"_"+name, func(t *testing.T) {
				ep := lostBodyEndpoint(t, status, `{"error":"temporarily_unavailable","error_description":"The service is unavailable."}`)
				store, dir := openStore(t)
				save(t, store, "conn", rotating(arm.family, ep.srv.URL, 8*time.Hour, time.Minute))
				src := source(t, store, "conn", ep)

				if token, err := src.Token(t.Context()); err != nil || token != "token-old" {
					t.Errorf("Token() over a refresh that issued nothing = (%q, %v), want (%q, nil): the token has a minute left", token, err, "token-old")
				}
				checkMarkStored(t, dir, markUnmarked)
				sent := len(ep.requests())
				if _, err := src.Token(t.Context()); codeOf(err) == forgeapi.CodeReconnectRequired {
					t.Errorf("Token() after a refresh that issued nothing = code %q, want the connection still refreshable", forgeapi.CodeReconnectRequired)
				}
				if n := len(ep.requests()); n <= sent {
					t.Errorf("the token endpoint received %d request(s) after the second call, want more than %d: the pair is still due a refresh", n, sent)
				}
			})
		}
	}
}
