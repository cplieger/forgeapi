package creds

import (
	"context"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/forgeapi/v2/internal/githubroot"
	"github.com/cplieger/forgeapi/v2/internal/transport"
)

// The waits an App source keeps between requests to GitHub.
const (
	// replicationDelay is how long a fresh installation token may be refused
	// before GitHub's replicas know it; GitHub's own SDK retries a 401 inside
	// it (octokit auth-app, sendRequestWithRetries):
	// https://github.com/octokit/auth-app.js/blob/main/src/hook.ts
	replicationDelay = 5 * time.Second
	// earlyRejection is how young a refused token must be for a fresh mint to
	// be unlikely to cure the refusal, so the source waits instead of minting.
	earlyRejection = time.Minute
	// reprobeFirst and reprobeCeiling bound the wait after a terminal answer,
	// or an early rejection, before the next request: doubling from the first
	// to the ceiling, so a fix made on GitHub is found without hammering it.
	reprobeFirst   = 5 * time.Minute
	reprobeCeiling = time.Hour
	// limitFirst and limitCeiling bound the wait after a rate limit that states
	// none, doubling per consecutive one, as GitHub's REST best practices ask:
	// https://docs.github.com/en/rest/using-the-rest-api/best-practices-for-using-the-rest-api#handle-rate-limit-errors-appropriately
	limitFirst   = time.Minute
	limitCeiling = 15 * time.Minute
	// statedCeiling bounds a wait GitHub states, an hour being the primary
	// limit's own window, so a malformed header cannot park the source longer.
	statedCeiling = time.Hour
	// retryBase is the first pause between two attempts of one mint.
	retryBase = time.Second
)

// appAnswers are the refusals an App source reads itself, because each one
// decides its standing; every other non-2xx is the transport's to map.
var appAnswers = []int{
	http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
	http.StatusUnprocessableEntity, http.StatusTooManyRequests,
}

// AppSourceConfig is what only an App source has: which installation, and
// what its tokens may do. A zero Grant is refused: a consumer names the
// permissions it needs, or asks for [FullGrant] explicitly.
type AppSourceConfig struct {
	Installation Installation
	Grant        AppGrant
}

// AppSource is the [forgeapi.CredentialSource] of one GitHub App installation.
// It mints an installation token on first use, hands it out until the refresh
// lead before its expiry, then mints again under one owner while other callers
// wait, bounded by their own contexts. A refusal only a human can repair reads
// [forgeapi.CredReconnectRequired], re-probed after 5 minutes doubling to an
// hour; a rate limit stops every request for its wait; an unclassified refusal
// waits a minute, doubling to 15; a 5xx, a lost connection or an unreadable
// answer is retried within the budget. It is safe for concurrent use.
type AppSource struct {
	owner    *owner
	ended    *forgeapi.Error
	cached   issued
	instance string
	renew    renewal
	conn     forgeapi.Connection
	wait     hold
	install  Installation
	grant    AppGrant
	set      forgeapi.Settings
	mu       sync.Mutex
	id       int64
	endedN   int
	limitN   int
	refusedN int
	discardN int
}

var _ forgeapi.CredentialSource = (*AppSource)(nil)

// issued is one minted token. Its lifetime is measured from sent, the instant
// its request was sent, because GitHub states its expiry and no issue time; its
// age, for a 401 that soon after the mint, from answered, the instant GitHub's
// answer arrived. The token is held as a [Secret], so a printed source shows
// none of it.
type issued struct {
	sent     time.Time
	answered time.Time
	expiry   time.Time
	token    Secret
}

// hold is the instant before which the source sends nothing, and what a call
// answers until then. Its kind and message are read only while no terminal
// refusal stands, which answers in their place.
type hold struct {
	until   time.Time
	message string
	kind    forgeapi.ErrorKind
}

// NewAppSource builds the source for the installation cfg names, signing with
// key and minting through conn as a client would, with opts' lead, logger,
// counters and budget; it copies the grant and sends nothing. It refuses what
// [NewStoredSource] refuses, a nil key with [forgeapi.CodeAppKeyInvalid], a
// config it cannot send with [forgeapi.CodeAppGrantInvalid], and a plaintext
// base with [forgeapi.CodePlaintextRefused] even under [forgeapi.WithPlaintextHTTP].
//
//nolint:gocritic // hugeParam: the connection by value is the published signature's own parameter
func NewAppSource(conn forgeapi.Connection, key *AppKey, cfg AppSourceConfig, opts ...forgeapi.Option) (*AppSource, error) {
	set := forgeapi.Resolve(opts...)
	renew, err := newRenewal(&set)
	if err != nil {
		return nil, err
	}
	if key == nil {
		return nil, appKeyInvalid("the App source has no App key")
	}
	if err = cfg.Installation.check(); err != nil {
		return nil, err
	}
	grant, err := cfg.Grant.checked()
	if err != nil {
		return nil, err
	}
	if plaintext(conn.WebBaseURL) || plaintext(conn.APIBaseURL) {
		return nil, transport.Local(forgeapi.CodePlaintextRefused, "a GitHub App source never speaks plaintext HTTP, because its App JWT is App-wide")
	}
	set.PlaintextHTTP = false
	set.Credential = appJWT{key: key}
	set.ReadCacheBytes = 0
	s := &AppSource{conn: conn, install: cfg.Installation, owner: newOwner(), set: set, grant: grant, renew: renew, id: cfg.Installation.id}
	ep, err := s.open()
	if err != nil {
		return nil, err
	}
	s.instance = ep.APIBase().Host
	ep.Close()
	return s, nil
}

// Token answers the cached installation token, minting one first where none
// is cached or the cached one is inside the refresh lead. A mint that fails
// hands the cached token over while it has not expired, and fails the call
// once it has; a waiter fails with its own context's error.
func (s *AppSource) Token(ctx context.Context) (string, error) {
	if token, ok := s.handOut(time.Now()); ok {
		return token, nil
	}
	if err := s.owner.take(ctx); err != nil {
		return "", err
	}
	defer s.owner.give()
	now := time.Now()
	if token, ok := s.handOut(now); ok {
		return token, nil
	}
	if err := s.held("Token", now); err != nil {
		return s.unexpired(now, err)
	}
	s.owner.setBusy(true)
	next, err := s.renewOwned(ctx, "Token")
	s.owner.setBusy(false)
	if err != nil {
		return s.unexpired(time.Now(), err)
	}
	s.mu.Lock()
	s.cached = next
	s.mu.Unlock()
	return next.token.Reveal(), nil
}

// Kind answers [forgeapi.CredKindAppInstallation].
func (s *AppSource) Kind() forgeapi.CredKind { return forgeapi.CredKindAppInstallation }

// State answers where the source sits without sending anything: reconnect
// required after a terminal answer until a mint succeeds, valid while the
// cached token is outside the refresh lead, refreshing while a Token call
// mints, and refresh-due otherwise, a source that has minted nothing yet
// included.
func (s *AppSource) State() forgeapi.CredState {
	s.mu.Lock()
	p := phaseDue
	switch {
	case s.ended != nil:
		p = phaseEnded
	case !s.cached.token.IsZero() && s.renew.fresh(s.cached.sent, s.cached.expiry, time.Now()):
		p = phaseValid
	}
	s.mu.Unlock()
	return p.state(s.owner)
}

// Resolve answers the installation's ID, looking it up on first call, retried
// as a mint is, and from memory after, so a consumer can build a client that
// needs the ID before any token exists. A token request GitHub answers 404
// clears the remembered ID, and so does every re-probe after a terminal
// answer, so a reinstall's new ID is found; an [InstallationByID] selector
// keeps its own.
func (s *AppSource) Resolve(ctx context.Context) (int64, error) {
	if id, ok := s.known(); ok {
		return id, nil
	}
	if err := s.owner.take(ctx); err != nil {
		return 0, err
	}
	defer s.owner.give()
	if id, ok := s.known(); ok {
		return id, nil
	}
	if err := s.held("Resolve", time.Now()); err != nil {
		return 0, err
	}
	ep, err := s.open()
	if err != nil {
		return 0, err
	}
	defer ep.Close()
	var id int64
	f := s.retrying(ctx, ep, "Resolve", func(ctx context.Context) (f *failure) {
		id, f = s.installation(ctx, ep, "Resolve")
		return f
	})
	if f != nil && cancelled(ctx, f) {
		return 0, f.err
	}
	if f != nil {
		_, err = s.settle(f)
		return 0, err
	}
	return id, nil
}

// Mint always mints a new token and answers it with its expiry, for handing to
// a process that reads its token once. It does not replace the cached token.
// It fails at once, sending nothing, where Token would: after a terminal
// answer until the re-probe, and during a rate limit's wait.
func (s *AppSource) Mint(ctx context.Context) (InstallationToken, error) {
	if err := s.owner.take(ctx); err != nil {
		return InstallationToken{}, err
	}
	defer s.owner.give()
	if err := s.held("Mint", time.Now()); err != nil {
		return InstallationToken{}, err
	}
	next, err := s.renewOwned(ctx, "Mint")
	if err != nil {
		return InstallationToken{}, err
	}
	return InstallationToken{Token: next.token, Expiry: next.expiry}, nil
}

// Discard is called after a request that used token was answered 401. A
// cached token minted under 5 seconds earlier is kept and Discard answers
// true: the 401 is GitHub's replication delay, so the caller retries it.
// Otherwise it drops the cached token when it is still token and answers
// false, and the next Token mints; but a token dropped under a minute after
// its mint makes Token and Mint fail at once with [forgeapi.KindUnauthorized]
// and RetryAfter for 5 minutes, doubling to an hour per further early drop,
// until a token is handed out past its first minute.
func (s *AppSource) Discard(token string) (retry bool) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if token == "" || token != s.cached.token.Reveal() {
		return false
	}
	age := now.Sub(s.cached.answered)
	if age < replicationDelay {
		return true
	}
	if age < earlyRejection {
		s.extend(hold{
			until: now.Add(doubling(reprobeFirst, reprobeCeiling, s.discardN)), kind: forgeapi.KindUnauthorized,
			message: "GitHub rejected a token minted under a minute earlier, so the App source waits before minting again",
		})
		s.discardN++
	}
	s.cached = issued{}
	return false
}

// handOut answers the cached token while it is outside the refresh lead.
func (s *AppSource) handOut(now time.Time) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached.token.IsZero() || !s.renew.fresh(s.cached.sent, s.cached.expiry, now) {
		return "", false
	}
	s.settled(now)
	return s.cached.token.Reveal(), true
}

// unexpired answers the cached token in place of err while it has not expired.
func (s *AppSource) unexpired(now time.Time, err error) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached.token.IsZero() || !now.Before(s.cached.expiry) {
		return "", err
	}
	s.settled(now)
	return s.cached.token.Reveal(), nil
}

// settled ends the wait early drops build up once the cached token is handed
// out past its first minute, which no early rejection followed. It runs under
// s.mu.
func (s *AppSource) settled(now time.Time) {
	if now.Sub(s.cached.answered) >= earlyRejection {
		s.discardN = 0
	}
}

// known answers the remembered installation ID.
func (s *AppSource) known() (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.id, s.id != 0
}

// held is what a call answers before the instant the source sends nothing
// until: the terminal refusal while one stands, else the hold's own failure
// with the time left. It answers nil once that instant has passed.
func (s *AppSource) held(op string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !now.Before(s.wait.until) {
		return nil
	}
	if s.ended != nil {
		return copyOf(s.ended)
	}
	return &forgeapi.Error{
		Op: op, Family: forgeapi.FamilyGitHub, Kind: s.wait.kind,
		Retryable: transport.Retryable(s.wait.kind, 0), Message: s.wait.message,
		RetryAfter: s.wait.until.Sub(now),
	}
}

// extend moves the hold to h where h ends later, so no wait shortens another.
// It runs under s.mu.
func (s *AppSource) extend(h hold) {
	if h.until.After(s.wait.until) {
		s.wait = h
	}
}

// forget drops the remembered installation ID, which an [InstallationByID]
// selector never does. It runs under s.mu.
func (s *AppSource) forget() {
	if s.install.by != selectByID {
		s.id = 0
	}
}

// open builds one request core for the source's connection, GitHub's by
// construction.
func (s *AppSource) open() (*transport.Conn, error) {
	return transport.Open(&s.conn, &s.set, transport.Options{
		Mapper: appMapper, DeriveAPIBase: githubroot.API, Family: forgeapi.FamilyGitHub,
	})
}

// appMapper maps the answers [appAnswers] leaves to the transport: a server
// failure is worth another attempt, and anything else is unclassified.
func appMapper(_ string, status int, _ http.Header, _ string) (kind forgeapi.ErrorKind, code string) {
	if status >= http.StatusInternalServerError {
		return forgeapi.KindTransient, ""
	}
	return forgeapi.KindUpstream, ""
}

// plaintext reports whether a base URL names the http scheme.
func plaintext(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "http"
}

// doubling is first doubled step times, capped at ceiling.
func doubling(first, ceiling time.Duration, step int) time.Duration {
	wait := first
	for range step {
		if wait >= ceiling/2 {
			return ceiling
		}
		wait *= 2
	}
	return min(wait, ceiling)
}

// copyOf is a copy of e, so a caller holding a returned refusal never shares
// the one the source keeps.
func copyOf(e *forgeapi.Error) *forgeapi.Error {
	c := *e
	return &c
}

// appJWT is the credential every App source request carries: a fresh App JWT,
// which the transport redacts from any text GitHub answers.
type appJWT struct{ key *AppKey }

func (j appJWT) Token(ctx context.Context) (string, error) { return j.key.JWT(ctx) }
func (appJWT) Kind() forgeapi.CredKind                     { return forgeapi.CredKindUnknown }
func (appJWT) State() forgeapi.CredState                   { return forgeapi.CredUnknown }
