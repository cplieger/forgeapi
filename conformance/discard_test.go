package conformance

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/forgeapi/v2/internal/spec"
)

// discardingCredential is a credential source with Discard that never asks for
// a resend, recording every token it is told about.
type discardingCredential struct {
	told []string
	mu   sync.Mutex
}

func (*discardingCredential) Token(context.Context) (string, error) {
	return "discard-placeholder", nil
}
func (*discardingCredential) Kind() forgeapi.CredKind   { return forgeapi.CredKindAppInstallation }
func (*discardingCredential) State() forgeapi.CredState { return forgeapi.CredValid }

func (c *discardingCredential) Discard(token string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.told = append(c.told, token)
	return false
}

func (c *discardingCredential) discarded() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.told)
}

// statusWire answers every request with status and counts them.
type statusWire struct {
	sent   atomic.Int64
	status int
}

func (w *statusWire) RoundTrip(r *http.Request) (*http.Response, error) {
	w.sent.Add(1)
	return &http.Response{
		StatusCode: w.status, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"message":"refused"}`)), Request: r,
	}, nil
}

// whoamiOver calls Whoami on p's client over wire with source as its credential.
func whoamiOver(t *testing.T, p spec.Product, wire http.RoundTripper, source forgeapi.CredentialSource) error {
	t.Helper()
	client, err := newClient(p, forgeapi.Connection{WebBaseURL: "https://forge.example"},
		forgeapi.WithWireTransport(wire),
		forgeapi.WithCredentialSource(source),
		forgeapi.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)
	if err != nil {
		t.Fatalf("Setup: the %s client: %v", p, err)
	}
	_, err = client.(forgeapi.Identity).Whoami(t.Context())
	return err
}

func TestEveryFamilyTellsDiscardOfA401(t *testing.T) {
	for _, p := range []spec.Product{spec.GitHub, spec.GitLab, spec.Gitea} {
		t.Run(string(p), func(t *testing.T) {
			t.Run("a_401", func(t *testing.T) {
				source, wire := &discardingCredential{}, &statusWire{status: http.StatusUnauthorized}
				if err := whoamiOver(t, p, wire, source); err == nil {
					t.Error("Whoami answered 401 = nil, want the 401's error")
				}
				if got := source.discarded(); !slices.Equal(got, []string{"discard-placeholder"}) {
					t.Errorf("Discard was told %q, want the token the request carried, once", got)
				}
				if got := wire.sent.Load(); got != 1 {
					t.Errorf("the wire saw %d request(s), want 1: a Discard answering false asks for no resend", got)
				}
			})
			t.Run("a_source_without_discard", func(t *testing.T) {
				wire := &statusWire{status: http.StatusUnauthorized}
				if err := whoamiOver(t, p, wire, staticCredential("conformance-placeholder")); err == nil {
					t.Error("Whoami answered 401 = nil, want the 401's error")
				}
				if got := wire.sent.Load(); got != 1 {
					t.Errorf("the wire saw %d request(s), want 1", got)
				}
			})
			for _, status := range []int{http.StatusForbidden, http.StatusInternalServerError} {
				t.Run(strings.ReplaceAll(http.StatusText(status), " ", "_"), func(t *testing.T) {
					source := &discardingCredential{}
					_ = whoamiOver(t, p, &statusWire{status: status}, source)
					if got := source.discarded(); len(got) != 0 {
						t.Errorf("a %d told Discard %q, want nothing", status, got)
					}
				})
			}
		})
	}
}
