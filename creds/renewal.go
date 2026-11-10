package creds

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/forgeapi/v2/internal/transport"
)

// renewal is what every source in this package shares above the question of
// where its token is kept: the refresh lead, and where an outcome is logged and
// counted.
type renewal struct {
	logger   *slog.Logger
	counters forgeapi.Counters
	lead     time.Duration
}

// newRenewal reads the refresh lead, the logger and the counters from set, and
// refuses a negative lead with [forgeapi.CodeBudgetInvalid].
func newRenewal(set *forgeapi.Settings) (renewal, error) {
	if set.RefreshLead < 0 {
		return renewal{}, transport.Local(forgeapi.CodeBudgetInvalid, "the refresh lead is negative")
	}
	logger := set.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return renewal{logger: logger, counters: set.Counters, lead: set.RefreshLead}, nil
}

// leadFor is the refresh lead for a token issued at issued that expires at
// expiry: the option's value, or half the token's lifetime where that is
// shorter, so a short lifetime cannot make every fresh token due on arrival.
func (r *renewal) leadFor(issued, expiry time.Time) time.Duration {
	lifetime := expiry.Sub(issued)
	if issued.IsZero() || lifetime <= 0 {
		return r.lead
	}
	return min(r.lead, lifetime/2)
}

// fresh reports whether a token issued at issued that expires at expiry is
// still outside its refresh lead at now.
func (r *renewal) fresh(issued, expiry, now time.Time) bool {
	return expiry.Sub(now) > r.leadFor(issued, expiry)
}

// outcome counts and logs one renewal outcome by the state it reached. The line
// names the family, the instance and the status, never a token.
func (r *renewal) outcome(family forgeapi.Family, instance string, reached forgeapi.CredState, status int, failure string) {
	if r.counters.RefreshOutcome != nil {
		r.counters.RefreshOutcome(family, reached)
	}
	level := slog.LevelInfo
	if reached != forgeapi.CredValid {
		level = slog.LevelWarn
	}
	r.logger.Log(context.Background(), level, "forgeapi credential refresh",
		"family", family.String(),
		"instance", instance,
		"state", reached.String(),
		"status", status,
		"failure", failure,
	)
}

// phase is where a credential sits against its expiry and the refresh lead,
// the part of a source's own standing that decides the state it reports.
type phase int

const (
	// phaseValid hands the token over.
	phaseValid phase = iota
	// phaseDue renews first.
	phaseDue
	// phaseEnded needs a human before anything is handed over again.
	phaseEnded
)

// state is the [forgeapi.CredState] a credential in phase p reports, refreshing
// where its owner has a renewal in flight.
func (p phase) state(o *owner) forgeapi.CredState {
	switch p {
	case phaseValid:
		return forgeapi.CredValid
	case phaseDue:
		if o.renewing() {
			return forgeapi.CredRefreshing
		}
		return forgeapi.CredRefreshDue
	}
	return forgeapi.CredReconnectRequired
}

// owner is one credential's single renewer: a renewal runs only while its
// caller holds the owner, and every other caller waits, bounded by its own
// context.
type owner struct {
	slot chan struct{}
	mu   sync.Mutex
	busy bool
}

func newOwner() *owner { return &owner{slot: make(chan struct{}, 1)} }

// take makes the caller the credential's owner, waiting no longer than ctx
// allows.
func (o *owner) take(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case o.slot <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (o *owner) give() { <-o.slot }

// setBusy records, under the owner, whether a renewal has left the process,
// which is what [phase.state] reads as refreshing.
func (o *owner) setBusy(busy bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.busy = busy
}

func (o *owner) renewing() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.busy
}
