package creds_test

import (
	"io"
	"log/slog"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/forgeapi/v2/creds"
	"github.com/cplieger/forgeapi/v2/github"
)

// mintedSource is a source that has minted ghs_token1 at the bubble's start.
func mintedSource(t *testing.T) (*creds.AppSource, *fakeGitHub) {
	t.Helper()
	g := newFakeGitHub(t, mintsAt(t, 42, time.Hour))
	s := appSource(t, g, creds.InstallationByID(42))
	if got, err := s.Token(t.Context()); err != nil || got != "ghs_token1" {
		t.Fatalf("Setup: Token() = %q, %v, want ghs_token1", got, err)
	}
	return s, g
}

func TestAppSource_Discard_keeps_a_token_inside_the_replication_delay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, g := mintedSource(t)
		time.Sleep(2 * time.Second)
		if !s.Discard("ghs_token1") {
			t.Error("Discard() 2 seconds after the mint = false, want true: retry the same token")
		}
		if got, err := s.Token(t.Context()); err != nil || got != "ghs_token1" || g.mints() != 1 {
			t.Errorf("Token() after a kept Discard = %q, %v after %d mints, want ghs_token1 after 1", got, err, g.mints())
		}
	})
}

// A token's age runs from the instant GitHub's answer arrived, so the pauses
// and slow answers of a mint that needed retries do not age it.
func TestAppSource_Discard_measures_the_age_from_the_answer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := newFakeGitHub(t, func(n int, _ appCall) appReply {
			if n < 3 {
				return refusal(http.StatusBadGateway, "bad gateway", nil)
			}
			slow := minted(n, time.Hour)
			slow.delay = 3 * time.Second
			return slow
		})
		s := appSource(t, g, creds.InstallationByID(42))
		if got, err := s.Token(t.Context()); err != nil || got != "ghs_token3" {
			t.Fatalf("Setup: Token() = %q, %v, want ghs_token3", got, err)
		}
		time.Sleep(3 * time.Second)
		if !s.Discard("ghs_token3") {
			t.Error("Discard() 3 seconds after the token arrived = false, want true: retry the same token")
		}
		if got, err := s.Token(t.Context()); err != nil || got != "ghs_token3" || g.mints() != 3 {
			t.Errorf("Token() after the kept Discard = %q, %v after %d requests, want ghs_token3 and no new request", got, err, g.mints())
		}
	})
}

func TestAppSource_Discard_of_an_older_token_mints_on_the_next_Token(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, g := mintedSource(t)
		time.Sleep(2 * time.Minute)
		if s.Discard("ghs_token1") {
			t.Error("Discard() 2 minutes after the mint = true, want false")
		}
		if got, err := s.Token(t.Context()); err != nil || got != "ghs_token2" || g.mints() != 2 {
			t.Errorf("Token() after the Discard = %q, %v after %d mints, want ghs_token2 after 2", got, err, g.mints())
		}
	})
}

func TestAppSource_Discard_of_another_string_does_nothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, g := mintedSource(t)
		time.Sleep(2 * time.Minute)
		for _, other := range []string{"", "ghs_other"} {
			if s.Discard(other) {
				t.Errorf("Discard(%q) = true, want false", other)
			}
		}
		if got, err := s.Token(t.Context()); err != nil || got != "ghs_token1" || g.mints() != 1 {
			t.Errorf("Token() = %q, %v after %d mints, want the kept ghs_token1 after 1", got, err, g.mints())
		}
	})
}

// A token refused between 5 seconds and a minute after its mint starts a wait
// instead of a mint: 5 minutes, doubling per further early drop, and reset
// once a token is handed out past its first minute.
func TestAppSource_Discard_of_a_young_token_waits_instead_of_minting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, g := mintedSource(t)
		time.Sleep(10 * time.Second)
		if s.Discard("ghs_token1") {
			t.Error("Discard() 10 seconds after the mint = true, want false")
		}
		time.Sleep(time.Second)
		_, err := s.Token(t.Context())
		if fe := forgeError(t, err); fe.Kind != forgeapi.KindUnauthorized || fe.RetryAfter != 5*time.Minute-time.Second || g.mints() != 1 {
			t.Errorf("Token() a second later = %+v after %d mints, want unauthorized with RetryAfter 4m59s and no request", fe, g.mints())
		}
		if got := s.State(); got != forgeapi.CredRefreshDue {
			t.Errorf("State() during the wait = %v, want %v", got, forgeapi.CredRefreshDue)
		}
		if _, err := s.Mint(t.Context()); forgeError(t, err).Kind != forgeapi.KindUnauthorized || g.mints() != 1 {
			t.Errorf("Mint() during the wait = %v after %d mints, want the wait and no request", err, g.mints())
		}

		time.Sleep(5*time.Minute - time.Second)
		if got, err := s.Token(t.Context()); err != nil || got != "ghs_token2" {
			t.Fatalf("Token() after the wait = %q, %v, want ghs_token2", got, err)
		}
		time.Sleep(10 * time.Second)
		s.Discard("ghs_token2")
		_, err = s.Token(t.Context())
		if fe := forgeError(t, err); fe.RetryAfter != 10*time.Minute {
			t.Errorf("Token() after a second early Discard = %+v, want the wait doubled to 10m", fe)
		}

		time.Sleep(10 * time.Minute)
		if _, err := s.Token(t.Context()); err != nil {
			t.Fatalf("Token() after the doubled wait = %v", err)
		}
		time.Sleep(time.Minute)
		if got, err := s.Token(t.Context()); err != nil || got != "ghs_token3" {
			t.Fatalf("Token() past the token's first minute = %q, %v, want ghs_token3", got, err)
		}
		s.Discard("ghs_token3")
		if got, err := s.Token(t.Context()); err != nil || got != "ghs_token4" {
			t.Fatalf("Token() after a late Discard = %q, %v, want ghs_token4", got, err)
		}
		time.Sleep(10 * time.Second)
		s.Discard("ghs_token4")
		_, err = s.Token(t.Context())
		if fe := forgeError(t, err); fe.RetryAfter != 5*time.Minute {
			t.Errorf("Token() after an early Discard that followed a settled token = %+v, want the wait reset to 5m", fe)
		}
	})
}

// A token's first minute runs from the instant its answer arrived, so a slow
// answer does not settle the early-drop wait sooner.
func TestAppSource_a_slow_answer_does_not_settle_the_early_drop_wait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := newFakeGitHub(t, func(n int, _ appCall) appReply {
			answer := minted(n, time.Hour)
			if n > 1 {
				answer.delay = 15 * time.Second
			}
			return answer
		})
		s := appSource(t, g, creds.InstallationByID(42))
		if _, err := s.Token(t.Context()); err != nil {
			t.Fatalf("Setup: Token() = %v", err)
		}
		time.Sleep(10 * time.Second)
		s.Discard("ghs_token1")
		time.Sleep(5 * time.Minute)
		if got, err := s.Token(t.Context()); err != nil || got != "ghs_token2" {
			t.Fatalf("Token() after the wait = %q, %v, want ghs_token2", got, err)
		}
		time.Sleep(50 * time.Second)
		if got, err := s.Token(t.Context()); err != nil || got != "ghs_token2" {
			t.Fatalf("Token() 50 seconds after the slow answer = %q, %v, want ghs_token2", got, err)
		}
		s.Discard("ghs_token2")
		_, err := s.Token(t.Context())
		if fe := forgeError(t, err); fe.RetryAfter != 10*time.Minute {
			t.Errorf("Token() after a second early Discard = %+v, want the wait doubled to 10m", fe)
		}
	})
}

// clientOver is a github client for appWeb whose credential is s, over g.
func clientOver(t *testing.T, g *fakeGitHub, s *creds.AppSource) *github.Client {
	t.Helper()
	c, err := github.New(forgeapi.Connection{WebBaseURL: appWeb},
		forgeapi.WithWireTransport(g), forgeapi.WithCredentialSource(s),
		forgeapi.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatalf("Setup: github.New: %v", err)
	}
	return c
}

// userRefusedOnce mints at installation 42 and answers GET /user 401 the
// first time, then the account.
func userRefusedOnce(t *testing.T) func(int, appCall) appReply {
	refused := false
	mint := mintsAt(t, 42, time.Hour)
	return func(n int, c appCall) appReply {
		if c.path != apiPrefix+"/user" {
			return mint(n, c)
		}
		if !refused {
			refused = true
			return refusal(http.StatusUnauthorized, "Bad credentials", nil)
		}
		return appReply{status: http.StatusOK, body: `{"login":"app[bot]","id":1}`}
	}
}

// userCalls is the Authorization header of every GET /user g received.
func userCalls(g *fakeGitHub) []string {
	var auth []string
	for _, c := range g.calls() {
		if c.path == apiPrefix+"/user" {
			auth = append(auth, c.header.Get("Authorization"))
		}
	}
	return auth
}

func TestAppSource_Discard_through_a_client(t *testing.T) {
	t.Run("a_401_inside_the_replication_delay", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			g := newFakeGitHub(t, userRefusedOnce(t))
			s := appSource(t, g, creds.InstallationByID(42))
			if _, err := s.Token(t.Context()); err != nil {
				t.Fatalf("Setup: Token(): %v", err)
			}
			time.Sleep(2 * time.Second)
			if _, err := clientOver(t, g, s).Whoami(t.Context()); err != nil {
				t.Fatalf("Whoami() answered 401 2 seconds after the mint, then 200 = %v, want nil", err)
			}
			if auth := userCalls(g); len(auth) != 2 || auth[0] != "Bearer ghs_token1" || auth[1] != auth[0] || g.mints() != 1 {
				t.Errorf("GET /user carried %q after %d mints, want ghs_token1 twice after 1", auth, g.mints())
			}
			if got, err := s.Token(t.Context()); err != nil || got != "ghs_token1" || s.State() != forgeapi.CredValid {
				t.Errorf("Token() after = %q, %v in state %v, want ghs_token1, no wait, valid", got, err, s.State())
			}
		})
	})
	t.Run("a_401_after_the_replication_delay", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			g := newFakeGitHub(t, userRefusedOnce(t))
			s := appSource(t, g, creds.InstallationByID(42))
			if _, err := s.Token(t.Context()); err != nil {
				t.Fatalf("Setup: Token(): %v", err)
			}
			time.Sleep(10 * time.Second)
			_, err := clientOver(t, g, s).Whoami(t.Context())
			if fe := forgeError(t, err); fe.Kind != forgeapi.KindUnauthorized || len(userCalls(g)) != 1 {
				t.Errorf("Whoami() answered 401 10 seconds after the mint = %+v after %d request(s), want unauthorized after 1", fe, len(userCalls(g)))
			}
			_, err = s.Token(t.Context())
			if fe := forgeError(t, err); fe.Kind != forgeapi.KindUnauthorized || fe.RetryAfter != 5*time.Minute || g.mints() != 1 {
				t.Errorf("Token() after = %+v after %d mints, want the 5-minute wait and no mint", fe, g.mints())
			}
		})
	})
}
