package creds_test

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
)

// pending is the token endpoint's answer while the user has not approved.
func pending(status int) answerBody {
	return answerBody{status: status, body: `{"error":"authorization_pending"}`}
}

// A consumer whose user cancels ends the grant itself: the next Poll answers the
// denial code at status 0, the consumer's own end and no instance's answer, and the
// token endpoint never hears of it.
func TestDeviceGrant_Close_ends_a_pending_grant_without_a_request(t *testing.T) {
	for _, arm := range deviceArms {
		t.Run(arm.family.String(), func(t *testing.T) {
			ep := newEndpoint(t, deviceFlow(deviceCode("900"), pending(arm.errorStatus)))
			g := startGrant(t, ep, arm.family)

			g.Close()
			rec, done, err := g.Poll(t.Context())
			fe, ok := errors.AsType[*forgeapi.Error](err)
			if !ok {
				t.Fatalf("Poll() after Close = %v, want a *forgeapi.Error", err)
			}
			if done || rec.Token != "" || fe.Code != forgeapi.CodeGrantDenied || fe.Kind != forgeapi.KindForbidden || fe.Status != 0 || fe.Family != arm.family {
				t.Errorf("Poll() after Close = (token %q, %v, %v / %q at %d on %v), want no record and %v / %q at status 0 on %v",
					rec.Token, done, fe.Kind, fe.Code, fe.Status, fe.Family, forgeapi.KindForbidden, forgeapi.CodeGrantDenied, arm.family)
			}
			if n := len(ep.requests()); n != 1 {
				t.Errorf("the endpoint received %d request(s), want 1, the device-code request alone", n)
			}
		})
	}
}

// A second Close changes nothing: the grant keeps the end the first one gave it and
// still sends nothing.
func TestDeviceGrant_Close_twice_keeps_the_first_end(t *testing.T) {
	for _, arm := range deviceArms {
		t.Run(arm.family.String(), func(t *testing.T) {
			ep := newEndpoint(t, deviceFlow(deviceCode("900"), pending(arm.errorStatus)))
			g := startGrant(t, ep, arm.family)

			g.Close()
			g.Close()
			_, _, err := g.Poll(t.Context())
			if code := codeOf(err); code != forgeapi.CodeGrantDenied {
				t.Errorf("Poll() after two Close calls = %v (code %q), want code %q", err, code, forgeapi.CodeGrantDenied)
			}
			if n := len(ep.requests()); n != 1 {
				t.Errorf("the endpoint received %d request(s), want 1", n)
			}
		})
	}
}

// A grant a poll completed stays granted: Close after it leaves the record, which the
// next Poll answers again without a request.
func TestDeviceGrant_Close_after_a_granted_poll_keeps_the_record(t *testing.T) {
	for _, arm := range deviceArms {
		t.Run(arm.family.String(), func(t *testing.T) {
			ep := newEndpoint(t, deviceFlow(deviceCode("900"), answerBody{status: http.StatusOK, body: grantedPair}))
			g := startGrant(t, ep, arm.family)
			if _, done, err := g.Poll(t.Context()); !done || err != nil {
				t.Fatalf("Setup: Poll() = (%v, %v), want the record", done, err)
			}
			sent := len(ep.requests())

			g.Close()
			rec, done, err := g.Poll(t.Context())
			if !done || err != nil || rec.Token != "token-granted" {
				t.Errorf("Poll() after a granted poll and Close = (token %q, %v, %v), want the granted record", rec.Token, done, err)
			}
			if n := len(ep.requests()); n != sent {
				t.Errorf("Poll() after Close sent %d request(s), want none", n-sent)
			}
		})
	}
}

// Close releases the grant's endpoint: the connection the device-code request left
// idle closes.
func TestDeviceGrant_Close_releases_its_endpoint(t *testing.T) {
	for _, arm := range deviceArms {
		t.Run(arm.family.String(), func(t *testing.T) {
			var mu sync.Mutex
			state := map[net.Conn]http.ConnState{}
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, err := io.Copy(io.Discard, io.LimitReader(r.Body, 1<<20)); err != nil {
					t.Errorf("Setup: reading the request body: %v", err)
				}
				w.Header().Set("Content-Type", "application/json")
				if _, err := io.WriteString(w, deviceCode("900")); err != nil {
					t.Errorf("Setup: writing the device code: %v", err)
				}
			}))
			srv.Config.ConnState = func(c net.Conn, s http.ConnState) {
				mu.Lock()
				defer mu.Unlock()
				state[c] = s
			}
			srv.Start()
			t.Cleanup(srv.Close)
			open := func() int {
				mu.Lock()
				defer mu.Unlock()
				n := 0
				for _, s := range state {
					if s != http.StateClosed && s != http.StateHijacked {
						n++
					}
				}
				return n
			}
			g, err := creds.StartDeviceGrant(t.Context(), forgeapi.Connection{WebBaseURL: srv.URL, APIBaseURL: srv.URL}, arm.family,
				creds.GrantRequest{ClientID: clientID, Scopes: scopes},
				forgeapi.WithPlaintextHTTP(true), forgeapi.WithPrivateAddresses(true),
				forgeapi.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
			if err != nil {
				t.Fatalf("StartDeviceGrant(%v) = error %v, want a grant", arm.family, err)
			}
			if n := open(); n != 1 {
				t.Fatalf("Setup: the device-code request left %d connections open, want its one idle connection", n)
			}

			g.Close()
			deadline := time.Now().Add(3 * time.Second)
			for open() != 0 && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if n := open(); n != 0 {
				t.Errorf("Close left %d connections open, want 0", n)
			}
		})
	}
}

// A cancel comes from another goroutine than the poll loop. Close during a poll in
// flight waits for it, and a grant that poll completed stays granted.
func TestDeviceGrant_Close_during_a_poll_returns_after_it(t *testing.T) {
	for _, arm := range deviceArms {
		t.Run(arm.family.String(), func(t *testing.T) {
			arrived := make(chan struct{})
			release := make(chan struct{})
			ep := newEndpoint(t, func(_ int, x exchange) (int, string) {
				if !strings.HasSuffix(x.path, arm.tokenPath) {
					return http.StatusOK, deviceCode("900")
				}
				close(arrived)
				<-release
				return http.StatusOK, grantedPair
			})
			g := startGrant(t, ep, arm.family)

			type polled struct {
				err  error
				rec  creds.Record
				done bool
			}
			poll := make(chan polled, 1)
			go func() {
				rec, done, err := g.Poll(t.Context())
				poll <- polled{rec: rec, done: done, err: err}
			}()
			<-arrived
			closed := make(chan struct{})
			go func() {
				g.Close()
				close(closed)
			}()
			select {
			case <-closed:
				t.Error("Close returned while a poll was in flight, want it to wait for the poll")
			case <-time.After(100 * time.Millisecond):
			}
			close(release)
			got := <-poll
			<-closed
			if !got.done || got.err != nil || got.rec.Token != "token-granted" {
				t.Errorf("the poll Close waited for = (token %q, %v, %v), want the granted record", got.rec.Token, got.done, got.err)
			}
			if rec, done, err := g.Poll(t.Context()); !done || err != nil || rec.Token != "token-granted" {
				t.Errorf("Poll() after it = (token %q, %v, %v), want the granted record still", rec.Token, done, err)
			}
		})
	}
}
