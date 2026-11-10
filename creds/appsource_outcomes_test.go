package creds_test

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/forgeapi/v2/creds"
)

// answering answers every request with reply, numbering nothing.
func answering(reply appReply) func(int, appCall) appReply {
	return func(int, appCall) appReply { return reply }
}

// Only evidence that the key, the installation or a permission needs a human is
// terminal: each reaches reconnect-required with a message naming which, and
// the next call fails at once without a request.
func TestAppSource_terminal_answers_need_a_human(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply appReply
		kind  forgeapi.ErrorKind
		names string
	}{
		{"a 401", refusal(http.StatusUnauthorized, "A JSON web token could not be decoded", nil), forgeapi.KindUnauthorized, "rejected the App JWT"},
		{"a 404", refusal(http.StatusNotFound, "Not Found", nil), forgeapi.KindNotFound, "no installation"},
		{"a plain 403", refusal(http.StatusForbidden, "This installation has been suspended", nil), forgeapi.KindForbidden, "suspended or lacks a permission"},
		{"a 422 whatever its message", refusal(http.StatusUnprocessableEntity, "Validation failed, or the endpoint has been spammed.", nil), forgeapi.KindUpstream, "repositories or permissions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				g := newFakeGitHub(t, answering(tc.reply))
				s := appSource(t, g, creds.InstallationByID(42))
				_, err := s.Token(t.Context())
				fe := forgeError(t, err)
				if fe.Code != forgeapi.CodeReconnectRequired || fe.Kind != tc.kind || fe.Status != tc.reply.status || fe.Retryable {
					t.Errorf("Token() = %+v, want code %q, kind %v, status %d, not retryable", fe, forgeapi.CodeReconnectRequired, tc.kind, tc.reply.status)
				}
				if !strings.Contains(fe.Message, tc.names) {
					t.Errorf("Token() message = %q, want it to name %q", fe.Message, tc.names)
				}
				if got := s.State(); got != forgeapi.CredReconnectRequired {
					t.Errorf("State() = %v, want %v", got, forgeapi.CredReconnectRequired)
				}
				time.Sleep(time.Minute)
				if _, err := s.Mint(t.Context()); codeOf(err) != forgeapi.CodeReconnectRequired || g.mints() != 1 {
					t.Errorf("Mint() a minute later = %v after %d requests, want the terminal refusal and no new request", err, g.mints())
				}
			})
		})
	}
}

// After a terminal answer the source re-probes with one mint 5 minutes later,
// doubling per further refusal to an hour, and a success leaves the state and
// resets the schedule.
func TestAppSource_terminal_reprobe_backs_off_to_an_hour(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		healed := false
		g := newFakeGitHub(t, func(n int, _ appCall) appReply {
			if healed {
				return minted(n, time.Hour)
			}
			return refusal(http.StatusUnauthorized, "Bad credentials", nil)
		})
		s := appSource(t, g, creds.InstallationByID(42))
		if _, err := s.Token(t.Context()); codeOf(err) != forgeapi.CodeReconnectRequired {
			t.Fatalf("Token() = %v, want %q", err, forgeapi.CodeReconnectRequired)
		}
		sent := 1
		for _, wait := range []time.Duration{5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 40 * time.Minute, time.Hour, time.Hour} {
			time.Sleep(wait - time.Second)
			if _, err := s.Token(t.Context()); codeOf(err) != forgeapi.CodeReconnectRequired || g.mints() != sent {
				t.Fatalf("Token() a second before the %v re-probe = %v after %d requests, want the refusal after %d", wait, err, g.mints(), sent)
			}
			time.Sleep(time.Second)
			sent++
			if _, err := s.Token(t.Context()); codeOf(err) != forgeapi.CodeReconnectRequired || g.mints() != sent {
				t.Fatalf("Token() at the %v re-probe = %v after %d requests, want one more refused request, %d", wait, err, g.mints(), sent)
			}
		}
		healed = true
		time.Sleep(time.Hour)
		if _, err := s.Token(t.Context()); err != nil {
			t.Fatalf("Token() at the re-probe after the fix = %v", err)
		}
		if got := s.State(); got != forgeapi.CredValid {
			t.Errorf("State() after a successful re-probe = %v, want %v", got, forgeapi.CredValid)
		}
		healed = false
		time.Sleep(time.Hour)
		if _, err := s.Token(t.Context()); codeOf(err) != forgeapi.CodeReconnectRequired {
			t.Fatalf("Token() = %v, want a fresh terminal refusal", err)
		}
		before := g.mints()
		time.Sleep(5 * time.Minute)
		if _, err := s.Token(t.Context()); g.mints() != before+1 {
			t.Errorf("Token() 5 minutes after a terminal answer that followed a success = %v after %d requests, want one re-probe, so the schedule was reset", err, g.mints()-before)
		}
	})
}

// A re-probe that meets anything but another terminal answer keeps the
// re-probe's wait: the next one goes out 5 minutes later, not sooner, and the
// source still reads reconnect-required.
func TestAppSource_a_failed_reprobe_keeps_the_reprobe_wait(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply appReply
	}{
		{"a 500", refusal(http.StatusInternalServerError, "oops", nil)},
		{"a 429 asking for 30 seconds", refusal(http.StatusTooManyRequests, "slow down", http.Header{"Retry-After": {"30"}})},
		{"a 400", refusal(http.StatusBadRequest, "Problems parsing JSON", nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				terminal := true
				g := newFakeGitHub(t, func(int, appCall) appReply {
					if terminal {
						return refusal(http.StatusUnauthorized, "Bad credentials", nil)
					}
					return tc.reply
				})
				s := appSource(t, g, creds.InstallationByID(42))
				if _, err := s.Token(t.Context()); codeOf(err) != forgeapi.CodeReconnectRequired {
					t.Fatalf("Setup: Token() = %v, want %q", err, forgeapi.CodeReconnectRequired)
				}
				terminal = false
				time.Sleep(5 * time.Minute)
				if _, err := s.Token(t.Context()); err == nil {
					t.Fatal("Token() at the re-probe succeeded")
				}
				reprobed := g.mints()
				time.Sleep(5*time.Minute - time.Second)
				if _, err := s.Token(t.Context()); codeOf(err) != forgeapi.CodeReconnectRequired || g.mints() != reprobed {
					t.Errorf("Token() a second before the next re-probe = %v after %d requests, want the terminal refusal and none after %d", err, g.mints(), reprobed)
				}
				if got := s.State(); got != forgeapi.CredReconnectRequired {
					t.Errorf("State() = %v, want %v", got, forgeapi.CredReconnectRequired)
				}
				time.Sleep(time.Second)
				if _, err := s.Token(t.Context()); err == nil || g.mints() == reprobed {
					t.Errorf("Token() at the next re-probe = %v after %d requests, want one more failed re-probe", err, g.mints())
				}
			})
		})
	}
}

// A 403 carrying only GitHub's secondary-rate-limit message sets the
// not-before instant a minute out, doubling per consecutive header-less limit
// to 15 minutes, and a success resets it. Nothing is sent before the instant.
func TestAppSource_rate_limit_sets_the_not_before_instant(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		limited := true
		g := newFakeGitHub(t, func(n int, _ appCall) appReply {
			if limited {
				return refusal(http.StatusForbidden, "You have exceeded a secondary rate limit. Please wait a few minutes before you try again.", nil)
			}
			return minted(n, time.Hour)
		})
		counted := 0
		s := appSource(t, g, creds.InstallationByID(42), forgeapi.WithCounters(forgeapi.Counters{RateLimited: func(forgeapi.Family) { counted++ }}))
		_, err := s.Token(t.Context())
		if fe := forgeError(t, err); fe.Kind != forgeapi.KindRateLimited || fe.RetryAfter != time.Minute || fe.Code != "" {
			t.Fatalf("Token() = %+v, want rate-limited with a minute's RetryAfter", fe)
		}
		if got := s.State(); got != forgeapi.CredRefreshDue {
			t.Errorf("State() while rate-limited = %v, want %v", got, forgeapi.CredRefreshDue)
		}
		time.Sleep(5 * time.Second)
		_, err = s.Token(t.Context())
		if fe := forgeError(t, err); fe.Kind != forgeapi.KindRateLimited || fe.RetryAfter != 55*time.Second || g.mints() != 1 {
			t.Errorf("Token() 5 seconds later = %+v after %d requests, want rate-limited with 55 seconds left and no request", fe, g.mints())
		}
		time.Sleep(55 * time.Second)
		sent := 1
		for _, want := range []time.Duration{2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 15 * time.Minute, 15 * time.Minute} {
			_, err = s.Token(t.Context())
			sent++
			if fe := forgeError(t, err); fe.RetryAfter != want || g.mints() != sent {
				t.Fatalf("Token() at the instant = %+v after %d requests, want one request and a %v wait", fe, g.mints(), want)
			}
			time.Sleep(want)
		}
		if counted != sent {
			t.Errorf("the RateLimited counter fired %d times, want once per limited answer, %d", counted, sent)
		}
		limited = false
		if _, err := s.Token(t.Context()); err != nil {
			t.Fatalf("Token() after the limit = %v", err)
		}
		limited = true
		time.Sleep(time.Hour)
		if _, err := s.Token(t.Context()); forgeError(t, err).RetryAfter != time.Minute {
			t.Errorf("Token() limited after a success = %v, want the wait reset to a minute", err)
		}
	})
}

// A wait GitHub states is the one in force: Retry-After, else the primary
// window's reset where nothing remains; a 429 is a limit as a 403 is.
func TestAppSource_rate_limit_takes_the_wait_GitHub_states(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		header func(now time.Time) http.Header
		want   time.Duration
	}{
		{"a 403 with Retry-After", http.StatusForbidden, func(time.Time) http.Header { return http.Header{"Retry-After": {"30"}} }, 30 * time.Second},
		{"a 429 with Retry-After", http.StatusTooManyRequests, func(time.Time) http.Header { return http.Header{"Retry-After": {"90"}} }, 90 * time.Second},
		{"a 429 stating nothing", http.StatusTooManyRequests, func(time.Time) http.Header { return nil }, time.Minute},
		{"a 403 with the primary budget spent", http.StatusForbidden, func(now time.Time) http.Header {
			return http.Header{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {strconv.FormatInt(now.Add(10*time.Minute).Unix(), 10)}}
		}, 10 * time.Minute},
		{"a Retry-After past an hour", http.StatusForbidden, func(time.Time) http.Header { return http.Header{"Retry-After": {"86400"}} }, time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				g := newFakeGitHub(t, func(int, appCall) appReply {
					return refusal(tc.status, "API rate limit exceeded", tc.header(time.Now()))
				})
				s := appSource(t, g, creds.InstallationByID(42))
				_, err := s.Token(t.Context())
				if fe := forgeError(t, err); fe.Kind != forgeapi.KindRateLimited || fe.RetryAfter != tc.want || !fe.Retryable {
					t.Errorf("Token() = %+v, want rate-limited and retryable with RetryAfter %v", fe, tc.want)
				}
				time.Sleep(tc.want - time.Second)
				if _, err := s.Mint(t.Context()); g.mints() != 1 {
					t.Errorf("Mint() before the instant = %v after %d requests, want none sent", err, g.mints())
				}
			})
		})
	}
}

// A still-valid cached token is answered whatever the renewal meets.
func TestAppSource_Token_answers_a_valid_cached_token_through_a_failed_renewal(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply appReply
	}{
		{"a rate limit", refusal(http.StatusTooManyRequests, "slow down", nil)},
		{"a terminal answer", refusal(http.StatusUnauthorized, "Bad credentials", nil)},
		{"a server failure", refusal(http.StatusInternalServerError, "oops", nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				first := true
				g := newFakeGitHub(t, func(n int, _ appCall) appReply {
					if first {
						first = false
						return minted(n, time.Hour)
					}
					return tc.reply
				})
				s := appSource(t, g, creds.InstallationByID(42))
				if _, err := s.Token(t.Context()); err != nil {
					t.Fatalf("Token() = %v", err)
				}
				time.Sleep(58 * time.Minute)
				for range 2 {
					if got, err := s.Token(t.Context()); err != nil || got != "ghs_token1" {
						t.Errorf("Token() inside the lead with renewal failing = %q, %v, want the unexpired ghs_token1", got, err)
					}
				}
				time.Sleep(2 * time.Minute)
				if got, err := s.Token(t.Context()); err == nil {
					t.Errorf("Token() once the cached token expired = %q, want the renewal's failure", got)
				}
			})
		})
	}
}

// A 5xx, a dropped connection, a tokenless answer and an answer with a past
// or unreadable expiry cost one more mint: each is retried, and the budget's
// retries bound every token request one mint sends, whatever mix of these it
// meets. Once they are spent the state reads refresh-due, never
// reconnect-required.
func TestAppSource_transient_failures_retry_and_end_refresh_due(t *testing.T) {
	expired := time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	gateway := refusal(http.StatusBadGateway, "bad gateway", nil)
	dropped := appReply{drop: true}
	tokenless := appReply{status: http.StatusCreated, body: `{"expires_at":"2099-01-01T00:00:00Z"}`}
	for _, tc := range []struct {
		reply func(int, appCall) appReply
		name  string
	}{
		{name: "a 500", reply: answering(refusal(http.StatusInternalServerError, "oops", nil))},
		{name: "a 502", reply: answering(gateway)},
		{name: "a 503", reply: answering(refusal(http.StatusServiceUnavailable, "unavailable", nil))},
		{name: "a dropped connection", reply: answering(dropped)},
		{name: "a tokenless answer", reply: answering(tokenless)},
		{name: "a past expiry", reply: answering(appReply{status: http.StatusCreated, body: `{"token":"ghs_x","expires_at":"` + expired + `"}`})},
		{name: "an unreadable expiry", reply: answering(appReply{status: http.StatusCreated, body: `{"token":"ghs_x","expires_at":"tomorrow"}`})},
		{name: "a body that is not JSON", reply: answering(appReply{status: http.StatusCreated, body: `ghs_x`})},
		{name: "a 502, a dropped connection, then a tokenless answer", reply: func(n int, _ appCall) appReply {
			return []appReply{gateway, dropped, tokenless}[(n-1)%3]
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				g := newFakeGitHub(t, tc.reply)
				retried := 0
				s := appSource(t, g, creds.InstallationByID(42), forgeapi.WithCounters(forgeapi.Counters{
					Retried: func(forgeapi.Family, string) { retried++ },
				}))
				_, err := s.Token(t.Context())
				if fe := forgeError(t, err); fe.Kind != forgeapi.KindTransient || !fe.Retryable || fe.Code == forgeapi.CodeReconnectRequired {
					t.Errorf("Token() = %+v, want a retryable transient failure", fe)
				}
				if strings.Contains(err.Error(), "ghs_x") {
					t.Errorf("Token() = %v, which quotes the answer's body", err)
				}
				if n := g.mints(); n != forgeapi.DefaultRetries+1 {
					t.Errorf("GitHub received %d token requests, want %d", n, forgeapi.DefaultRetries+1)
				}
				if retried != forgeapi.DefaultRetries {
					t.Errorf("the Retried counter fired %d times, want once per retry, %d", retried, forgeapi.DefaultRetries)
				}
				if got := s.State(); got != forgeapi.CredRefreshDue {
					t.Errorf("State() = %v, want %v", got, forgeapi.CredRefreshDue)
				}
			})
		})
	}
	t.Run("a 500 that recovers", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			g := newFakeGitHub(t, func(n int, _ appCall) appReply {
				if n < 3 {
					return refusal(http.StatusInternalServerError, "oops", nil)
				}
				return minted(n, time.Hour)
			})
			s := appSource(t, g, creds.InstallationByID(42))
			if got, err := s.Token(t.Context()); err != nil || got != "ghs_token3" {
				t.Errorf("Token() = %q, %v, want the third attempt's ghs_token3", got, err)
			}
		})
	})
}

// The retries of one mint stay inside the operation's deadline: a pause the
// deadline cannot hold is not taken, so the caller reads GitHub's last answer
// and not a deadline error its own context did not cause.
func TestAppSource_mint_retries_end_inside_the_operation_deadline(t *testing.T) {
	for _, tc := range []struct {
		name    string
		timeout time.Duration
		retries int
	}{
		{"eight retries under the default deadline", forgeapi.DefaultOperationTimeout, 8},
		{"a deadline shorter than the first pause", 100 * time.Millisecond, forgeapi.DefaultRetries},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				g := newFakeGitHub(t, answering(refusal(http.StatusInternalServerError, "oops", nil)))
				s := appSource(t, g, creds.InstallationByID(42), forgeapi.WithRetries(tc.retries), forgeapi.WithOperationTimeout(tc.timeout))
				started := time.Now()
				_, err := s.Token(t.Context())
				if took := time.Since(started); took > tc.timeout {
					t.Errorf("Token() took %v, want it inside the %v deadline", took, tc.timeout)
				}
				if fe := forgeError(t, err); fe.Status != http.StatusInternalServerError || fe.Kind != forgeapi.KindTransient {
					t.Errorf("Token() = %+v, want GitHub's 500", fe)
				}
				if g.mints() == 0 {
					t.Error("GitHub received no token request")
				}
			})
		})
	}
}

// A retry the operation's deadline cuts short while it is in flight answers
// the attempt before it, so the caller reads GitHub's answer and not a
// deadline its own context did not set; a caller whose own context ended
// reads that context's error.
func TestAppSource_a_retry_the_deadline_cuts_short_answers_GitHubs_last_answer(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reply   appReply
		timeout time.Duration
	}{
		{"a 504 after 8 seconds under the default deadline", appReply{status: http.StatusGatewayTimeout, body: `{}`, delay: 8 * time.Second}, forgeapi.DefaultOperationTimeout},
		{"a 500 after 200ms under a deadline of 3.5 seconds", appReply{status: http.StatusInternalServerError, body: `{}`, delay: 200 * time.Millisecond}, 3500 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				g := newFakeGitHub(t, answering(tc.reply))
				s := appSource(t, g, creds.InstallationByID(42), forgeapi.WithOperationTimeout(tc.timeout))
				started := time.Now()
				_, err := s.Token(t.Context())
				if took := time.Since(started); took > tc.timeout {
					t.Errorf("Token() took %v, want it inside the %v deadline", took, tc.timeout)
				}
				if fe := forgeError(t, err); fe.Status != tc.reply.status || fe.Kind != forgeapi.KindTransient {
					t.Errorf("Token() = %+v, want GitHub's %d", fe, tc.reply.status)
				}
				if n := g.mints(); n != forgeapi.DefaultRetries+1 {
					t.Errorf("GitHub received %d token requests, want %d, the last cut short", n, forgeapi.DefaultRetries+1)
				}
			})
		})
	}
	slow := appReply{status: http.StatusInternalServerError, body: `{}`, delay: 200 * time.Millisecond}
	t.Run("the caller's own deadline answers its own error", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3500*time.Millisecond)
			defer cancel()
			s := appSource(t, newFakeGitHub(t, answering(slow)), creds.InstallationByID(42))
			if _, err := s.Token(ctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("Token() past the caller's deadline = %v, want %v", err, context.DeadlineExceeded)
			}
		})
	})
	t.Run("the caller's cancel during a pause answers its own error", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			time.AfterFunc(500*time.Millisecond, cancel)
			g := newFakeGitHub(t, answering(slow))
			s := appSource(t, g, creds.InstallationByID(42))
			if _, err := s.Token(ctx); !errors.Is(err, context.Canceled) || g.mints() != 1 {
				t.Errorf("Token() cancelled in the first pause = %v after %d token requests, want %v after 1", err, g.mints(), context.Canceled)
			}
		})
	})
}

// A caller that cancels its own call while a re-probe is in flight moves no
// standing: GitHub answered nothing, so the next call sends the re-probe and
// only the answered requests are counted as outcomes.
func TestAppSource_a_callers_cancel_spends_no_reprobe(t *testing.T) {
	cancelled := func(t *testing.T) context.Context {
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		time.AfterFunc(500*time.Millisecond, cancel)
		return ctx
	}
	t.Run("Token", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			g := newFakeGitHub(t, func(n int, _ appCall) appReply {
				switch n {
				case 1:
					return refusal(http.StatusUnauthorized, "Bad credentials", nil)
				case 2:
					return appReply{status: http.StatusInternalServerError, body: `{}`, delay: 2 * time.Second}
				}
				return minted(n, time.Hour)
			})
			var o outcomes
			s := appSource(t, g, creds.InstallationByID(42), forgeapi.WithCounters(o.counters()))
			if _, err := s.Token(t.Context()); codeOf(err) != forgeapi.CodeReconnectRequired {
				t.Fatalf("Setup: Token() = %v, want %q", err, forgeapi.CodeReconnectRequired)
			}
			time.Sleep(5 * time.Minute)
			if _, err := s.Token(cancelled(t)); !errors.Is(err, context.Canceled) || g.mints() != 2 {
				t.Fatalf("Token() cancelled during the re-probe = %v after %d token requests, want %v after 2", err, g.mints(), context.Canceled)
			}
			if _, err := s.Token(t.Context()); err != nil || g.mints() != 3 {
				t.Errorf("Token() after the cancel = %v after %d token requests, want the re-probe sent and a token", err, g.mints())
			}
			want := []string{"github reconnect_required", "github valid"}
			if got := o.list(); strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("RefreshOutcome = %v, want %v: the cancelled re-probe is no outcome", got, want)
			}
		})
	})
	t.Run("a deadline still keeps the re-probe wait", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			g := newFakeGitHub(t, func(n int, _ appCall) appReply {
				if n == 1 {
					return refusal(http.StatusUnauthorized, "Bad credentials", nil)
				}
				return appReply{status: http.StatusInternalServerError, body: `{}`, delay: 2 * time.Second}
			})
			s := appSource(t, g, creds.InstallationByID(42))
			if _, err := s.Token(t.Context()); codeOf(err) != forgeapi.CodeReconnectRequired {
				t.Fatalf("Setup: Token() = %v, want %q", err, forgeapi.CodeReconnectRequired)
			}
			time.Sleep(5 * time.Minute)
			ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
			defer cancel()
			if _, err := s.Token(ctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Token() past the caller's deadline = %v, want %v", err, context.DeadlineExceeded)
			}
			if _, err := s.Token(t.Context()); codeOf(err) != forgeapi.CodeReconnectRequired || g.mints() != 2 {
				t.Errorf("Token() after the deadline = %v after %d token requests, want the terminal refusal and none after 2", err, g.mints())
			}
		})
	})
	t.Run("Resolve", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			lookup := apiPrefix + "/orgs/octo-org/installation"
			g := newFakeGitHub(t, func(n int, c appCall) appReply {
				if c.method == http.MethodPost {
					return refusal(http.StatusNotFound, "Not Found", nil)
				}
				if c.path != lookup {
					t.Errorf("%s %s reached GitHub, want only %s and the token request", c.method, c.path, lookup)
				}
				if n == 3 {
					return appReply{status: http.StatusOK, body: `{"id":42}`, delay: 2 * time.Second}
				}
				return appReply{status: http.StatusOK, body: `{"id":42}`}
			})
			s := appSource(t, g, creds.InstallationOfOrg("octo-org"))
			if _, err := s.Token(t.Context()); codeOf(err) != forgeapi.CodeReconnectRequired {
				t.Fatalf("Setup: Token() = %v, want %q", err, forgeapi.CodeReconnectRequired)
			}
			time.Sleep(5 * time.Minute)
			if _, err := s.Resolve(cancelled(t)); !errors.Is(err, context.Canceled) {
				t.Fatalf("Resolve() cancelled during the lookup = %v, want %v", err, context.Canceled)
			}
			id, err := s.Resolve(t.Context())
			if lookups := len(g.calls()) - g.mints(); err != nil || id != 42 || lookups != 3 {
				t.Errorf("Resolve() after the cancel = %d, %v after %d lookups, want 42 from a third lookup", id, err, lookups)
			}
		})
	})
}

// A refusal in none of the classes the source reads, such as a 400, would
// meet a resend the same way: the source waits a minute before sending again,
// doubling per consecutive one, and reads refresh-due, since nothing shows a
// human is needed.
func TestAppSource_an_unclassified_refusal_waits_before_sending_again(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := newFakeGitHub(t, answering(refusal(http.StatusBadRequest, "Problems parsing JSON", nil)))
		s := appSource(t, g, creds.InstallationByID(42))
		_, err := s.Token(t.Context())
		if fe := forgeError(t, err); fe.Status != http.StatusBadRequest || fe.RetryAfter != time.Minute || fe.Code == forgeapi.CodeReconnectRequired {
			t.Fatalf("Token() = %+v, want the 400 with a minute's RetryAfter", fe)
		}
		_, err = s.Token(t.Context())
		if fe := forgeError(t, err); fe.Kind != forgeapi.KindUpstream || fe.RetryAfter != time.Minute || g.mints() != 1 {
			t.Errorf("Token() at once after = %+v after %d requests, want an upstream refusal waiting a minute and no new request", fe, g.mints())
		}
		if got := s.State(); got != forgeapi.CredRefreshDue {
			t.Errorf("State() = %v, want %v", got, forgeapi.CredRefreshDue)
		}
		time.Sleep(time.Minute)
		_, err = s.Token(t.Context())
		if fe := forgeError(t, err); fe.RetryAfter != 2*time.Minute || g.mints() != 2 {
			t.Errorf("Token() a minute later = %+v after %d requests, want one more request and a 2-minute wait", fe, g.mints())
		}
	})
}

// GitHub's message is server text: it reaches the error on one line, bounded,
// with the App JWT the request carried redacted out of it.
func TestAppSource_quotes_GitHubs_message_safely(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := newFakeGitHub(t, func(_ int, c appCall) appReply {
			jwt := strings.TrimPrefix(c.header.Get("Authorization"), "Bearer ")
			return refusal(http.StatusUnauthorized, "bad\nline\x07 "+jwt+" "+strings.Repeat("x", 2000), nil)
		})
		s := appSource(t, g, creds.InstallationByID(42))
		_, err := s.Token(t.Context())
		fe := forgeError(t, err)
		jwt := strings.TrimPrefix(g.calls()[0].header.Get("Authorization"), "Bearer ")
		if jwt == "" || strings.Contains(fe.Message, jwt) {
			t.Errorf("Token() message carries the request's App JWT: %q", fe.Message)
		}
		if strings.ContainsAny(fe.Message, "\n\r\x07") {
			t.Errorf("Token() message = %q, want one line with no control character", fe.Message)
		}
		if len(fe.Message) > 600 {
			t.Errorf("Token() message is %d bytes, want it bounded", len(fe.Message))
		}
	})
}

// Each renewal is counted by the state it reached.
func TestAppSource_counts_each_renewal_outcome(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := newFakeGitHub(t, func(n int, _ appCall) appReply {
			if n == 1 {
				return minted(n, time.Hour)
			}
			return refusal(http.StatusUnauthorized, "Bad credentials", nil)
		})
		var o outcomes
		s := appSource(t, g, creds.InstallationByID(42), forgeapi.WithCounters(o.counters()))
		if _, err := s.Mint(t.Context()); err != nil {
			t.Fatalf("Mint() = %v", err)
		}
		if _, err := s.Mint(t.Context()); err == nil {
			t.Fatal("Mint() against a rejected key succeeded")
		}
		want := []string{"github valid", "github reconnect_required"}
		if got := o.list(); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("RefreshOutcome = %v, want %v", got, want)
		}
	})
}
