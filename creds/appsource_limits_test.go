package creds_test

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/forgeapi/v3/creds"
	"github.com/cplieger/forgeapi/v3/github/githubtest"
)

// TestReadLimit runs every GitHub rate-limit case through the App mint: a limit
// answers rate-limited with the wait the case's hold names (its Retry-After,
// else its reset, else the back-off's first minute), any other answer the
// case's kind, and both carry the figures github.ReadLimit reads.
func TestReadLimit(t *testing.T) {
	for i, named := range githubtest.LimitCases(time.Time{}) {
		t.Run(named.Name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				now := time.Now()
				c := githubtest.LimitCases(now)[i]
				g := newFakeGitHub(t, answering(appReply{status: c.Status, header: c.Header, body: string(c.Body)}))
				s := appSource(t, g, creds.InstallationByID(42))
				_, err := s.Token(t.Context())
				fe := forgeError(t, err)
				if fe.Kind != c.Kind || fe.Status != c.Status {
					t.Errorf("Token() = kind %v status %d, want %v, %d", fe.Kind, fe.Status, c.Kind, c.Status)
				}
				if fe.Pool != c.Read.Pool || fe.Remaining != c.Read.Remaining || !fe.Reset.Equal(c.Read.Reset) || fe.Secondary != c.Read.Secondary {
					t.Errorf("Token() = pool %q remaining %d reset %v secondary %t, want ReadLimit's %q, %d, %v, %t",
						fe.Pool, fe.Remaining, fe.Reset, fe.Secondary, c.Read.Pool, c.Read.Remaining, c.Read.Reset, c.Read.Secondary)
				}
				if !c.Read.Limited {
					return
				}
				wait := time.Minute
				switch {
				case c.Hold.RetryAfter > 0:
					wait = c.Hold.RetryAfter
				case !c.Hold.Reset.IsZero():
					wait = c.Hold.Reset.Sub(now)
				}
				if fe.RetryAfter != wait {
					t.Errorf("Token() = retry after %v, want the hold's %v", fe.RetryAfter, wait)
				}
			})
		})
	}
}
