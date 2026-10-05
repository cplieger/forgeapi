package families_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/families"
	"github.com/cplieger/forgeapi/gitea"
	"github.com/cplieger/forgeapi/github"
	"github.com/cplieger/forgeapi/gitlab"
)

// A family's read that outlives its own operation deadline is that family's absence
// and not the caller's context ending, so the walk spends that deadline and asks the
// next family.
func TestOpenAsksTheNextFamilyWhereARefusedReadOutlivesItsOperationDeadline(t *testing.T) {
	product := detectedProducts["Gitea"]
	own := connectionRead(t, product.dir)
	foreign := foreignReads(t)[product.dir]
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.EscapedPath()
		mu.Lock()
		seen = append(seen, r.Method+" "+path)
		mu.Unlock()
		if path == "/api/v4/metadata" {
			select {
			case <-r.Context().Done():
			case <-time.After(10 * time.Second):
				t.Errorf("Setup: the request to %s outlived its operation deadline by ten seconds", path)
			}
			return
		}
		answer, ok := ownAnswer(own, product.root, r.Method, path)
		if !ok {
			answer, _ = foreignAnswer(foreign, r.Method, path)
		}
		for k, v := range answer.Headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(answer.Status)
		if _, err := w.Write(answer.payload()); err != nil {
			t.Errorf("Setup: writing %s %s: %v", r.Method, path, err)
		}
	}))
	t.Cleanup(srv.Close)
	opts := append(openOptions(srv), forgeapi.WithOperationTimeout(500*time.Millisecond))

	core, family, err := families.Open(t.Context(), forgeapi.Connection{WebBaseURL: srv.URL}, opts...)
	if err != nil {
		t.Fatalf("families.Open on a Gitea instance whose GitLab read outlives its deadline = error %v, want %v: %v", err, forgeapi.FamilyGitea, seen)
	}
	if core == nil || family != forgeapi.FamilyGitea {
		t.Errorf("families.Open on a Gitea instance whose GitLab read outlives its deadline = (%T, %v), want a client and %v", core, family, forgeapi.FamilyGitea)
	}
	mu.Lock()
	defer mu.Unlock()
	if i, j := slices.Index(seen, "GET /api/v4/metadata"), slices.Index(seen, "GET /api/v3/meta"); i < 0 || j < i {
		t.Errorf("families.Open sent %v, want GitHub's read asked after GitLab's spent its deadline", seen)
	}
}

// An instance where every family's read outlives its own operation deadline, the
// caller's context alive throughout, establishes no family, so the answer is the
// undetected refusal and not a context sentinel: no request was answered, so it
// carries no status.
func TestOpenRefusesAnInstanceWhereEveryFamilysReadOutlivesItsOperationDeadline(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.EscapedPath())
		mu.Unlock()
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
			t.Errorf("Setup: the request to %s outlived its operation deadline by ten seconds", r.URL.EscapedPath())
		}
	}))
	t.Cleanup(srv.Close)
	opts := append(openOptions(srv), forgeapi.WithOperationTimeout(200*time.Millisecond))

	core, family, err := families.Open(t.Context(), forgeapi.Connection{WebBaseURL: srv.URL}, opts...)
	if ctxErr := t.Context().Err(); ctxErr != nil {
		t.Fatalf("Setup: the caller's context ended during detection: %v", ctxErr)
	}
	if core != nil || family != forgeapi.FamilyUnknown {
		t.Errorf("families.Open on an instance that answers no read = (%T, %v), want no client and %v", core, family, forgeapi.FamilyUnknown)
	}
	fe, ok := errors.AsType[*forgeapi.Error](err)
	switch {
	case !ok:
		t.Fatalf("families.Open on an instance that answers no read = error %v (%T), want a *forgeapi.Error", err, err)
	case fe.Code != forgeapi.CodeFamilyUndetected || fe.Family != forgeapi.FamilyUnknown:
		t.Errorf("families.Open on an instance that answers no read = (code %q, family %v), want (%q, %v)", fe.Code, fe.Family, forgeapi.CodeFamilyUndetected, forgeapi.FamilyUnknown)
	case fe.Status != 0:
		t.Errorf("families.Open on an instance that answers no read = status %d, want 0: no read was answered", fe.Status)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Contains(seen, "GET /api/v1/version") {
		t.Errorf("families.Open on an instance that answers no read sent %v, want the Gitea family's read asked last", seen)
	}
}

// A context that ends in the detected family's later setup read, after its witness
// arrived, still ends detection with the context's sentinel: no client is handed out
// holding answers a cut read decided.
func TestOpenAnswersTheContextsSentinelWhereItEndsInTheDetectedFamilysLaterRead(t *testing.T) {
	for name, cutAt := range map[string]string{
		"GitHub":  "GET /api/v3/versions",
		"GitLab":  "POST /api/graphql",
		"Gitea":   "GET /api/v1/settings/api",
		"Forgejo": "GET /api/v1/settings/api",
	} {
		t.Run(name, func(t *testing.T) {
			product := detectedProducts[name]
			own := connectionRead(t, product.dir)
			foreign := foreignReads(t)[product.dir]
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var mu sync.Mutex
			var seen []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// A body read to its end is what lets the server see the client go away.
				if _, err := io.Copy(io.Discard, io.LimitReader(r.Body, 1<<20)); err != nil {
					t.Errorf("Setup: reading the request body: %v", err)
				}
				request := r.Method + " " + r.URL.EscapedPath()
				mu.Lock()
				seen = append(seen, request)
				mu.Unlock()
				if request == cutAt {
					cancel()
					select {
					case <-r.Context().Done():
					case <-time.After(10 * time.Second):
						t.Errorf("Setup: the request %s outlived its cancelled context by ten seconds", request)
					}
					return
				}
				answer, ok := ownAnswer(own, product.root, r.Method, r.URL.EscapedPath())
				if !ok {
					answer, _ = foreignAnswer(foreign, r.Method, r.URL.EscapedPath())
				}
				for k, v := range answer.Headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(answer.Status)
				if _, err := w.Write(answer.payload()); err != nil {
					t.Errorf("Setup: writing %s: %v", request, err)
				}
			}))
			t.Cleanup(srv.Close)

			core, family, err := families.Open(ctx, forgeapi.Connection{WebBaseURL: srv.URL}, openOptions(srv)...)
			mu.Lock()
			defer mu.Unlock()
			if !slices.Contains(seen, cutAt) {
				t.Fatalf("Setup: families.Open on %s sent %v, never %s", name, seen, cutAt)
			}
			if !errors.Is(err, context.Canceled) {
				t.Errorf("families.Open on %s cancelled at %s = error %v, want %v", name, cutAt, err, context.Canceled)
			}
			if core != nil || family != forgeapi.FamilyUnknown {
				t.Errorf("families.Open on %s cancelled at %s = (%T, %v), want no client and %v", name, cutAt, core, family, forgeapi.FamilyUnknown)
			}
		})
	}
}

// Each family's own connection read answers the context's sentinel where the
// context ends in its later setup read, called directly as through the factory, and
// holds nothing that read would have decided: the next call on a live context asks
// that read again.
func TestEachFamilysConnectionReadAnswersTheContextsSentinelWhereItEndsInALaterRead(t *testing.T) {
	for name, cutAt := range map[string]string{
		"GitHub":  "GET /api/v3/versions",
		"GitLab":  "POST /api/graphql",
		"Gitea":   "GET /api/v1/settings/api",
		"Forgejo": "GET /api/v1/settings/api",
	} {
		t.Run(name, func(t *testing.T) {
			product := detectedProducts[name]
			own := connectionRead(t, product.dir)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var mu sync.Mutex
			var seen []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, err := io.Copy(io.Discard, io.LimitReader(r.Body, 1<<20)); err != nil {
					t.Errorf("Setup: reading the request body: %v", err)
				}
				request := r.Method + " " + r.URL.EscapedPath()
				mu.Lock()
				seen = append(seen, request)
				first := slices.Index(seen, request) == len(seen)-1
				mu.Unlock()
				if request == cutAt && first {
					cancel()
					select {
					case <-r.Context().Done():
					case <-time.After(10 * time.Second):
						t.Errorf("Setup: the request %s outlived its cancelled context by ten seconds", request)
					}
					return
				}
				answer, ok := ownAnswer(own, product.root, r.Method, r.URL.EscapedPath())
				if !ok {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				for k, v := range answer.Headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(answer.Status)
				if _, err := w.Write(answer.payload()); err != nil {
					t.Errorf("Setup: writing %s: %v", request, err)
				}
			}))
			t.Cleanup(srv.Close)
			conn := forgeapi.Connection{WebBaseURL: srv.URL}
			var client forgeapi.Capabilities
			var err error
			switch product.family {
			case forgeapi.FamilyGitHub:
				client, err = github.New(conn, openOptions(srv)...)
			case forgeapi.FamilyGitLab:
				client, err = gitlab.New(conn, openOptions(srv)...)
			default:
				client, err = gitea.New(conn, openOptions(srv)...)
			}
			if err != nil {
				t.Fatalf("Setup: %s's own client: %v", name, err)
			}

			_, err = client.ConnectionCaps(ctx)
			if !errors.Is(err, context.Canceled) {
				t.Errorf("%s ConnectionCaps cancelled at %s = error %v, want %v", name, cutAt, err, context.Canceled)
			}
			if _, err := client.ConnectionCaps(t.Context()); err != nil {
				t.Errorf("%s ConnectionCaps on a live context after one cancelled at %s = error %v, want the capabilities", name, cutAt, err)
			}
			mu.Lock()
			defer mu.Unlock()
			asked := 0
			for _, request := range seen {
				if request == cutAt {
					asked++
				}
			}
			if asked != 2 {
				t.Errorf("%s's two connection reads sent %s %d time(s), want 2: the first call cut it, so nothing it would have decided is held: %v", name, cutAt, asked, seen)
			}
		})
	}
}

// Once the caller's context has ended, no later family is asked: nothing is sent,
// and no later family's client records an exchange the walk never made, so the
// caller's log names the read the context ended in and nothing after it.
func TestOpenAsksNoLaterFamilyOnceTheCallersContextEnded(t *testing.T) {
	for name, test := range map[string]struct {
		cutAt string
		cut   string
		later []string
	}{
		"in_GitLabs_read": {cutAt: "/api/v4/metadata", cut: "gitlab", later: []string{"github", "gitea"}},
		"in_GitHubs_read": {cutAt: "/api/v3/meta", cut: "github", later: []string{"gitea"}},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if path := r.URL.EscapedPath(); path == test.cutAt {
					cancel()
					select {
					case <-r.Context().Done():
					case <-time.After(10 * time.Second):
						t.Errorf("Setup: the request to %s outlived its cancelled context by ten seconds", path)
					}
					return
				}
				w.WriteHeader(http.StatusNotFound)
			}))
			t.Cleanup(srv.Close)
			var records bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&records, &slog.HandlerOptions{Level: slog.LevelDebug}))

			_, _, err := families.Open(ctx, forgeapi.Connection{WebBaseURL: srv.URL}, append(openOptions(srv), forgeapi.WithLogger(logger))...)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("families.Open cancelled at %s = error %v, want %v", test.cutAt, err, context.Canceled)
			}
			if !strings.Contains(records.String(), `"family":"`+test.cut+`"`) {
				t.Fatalf("Setup: the log names no exchange of the %s family the context ended in:\n%s", test.cut, records.String())
			}
			for _, family := range test.later {
				if strings.Contains(records.String(), `"family":"`+family+`"`) {
					t.Errorf("families.Open cancelled at %s logged an exchange of the %s family, asked after the caller's context ended:\n%s", test.cutAt, family, records.String())
				}
			}
		})
	}
}
