package creds_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
)

// cancelOnAnswer is a wire transport that ends a context the moment the token
// endpoint's status and headers arrive, so the poll's context ends while the
// success's body is still on the way.
type cancelOnAnswer struct {
	next      http.RoundTripper
	cancel    context.CancelFunc
	tokenPath string
}

func (c cancelOnAnswer) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := c.next.RoundTrip(r)
	if err == nil && r.URL.Path == c.tokenPath {
		c.cancel()
	}
	return resp, err
}

// A poll whose context ends while a success is arriving answers the context's own
// sentinel, as every call its context ends does, and the device code that success
// spent ends the grant: the next poll answers the grant's end and sends nothing.
func TestDeviceGrant_ends_on_a_success_its_context_cut_off(t *testing.T) {
	for _, arm := range deviceArms {
		t.Run(arm.family.String(), func(t *testing.T) {
			polls := 0
			ep := &endpoint{}
			ep.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ep.mu.Lock()
				ep.seen = append(ep.seen, exchange{method: r.Method, path: r.URL.Path})
				if strings.HasSuffix(r.URL.Path, arm.tokenPath) {
					polls++
				}
				poll := polls
				ep.mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if poll == 0 {
					if _, err := io.WriteString(w, deviceCode("900")); err != nil {
						t.Errorf("Setup: writing the device code: %v", err)
					}
					return
				}
				if poll > 1 {
					w.WriteHeader(arm.errorStatus)
					if _, err := io.WriteString(w, `{"error":"authorization_pending"}`); err != nil {
						t.Errorf("Setup: writing the pending answer: %v", err)
					}
					return
				}
				w.Header().Set("Content-Length", strconv.Itoa(len(grantedPair)))
				w.WriteHeader(http.StatusOK)
				if _, err := io.WriteString(w, grantedPair[:len(grantedPair)/2]); err != nil {
					t.Errorf("Setup: writing half the success: %v", err)
				}
				if err := http.NewResponseController(w).Flush(); err != nil {
					t.Errorf("Setup: flushing half the success: %v", err)
				}
				<-r.Context().Done()
			}))
			t.Cleanup(ep.srv.Close)

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			wire := cancelOnAnswer{next: ep.srv.Client().Transport, cancel: cancel, tokenPath: arm.tokenPath}
			g, err := creds.StartDeviceGrant(t.Context(), ep.conn(), arm.family,
				creds.GrantRequest{ClientID: clientID, Scopes: scopes},
				forgeapi.WithWireTransport(wire), forgeapi.WithPlaintextHTTP(true), forgeapi.WithPrivateAddresses(true),
				forgeapi.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
			if err != nil {
				t.Fatalf("StartDeviceGrant(%v) = error %v, want a grant", arm.family, err)
			}

			rec, done, err := g.Poll(ctx)
			_, folded := errors.AsType[*forgeapi.Error](err)
			if done || rec.Token != "" || !errors.Is(err, context.Canceled) || folded {
				t.Errorf("Poll() cancelled inside a success = (token %q, %v, %v), want no record and context.Canceled unchanged", rec.Token, done, err)
			}
			sent := len(ep.requests())
			_, _, again := g.Poll(t.Context())
			if fe, ok := errors.AsType[*forgeapi.Error](again); !ok || fe.Code != forgeapi.CodeValidation || fe.Status != http.StatusOK || fe.Family != arm.family {
				t.Errorf("a second Poll() = %v, want code %q at status 200 on %v: the cut success spent the device code", again, forgeapi.CodeValidation, arm.family)
			}
			if n := len(ep.requests()); n != sent {
				t.Errorf("a second Poll() sent %d request(s), want none: a spent device code is not sent again", n-sent)
			}
		})
	}
}
