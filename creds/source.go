package creds

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"net/url"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/transport"
)

// standing is where a stored record sits, read from the record and its key's
// custody alone.
type standing int

const (
	// standValid hands the token over.
	standValid standing = iota
	// standDue refreshes first.
	standDue
	// standLapsed is a refresh token past its own expiry: terminal, and a
	// refresh outcome the first time it is found.
	standLapsed
	// standSpent is a record marked spent, in the store or in this process.
	standSpent
	// standRefused is a record marked reconnect-required, in the store or in
	// this process.
	standRefused
	// standGone is reconnect-required for every other reason.
	standGone
)

// ended is the refusal Token answers for a record standing at st, one of the
// terminal standings, with a message naming why.
func ended(rec *Record, st standing) error {
	switch st {
	case standLapsed:
		return reconnect(rec, 0, "the refresh token is past its own expiry")
	case standSpent:
		return reconnect(rec, 0, "the stored credential is spent: the instance rotated it and the pair it issued was lost")
	case standRefused:
		return reconnect(rec, 0, "the product refused a refresh of the stored credential")
	}
	return reconnect(rec, 0, "no stored credential can be handed over or renewed")
}

// maxTurns bounds how many times one Token call reads the record again after a
// rotation lost its compare-and-swap to a record saved during the refresh.
const maxTurns = 2

// Source is the [forgeapi.CredentialSource] one connection runs behind: the
// refresh state machine over the record a store holds under one key. A record is
// valid until the time left to its expiry is inside the refresh lead, then due,
// and the next Token refreshes it under the key's one owner in its store while
// every other caller for that key waits, bounded by its own context. It is safe
// for concurrent use.
type Source struct {
	store    Store
	logger   *slog.Logger
	counters forgeapi.Counters
	conn     forgeapi.Connection
	key      string
	set      forgeapi.Settings
	lead     time.Duration
}

var _ forgeapi.CredentialSource = (*Source)(nil)

// NewSource builds the source for the record under key in store, refreshing it
// through conn's trust material, proxy and address policy. It reads the refresh
// lead, the logger, the counters and the two per-connection postures from opts,
// sends nothing, and refuses a negative lead with [forgeapi.CodeBudgetInvalid] and
// a connection a client could not run with that connection's own refusal.
//
//nolint:gocritic // hugeParam: the connection by value is the published signature's own parameter
func NewSource(store Store, key string, conn forgeapi.Connection, opts ...forgeapi.Option) (*Source, error) {
	set := forgeapi.Resolve(opts...)
	if set.RefreshLead < 0 {
		return nil, transport.Local(forgeapi.CodeBudgetInvalid, "the refresh lead is negative")
	}
	ep, err := transport.OpenEndpoint(&conn, &set, forgeapi.FamilyUnknown)
	if err != nil {
		return nil, err
	}
	ep.Close()
	logger := set.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Source{
		store: store, logger: logger, counters: set.Counters, set: set,
		conn: conn, key: key, lead: set.RefreshLead,
	}, nil
}

// Token answers the stored token, refreshing it first where it is due. A refresh
// that fails before the instance issues anything hands the still-valid token over,
// and fails the call only once that token has expired. A refresh whose new pair
// could not be read, its body undecodable or lost on the way, fails the call and
// marks the record [UsabilitySpent], because the instance spent the stored pair. A
// refresh whose new pair the store could not take fails the call with the store's
// error and holds the pair: the next call for the key stores it before anything
// else, sending no refresh, and answers from it, unless the stored record is no
// longer the one the pair was rotated from. A waiter fails with its own context's
// error. Reconnect-required fails with [forgeapi.CodeReconnectRequired], and so
// does a record marked [UsabilitySpent] or [UsabilityReconnectRequired], with a
// message naming the mark. What a refresh learns is written to the record, and a
// mark the store could not take is written by the next call for the key whose
// write succeeds.
func (s *Source) Token(ctx context.Context) (string, error) {
	rot := s.store.rotation(s.key)
	rot.settle()
	if err := s.storeHeld(ctx, rot); err != nil {
		return "", err
	}
	for range maxTurns {
		rec, ok, err := s.store.Load(s.key)
		if err != nil {
			return "", err
		}
		switch st := s.standing(rot, &rec, ok, time.Now()); st {
		case standValid:
			return rec.Token, nil
		case standLapsed:
			s.lapse(rot, &rec)
			return "", ended(&rec, st)
		case standSpent, standRefused, standGone:
			return "", ended(&rec, st)
		}
		token, replaced, err := s.refresh(ctx, rot)
		if !replaced {
			return token, err
		}
	}
	return "", &forgeapi.Error{
		Kind: forgeapi.KindTransient, Retryable: true,
		Message: "the credential was replaced during each refresh of it",
	}
}

// Kind answers the stored record's kind, the unknown member where none is stored
// or the store cannot be read.
func (s *Source) Kind() forgeapi.CredKind {
	rec, ok, err := s.store.Load(s.key)
	if err != nil || !ok {
		return forgeapi.CredKindUnknown
	}
	return rec.Kind
}

// State answers where the stored record sits in the machine without sending
// anything, and [forgeapi.CredUnknown] where the store cannot be read. A record
// whose rotated pair is held for the next call to store is refresh-due, since the
// connection needs work from the library and not from a human.
func (s *Source) State() forgeapi.CredState {
	rec, ok, err := s.store.Load(s.key)
	if err != nil {
		return forgeapi.CredUnknown
	}
	rot := s.store.rotation(s.key)
	switch s.standing(rot, &rec, ok, time.Now()) {
	case standValid:
		return forgeapi.CredValid
	case standDue:
		if rot.refreshing() {
			return forgeapi.CredRefreshing
		}
		return forgeapi.CredRefreshDue
	}
	return forgeapi.CredReconnectRequired
}

// standing reads a record against the machine. A marked record, by the store or by
// this process, is never handed over or refreshed, whatever its kind and expiry. A
// static token is valid until any expiry it carries; a rotating one is due inside
// the lead, and with nothing to refresh it, valid until it expires. A rotating
// record states its expiry by its kind's definition, so one stating none is not a
// token that never expires: it reads as reconnect-required, as an unknown kind does.
// A record whose rotated pair the key's custody holds is due whatever its expiry,
// because the next refresh stores that pair in place of sending one.
func (s *Source) standing(rot *rotation, rec *Record, ok bool, now time.Time) standing {
	mark := cmp.Or(rec.Usability, rot.markFor(rec))
	switch {
	case !ok:
		return standGone
	case rot.holds(rec):
		return standDue
	case mark == UsabilitySpent:
		return standSpent
	case mark != UsabilityUnmarked:
		return standRefused
	case rec.Kind == forgeapi.CredKindStaticPAT:
		if expired(rec, now) {
			return standGone
		}
		return standValid
	case rec.Kind != forgeapi.CredKindRotatingOAuth, rec.Expiry.IsZero():
		return standGone
	case rec.Expiry.Sub(now) > s.leadFor(rec):
		return standValid
	}
	if _, refreshable := endpointsOf(rec.Family); !refreshable || rec.RefreshToken == "" {
		if expired(rec, now) {
			return standGone
		}
		return standValid
	}
	if !rec.RefreshExpiry.IsZero() && !now.Before(rec.RefreshExpiry) {
		return standLapsed
	}
	return standDue
}

// leadFor is the refresh lead for one record: the option's value, or half the
// lifetime the record was issued with where that is shorter, so a short lifetime
// cannot make every fresh token due on arrival.
func (s *Source) leadFor(rec *Record) time.Duration {
	lifetime := rec.Expiry.Sub(rec.Issued)
	if rec.Issued.IsZero() || lifetime <= 0 {
		return s.lead
	}
	return min(s.lead, lifetime/2)
}

// refresh rotates the record under the key's single owner. It reads the record
// again once it owns the key, because the owner before it may have rotated it
// already. replaced reports a rotation that lost its compare-and-swap to a record
// saved while it was in flight, or a held pair stored in place of a refresh, either
// of which the caller reads afresh.
func (s *Source) refresh(ctx context.Context, rot *rotation) (token string, replaced bool, err error) {
	if waitErr := rot.take(ctx); waitErr != nil {
		return "", false, waitErr
	}
	defer rot.give()
	// A call this one waited behind may have held a pair it could not store, which
	// replaced the stored refresh token, so that pair is stored rather than a
	// refresh sent with a spent token.
	if held, heldErr := s.storeHeldOwned(rot); held {
		if heldErr != nil {
			return "", false, heldErr
		}
		return "", true, nil
	}
	cur, ok, err := s.store.Load(s.key)
	if err != nil {
		return "", false, err
	}
	switch st := s.standing(rot, &cur, ok, time.Now()); st {
	case standValid:
		return cur.Token, false, nil
	case standLapsed:
		s.lapse(rot, &cur)
		return "", false, ended(&cur, st)
	case standSpent, standRefused, standGone:
		return "", false, ended(&cur, st)
	}

	rot.setBusy(true)
	next, status, v, err := s.exchange(ctx, &cur)
	rot.setBusy(false)
	if v != rotated && s.replaced(&cur) {
		return "", true, nil
	}
	switch v {
	case refused:
		s.mark(rot, &cur, UsabilityReconnectRequired, status, "the refresh was answered terminally")
		return "", false, reconnect(&cur, status, "the product refused the refresh token")
	case unspent:
		s.outcome(&cur, forgeapi.CredRefreshDue, status, "the refresh failed")
		return keep(&cur, err)
	case spent:
		s.mark(rot, &cur, UsabilitySpent, status, "the refresh answered a token that could not be read")
		return "", false, err
	case misaddressed:
		s.mark(rot, &cur, UsabilityReconnectRequired, 0, "the record's web base URL is not an origin and a path")
		return "", false, reconnect(&cur, 0, "the record's web base URL is not an origin and a path")
	}
	return s.storeRotated(rot, &cur, &next, status)
}

// storeRotated stores the pair a rotation of cur issued, by compare-and-swap over
// cur, and answers its token. A write that fails outright fails the call with the
// store's error and holds the pair for the next call to store, marking nothing,
// since the pair is live upstream and in this process's hands; one that reached the
// file unproved fails the call too, with the pair at the name. A record saved while
// the rotation was in flight keeps its place, and the caller reads it afresh.
func (s *Source) storeRotated(rot *rotation, cur, next *Record, status int) (token string, replaced bool, err error) {
	swapped, err := rot.swap(cur, next)
	switch {
	case errors.Is(err, errNotDurable):
		s.outcome(cur, forgeapi.CredRefreshDue, status, "the rotated credential was not proved durable")
		return "", false, err
	case err != nil:
		rot.hold(cur, next)
		s.outcome(cur, forgeapi.CredRefreshDue, status, "the rotated credential could not be stored")
		return "", false, err
	}
	s.outcome(cur, forgeapi.CredValid, status, "")
	if !swapped {
		return "", true, nil
	}
	return next.Token, false, nil
}

// storeHeld is a call's first act while the key's custody holds a rotated pair
// whose write failed: it takes the key's owner, bounded by ctx, and stores the pair
// before anything else, so no refresh is sent with the refresh token that pair
// replaced, and a pair whose base record is no longer stored is dropped at once.
func (s *Source) storeHeld(ctx context.Context, rot *rotation) error {
	if _, _, ok := rot.held(); !ok {
		return nil
	}
	if err := rot.take(ctx); err != nil {
		return err
	}
	defer rot.give()
	_, err := s.storeHeldOwned(rot)
	return err
}

// storeHeldOwned stores the held pair, under the key's owner, by compare-and-swap
// against the record its rotation started from, and reports whether a pair was
// held. A write that succeeds makes the pair the stored record, unmarked; one that
// fails answers the store's error and keeps the pair held; and a stored record that
// is no longer the one the rotation started from drops the pair, which belongs to a
// record the key no longer holds. A write that reached the file unproved has the
// pair at the name, so it drops the pair too and answers its error, as a rotation's
// own write does.
func (s *Source) storeHeldOwned(rot *rotation) (bool, error) {
	base, pair, ok := rot.held()
	if !ok {
		return false, nil
	}
	swapped, err := rot.swap(&base, &pair)
	switch {
	case errors.Is(err, errNotDurable):
		rot.drop()
		s.outcome(&base, forgeapi.CredRefreshDue, 0, "the rotated credential was not proved durable")
		return true, err
	case err != nil:
		return true, err
	}
	rot.drop()
	if swapped {
		s.outcome(&base, forgeapi.CredValid, 0, "")
	}
	return true, nil
}

// replaced reports whether the stored record is no longer cur, which is a connect
// or a reconnect saved while cur's refresh was in flight. A refresh that did not
// rotate then says nothing about the connection, and the caller reads it afresh.
func (s *Source) replaced(cur *Record) bool {
	stored, ok, err := s.store.Load(s.key)
	return err == nil && ok && !same(&stored, cur)
}

// verdict is how one refresh exchange ended, which decides what the stored token is
// still worth.
type verdict int

const (
	// rotated: the instance issued a new pair, and next holds it.
	rotated verdict = iota
	// refused: a terminal answer, so the stored pair is dead and nothing replaces it.
	refused
	// unspent: a failure before the instance issued anything, so the stored pair
	// stands.
	unspent
	// spent: the instance answered a success whose pair this package could not
	// read, its body undecodable, lost on the way or carrying no token this
	// package admits, so the stored pair is dead upstream while the record still
	// holds it. A success that may have been a refusal cut short reads as spent
	// too, since handing a dead token over is the worse of the two errors.
	spent
	// misaddressed: the record's own web base is refused for its form (a base
	// that is not an origin and a path) before anything is sent, so no retry
	// reaches the instance and the record needs a reconnect, exactly as a
	// terminal answer does.
	misaddressed
)

// exchange sends one refresh to the record's own web base and reads the answer.
// The request runs on a context the caller cannot cancel, bounded by the
// connection's operation deadline: a refresh the instance has processed has
// already spent the old pair, and the answer is the only copy of the new one.
func (s *Source) exchange(ctx context.Context, cur *Record) (next Record, status int, v verdict, err error) {
	ends, _ := endpointsOf(cur.Family)
	conn := s.conn
	conn.WebBaseURL = cur.WebBaseURL
	ep, err := transport.OpenEndpoint(&conn, &s.set, cur.Family)
	if fe, ok := errors.AsType[*forgeapi.Error](err); ok && fe.Code == forgeapi.CodeConnectionInvalid {
		return Record{}, 0, misaddressed, err
	}
	if err != nil {
		return Record{}, 0, unspent, err
	}
	defer ep.Close()
	form := url.Values{formClientID: {cur.ClientID}, "grant_type": {refreshGrantType}, "refresh_token": {cur.RefreshToken}}
	a, status, err := post(context.WithoutCancel(ctx), ep, "Token", ends.grant, form)
	switch {
	case err != nil && status != 0 && a.succeeded(status):
		// A success whose body did not arrive: the instance issued a pair, which
		// spent the stored one, and none of it can be read.
		return Record{}, status, spent, tokenless(cur.Family, status)
	case err != nil:
		return Record{}, status, unspent, err
	case a.granted(status) && !a.readable():
		return Record{}, status, spent, unreadable(cur.Family, status)
	case a.granted(status):
		return minted(cur, &a, time.Now()), status, rotated, nil
	case a.succeeded(status):
		return Record{}, status, spent, tokenless(cur.Family, status)
	case a.Error == errBadRefreshToken, a.Error == errInvalidGrant:
		return Record{}, status, refused, nil
	}
	return Record{}, status, unspent, unanswered(cur.Family, status, &a)
}

// keep is what a refresh that failed before the instance issued anything hands
// over: the stored token while it has not expired, and the failure once it has.
func keep(cur *Record, err error) (token string, replaced bool, failure error) {
	if time.Now().Before(cur.Expiry) {
		return cur.Token, false, nil
	}
	return "", false, err
}

// lapse records a refresh token found past its own expiry, counting the outcome
// the first time it is found for that record. It writes no mark, since every
// reader derives the lapse from the record.
func (s *Source) lapse(rot *rotation, rec *Record) {
	if rot.end(rec, UsabilityUnmarked, false) {
		s.outcome(rec, forgeapi.CredReconnectRequired, 0, "the refresh token is past its own expiry")
	}
}

// mark records a terminal refresh outcome for cur and writes its mark to the
// store, by compare-and-swap over cur, so a record saved since is never marked.
// Where the store cannot take it, the mark stays owed in the key's custody.
func (s *Source) mark(rot *rotation, cur *Record, mark Usability, status int, failure string) {
	rot.end(cur, mark, true)
	rot.settle()
	s.outcome(cur, forgeapi.CredReconnectRequired, status, failure)
}

// outcome counts and records one refresh outcome by the state it reached. The
// line names the family, the instance and the status, never a token.
func (s *Source) outcome(rec *Record, reached forgeapi.CredState, status int, failure string) {
	if s.counters.RefreshOutcome != nil {
		s.counters.RefreshOutcome(rec.Family, reached)
	}
	level := slog.LevelInfo
	if reached != forgeapi.CredValid {
		level = slog.LevelWarn
	}
	s.logger.Log(context.Background(), level, "forgeapi credential refresh",
		"family", rec.Family.String(),
		"instance", hostOf(rec.WebBaseURL),
		"state", reached.String(),
		"status", status,
		"failure", failure,
	)
}

// reconnect is the refusal Token answers once the machine is reconnect-required.
// status is the instance's where a terminal answer reached it, and zero where the
// verdict was the stored record's alone.
func reconnect(rec *Record, status int, message string) error {
	return &forgeapi.Error{
		Code: forgeapi.CodeReconnectRequired, Family: rec.Family, Status: status,
		Kind: forgeapi.KindUnauthorized, Message: message,
	}
}

// hostOf is a web base URL's host, for a log line, and empty where it does not
// parse.
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}
