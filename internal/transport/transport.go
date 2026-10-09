// Package transport is forgeapi's one request core: addressing, auth injection,
// retry, the SSRF hook, trust, decode bounds, pagination, budget, error mapping
// and the per-request log line.
//
// It is under internal/ because its whole job is to hold the three dependency
// types the exported surface bans from a signature, so an importable spelling
// would put a dependency's next major on that surface. The path decides it
// mechanically: the surface generator skips any directory named internal.
//
// A family package builds one [Conn] per connection and speaks to it in this
// package's own vocabulary. Nothing here reaches a consumer.
package transport

import (
	"bytes"
	"cmp"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/httpx/v5"
	"github.com/cplieger/jsoncap/v2"
	"github.com/cplieger/runesafe/v2"
	"github.com/cplieger/ssrf/v4"
	"github.com/cplieger/urlform"
)

const (
	// maxBodyBytes bounds one response body before it is decoded, so a hostile
	// or misconfigured instance cannot cost the process more than this.
	maxBodyBytes = 8 << 20

	// maxDecodeElements bounds what one decode may allocate, counted in JSON
	// elements rather than in bytes, which is what makes a deeply nested or
	// array-heavy body bounded as well as a large one.
	maxDecodeElements = 500_000

	// maxMessageBytes bounds the upstream message this library carries on an
	// error after sanitizing it.
	maxMessageBytes = 512

	// dialTimeout, handshakeTimeout and headerTimeout are the per-phase bounds.
	// They sum to more than one operation's budget on purpose: on an instance
	// that stalls at every phase the operation's context deadline fires first,
	// which is what makes the retry count an upper bound rather than a
	// behaviour.
	dialTimeout      = 10 * time.Second
	handshakeTimeout = 10 * time.Second
	headerTimeout    = 15 * time.Second

	// userAgent is what this library sends. It names the library and nothing
	// about the host it runs on.
	userAgent = "forgeapi/1.0"

	// schemeHTTPS and schemeHTTP are the two schemes a connection may speak.
	schemeHTTPS = "https"
	schemeHTTP  = "http"
)

// errorMapper is how a family states its own error mapping. Mapping is by family plus
// operation plus status plus selected headers, never by status alone, so the
// family that knows those cells supplies the function and this package owns
// everything around it.
//
// bodyError is the body's machine-readable error member, RFC 6750's `error`,
// empty where the body carries none. It is a code the instance states, so a
// family may map on it; the body's prose never reaches a mapper.
type errorMapper func(op string, status int, header http.Header, bodyError string) (forgeapi.ErrorKind, string)

// budgetSignal is how a family reads its product's budget signal off a response. It
// returns the remaining units, the reset instant and whether the response carried
// a signal at all; a family whose product sends none returns false and the
// governor then reports the neutral value.
type budgetSignal func(header http.Header) (remaining int, reset time.Time, ok bool)

// Conn is one connection's request core: one wire transport, two clients over it,
// and its governor.
//
// There are two clients because a transport configuration is consumed once per
// round tripper and carries no per-request override, so the read path and the
// mutation path cannot share one: the read path retries non-idempotent requests
// and the mutation path takes exactly one attempt, because a replayed merge is a
// duplicate no interval undoes.
type Conn struct { //nolint:govet // fieldalignment: the field order is this type's own reading order, the logger and the credential first, and no value of it is allocated per request
	logger     *slog.Logger
	credential forgeapi.CredentialSource
	mapper     errorMapper
	signal     budgetSignal
	apiBase    *url.URL
	web        *url.URL
	wire       *http.Transport
	read       *http.Client
	mutate     *http.Client
	gov        *governor
	counters   forgeapi.Counters
	headers    []forgeapi.Header
	budget     forgeapi.Budget
	readSlots  chan struct{}
	writeSlots chan struct{}
	family     forgeapi.Family
	mutations  bool
	mu         sync.Mutex
	lastMutate time.Time
}

// Options is what a family hands [Open]: the connection, the resolved settings,
// the family this client serves, the API base to use when the connection names
// none, and the two per-family functions this package cannot own.
//
// Clock is the time source the governor's ROLLING INTERVAL is measured on,
// [time.Now] where it is nil. It exists because the interval is the one bound
// nothing but the clock can move: a caller cannot ask for the next window, so a
// test of the rotation across several windows either waits out a real interval,
// which makes its verdict a race against however loaded the machine is, or drives
// the clock. Nothing else in this package reads it, and no exported surface carries
// it, so a consumer cannot substitute a clock for the pacing this governor exists
// to do.
type Options struct {
	Mapper        errorMapper
	Signal        budgetSignal
	DeriveAPIBase func(web *url.URL) string
	Clock         func() time.Time
	Family        forgeapi.Family
}

// Open validates one connection and builds its request core. It performs no I/O:
// what it refuses is a client it could not run honestly, and each refusal carries
// its own code so a consumer names the remedy rather than showing a warning.
func Open(conn *forgeapi.Connection, set *forgeapi.Settings, opts Options) (*Conn, error) {
	if set.Credential == nil {
		return nil, local(forgeapi.CodeAnonymousRefused, "no credential source: this client would send anonymous requests")
	}
	if err := checkBudget(set); err != nil {
		return nil, err
	}
	if err := forgeapi.ValidateCursor(forgeapi.Cursor(set.RotationCursor)); err != nil {
		return nil, local(forgeapi.CodeCursorInvalid, "rotation cursor is not well formed")
	}
	web, err := parseBase(conn.WebBaseURL, set)
	if err != nil {
		return nil, err
	}
	if headerErr := checkHeaders(conn.Headers); headerErr != nil {
		return nil, headerErr
	}
	apiBase, err := resolveAPIBase(conn, web, set, opts)
	if err != nil {
		return nil, err
	}
	tlsCfg, err := buildTLS(conn)
	if err != nil {
		return nil, err
	}
	proxy, err := proxyFor(conn, set)
	if err != nil {
		return nil, err
	}

	wire := ssrf.SafeTransport(
		ssrf.WithAddressPolicy(addressPolicy(set, &set.Counters, opts.Family)),
		ssrf.WithAllowedPorts(dialablePorts(web, conn.Proxy, set)...),
		ssrf.WithDialer(&net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}),
	)
	wire.TLSClientConfig = tlsCfg
	wire.TLSHandshakeTimeout = handshakeTimeout
	wire.ResponseHeaderTimeout = headerTimeout
	wire.Proxy = proxy
	wire.MaxIdleConnsPerHost = set.Budget.ReadConcurrency + set.Budget.MutationConcurrency

	base := http.RoundTripper(wire)
	if set.WireTransport != nil {
		base = set.WireTransport
	}
	// The price counter sits at the wire, beneath the retry round tripper, which
	// is the only position that sees what a call SENT rather than what it was
	// answered: a repeated attempt spends the user's quota exactly as an answered
	// one does.
	base = pricedTransport{next: base}
	// The attempt hook sits beneath the retry round tripper, so it sees one call
	// per attempt. The request hook sits above it, installed on the client below,
	// so it sees one call per logical request whatever the attempt count.
	if set.AttemptObserver != nil {
		base = set.AttemptObserver(base)
	}

	c := &Conn{
		logger:     loggerOf(set),
		credential: set.Credential,
		mapper:     opts.Mapper,
		signal:     opts.Signal,
		apiBase:    apiBase,
		web:        web,
		wire:       wire,
		counters:   set.Counters,
		// The header list is COPIED, as the CA and key material is, so the
		// validated set cannot change under a live connection: the caller keeps
		// the backing array of the slice it passed, and an entry rewritten there
		// would reach the wire with a name the constructor refuses, applied after
		// the credential and therefore able to replace it.
		headers:    slices.Clone(conn.Headers),
		budget:     set.Budget,
		readSlots:  make(chan struct{}, set.Budget.ReadConcurrency),
		writeSlots: make(chan struct{}, set.Budget.MutationConcurrency),
		family:     opts.Family,
		mutations:  set.Mutations,
	}
	c.gov = newGovernor(set, &c.counters, opts.Family, opts.Clock)

	policy := c.redirectPolicy(web, set)
	c.read = httpx.NewRetryClient(base, policy, httpx.TransportConfig{
		MaxAttempts:        set.Budget.Retries + 1,
		RetryNonIdempotent: true,
		CheckRetry:         retryCheck,
		OnRetry:            c.onRetry,
	})
	c.mutate = httpx.NewRetryClient(base, policy, httpx.TransportConfig{
		MaxAttempts: -1,
		CheckRetry:  retryCheck,
		OnRetry:     c.onRetry,
	})
	observe(c.read, set.RequestObserver)
	observe(c.mutate, set.RequestObserver)
	return c, nil
}

// observe installs the consumer's per-logical-request hook OUTSIDE the retry round
// tripper, which is the position that sees one call per request rather than one per
// attempt: the retry round tripper is the value the client constructor left in
// Transport, so wrapping that value is what puts the hook above it.
func observe(client *http.Client, wrap func(http.RoundTripper) http.RoundTripper) {
	if wrap == nil {
		return
	}
	client.Transport = wrap(client.Transport)
}

// onRetry records that an attempt was repeated: the attempt number onto the
// counter the operation's context carries, so the per-request line can report it,
// and the counter seam with the operation the request belongs to.
func (c *Conn) onRetry(attempt int, req *http.Request, _ *http.Response, _ error) {
	if req != nil {
		if counter := attemptsFrom(req.Context()); counter != nil {
			counter.Store(int64(attempt) + 1)
		}
	}
	if c.counters.Retried != nil {
		c.counters.Retried(c.family, opFrom(req))
	}
}

// The three values one operation's context carries for the machinery beneath it: the
// operation's own name, which the retry seam has no other way to learn, the attempt
// count that seam reports, which the per-request line reads back, and the price of
// the CALL every request of it belongs to.
type (
	opKey       struct{}
	attemptsKey struct{}
	priceKey    struct{}
)

// withRecord puts an operation's name and a fresh attempt counter on its context.
func withRecord(ctx context.Context, op string) context.Context {
	ctx = context.WithValue(ctx, opKey{}, op)
	return context.WithValue(ctx, attemptsKey{}, &atomic.Int64{})
}

// Call opens one operation and returns the context its every request is issued on,
// which is what makes the published price a CALL's price rather than a request's.
//
// A family calls it once per operation, because only the family knows where one
// begins: on this library's own hot path a list costs one request plus one per row,
// and nothing beneath the family can tell that from several calls of one operation.
// The boundary is therefore stated rather than inferred from the method name, which
// cannot tell a second call of one operation from a second request of the first.
//
// A request issued outside a Call is priced as a call of its own, so an operation
// that forgets to open one under-reports rather than accumulating without bound.
func Call(ctx context.Context, op string) context.Context {
	return context.WithValue(withPrice(ctx), opKey{}, op)
}

// writeKey marks a call that belongs to a mutating operation.
type writeKey struct{}

// CallWrite is [Call] for a mutating operation whose call also reads: every read
// it makes is part of its write, so a read-only client refuses it with
// [forgeapi.CodeMutationsDisabled] before anything is sent, and the mutation reserve,
// held back for a write and its follow-up reads, admits it.
func CallWrite(ctx context.Context, op string) context.Context {
	return context.WithValue(Call(ctx, op), writeKey{}, true)
}

// writes reports whether ctx belongs to a call [CallWrite] opened.
func writes(ctx context.Context) bool {
	marked, _ := ctx.Value(writeKey{}).(bool)
	return marked
}

// withPrice puts a fresh price counter on a context that carries none, and leaves one
// that does alone: the counter IS the call, so a nested request joins the call it is
// part of rather than opening another.
func withPrice(ctx context.Context) context.Context {
	if priceOf(ctx) != nil {
		return ctx
	}
	return context.WithValue(ctx, priceKey{}, &atomic.Int64{})
}

// priceOf reads the price counter of the call a context belongs to, nil where nothing
// opened one.
func priceOf(ctx context.Context) *atomic.Int64 {
	counter, _ := ctx.Value(priceKey{}).(*atomic.Int64)
	return counter
}

// price is what the call a context belongs to has sent so far, counted at the wire.
func price(ctx context.Context) int {
	if counter := priceOf(ctx); counter != nil {
		return int(counter.Load())
	}
	return 0
}

// pricedTransport counts every attempt that reaches the wire against the call that
// issued it. It sits beneath the retry round tripper, so a retried request is
// counted as the two requests it is, and above nothing: a request this library never
// built is never counted.
type pricedTransport struct{ next http.RoundTripper }

func (t pricedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if counter := priceOf(r.Context()); counter != nil {
		counter.Add(1)
	}
	return t.next.RoundTrip(r)
}

// opFrom reads the operation a request belongs to, empty where the request was not
// one of this library's.
func opFrom(req *http.Request) string {
	if req == nil {
		return ""
	}
	op, _ := req.Context().Value(opKey{}).(string)
	return op
}

// attemptsFrom reads the attempt counter of the operation a context belongs to.
func attemptsFrom(ctx context.Context) *atomic.Int64 {
	counter, _ := ctx.Value(attemptsKey{}).(*atomic.Int64)
	return counter
}

// attempts is the attempt count one request reached, which is one where the retry
// seam never fired.
func attempts(ctx context.Context) int {
	counter := attemptsFrom(ctx)
	if counter == nil {
		return 1
	}
	if n := counter.Load(); n > 0 {
		return int(n)
	}
	return 1
}

// retryCheck is this library's own retry predicate. It covers transient transport
// errors and the gateway statuses and NEVER 429: the default sleeps a parsed
// retry-after after draining the response that carried it, so a throttle would
// reach the caller as a deadline cancellation rather than as a rate-limited error
// with its duration.
func retryCheck(ctx context.Context, resp *http.Response, err error) (bool, error) {
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if err != nil {
		return httpx.IsTransient(err), nil
	}
	switch resp.StatusCode {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true, nil
	}
	return false, nil
}

// loggerOf is the injected logger with the documented fallback. This library
// supervises rather than computes, so it emits, and a nil option means the
// default sink rather than silence.
func loggerOf(set *forgeapi.Settings) *slog.Logger {
	if set.Logger != nil {
		return set.Logger
	}
	return slog.Default()
}

// checkBudget refuses a negative value on any budget knob, which is the one
// arithmetic a resolved budget cannot carry.
func checkBudget(set *forgeapi.Settings) error {
	b := set.Budget
	negatives := []int{b.ListPages, b.StatusPages, b.StatusReadsPerInterval, b.Retries, b.ReadConcurrency, b.MutationConcurrency, set.MutationReserve}
	for _, n := range negatives {
		if n < 0 {
			return local(forgeapi.CodeBudgetInvalid, "a budget option is negative")
		}
	}
	durations := []time.Duration{b.OperationTimeout, b.StatusTimePerInterval, b.MutationInterval}
	for _, d := range durations {
		if d < 0 {
			return local(forgeapi.CodeBudgetInvalid, "a budget duration is negative")
		}
	}
	if b.ReadConcurrency == 0 || b.MutationConcurrency == 0 {
		return local(forgeapi.CodeBudgetInvalid, "a concurrency limit of zero admits no request")
	}
	return nil
}

// parseBase reads the connection's web base URL, refusing at entry the one form
// whose browser reading differs from net/url's and any form but an origin and a
// path, then the two postures a connection has to state for itself.
func parseBase(raw string, set *forgeapi.Settings) (*url.URL, error) {
	if raw == "" {
		return nil, local(forgeapi.CodeConnectionInvalid, "no web base URL")
	}
	if urlform.Classify(raw).Class != urlform.ClassAbsolute {
		return nil, local(forgeapi.CodeConnectionInvalid, "web base URL is not an absolute scheme-and-host URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, local(forgeapi.CodeConnectionInvalid, "web base URL does not parse")
	}
	if !originAndPath(u, raw) {
		return nil, local(forgeapi.CodeConnectionInvalid, "base URL carries userinfo, a query or a fragment: a base is an origin and a path")
	}
	switch u.Scheme {
	case schemeHTTPS:
	case schemeHTTP:
		if !set.PlaintextHTTP {
			return nil, local(forgeapi.CodePlaintextRefused, "instance URL is plaintext and this connection did not opt in")
		}
	default:
		return nil, local(forgeapi.CodeConnectionInvalid, "instance URL carries a scheme this library does not speak")
	}
	if !set.PrivateAddresses && !publicHost(u.Hostname()) {
		return nil, local(forgeapi.CodePrivateAddressRefused, "instance host is private or single label and this connection did not opt in")
	}
	return u, nil
}

// ParseOriginAndPath parses raw when it is an origin and a path, the one form a
// base URL takes, and answers false for any other form or a URL that does not parse.
func ParseOriginAndPath(raw string) (*url.URL, bool) {
	u, err := url.Parse(raw)
	if err != nil || !originAndPath(u, raw) {
		return nil, false
	}
	return u, true
}

// originAndPath reports whether u, parsed from raw, carries no userinfo, query or
// fragment. Userinfo is no part of the host (RFC 3986 section 3.2.1), so a base
// carrying one names a host a reader takes for the forge and another a request
// reaches; a query or a fragment is no part of a root a request path is joined to.
// The fragment is read off raw, because net/url records an empty one nowhere.
func originAndPath(u *url.URL, raw string) bool {
	return u.User == nil && !u.ForceQuery && u.RawQuery == "" && !strings.Contains(raw, "#")
}

// publicHost reports whether a host is one the default posture reaches: a
// globally routable literal, or a dotted name. It is the form half of the check;
// the address verdict is taken again at the socket.
func publicHost(host string) bool {
	if addr, err := netip.ParseAddr(host); err == nil {
		return ssrf.IsPublicAddr(addr)
	}
	return ssrf.IsPublicHost(host)
}

// resolveAPIBase takes the connection's API base where it names one and derives
// the family's own root where it does not.
func resolveAPIBase(conn *forgeapi.Connection, web *url.URL, set *forgeapi.Settings, opts Options) (*url.URL, error) {
	raw := conn.APIBaseURL
	if raw == "" && opts.DeriveAPIBase != nil {
		raw = opts.DeriveAPIBase(web)
	}
	if raw == "" {
		return nil, local(forgeapi.CodeConnectionInvalid, "no API base URL and none could be derived")
	}
	u, err := parseBase(raw, set)
	if err != nil {
		return nil, err
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	return u, nil
}

// checkHeaders refuses a malformed entry and a name something beneath the
// consumer writes, naming the header rather than dropping it: an operator whose
// setting was ignored believes something false about their traffic.
func checkHeaders(headers []forgeapi.Header) error {
	reserved := forgeapi.ReservedHeaders()
	for _, h := range headers {
		if h.Name == "" || !validFieldName(h.Name) || !validFieldValue(h.Value) {
			return local(forgeapi.CodeConnectionInvalid, "a header entry is not a well-formed field name and value")
		}
		for _, r := range reserved {
			if strings.EqualFold(h.Name, r) {
				return &forgeapi.Error{Code: forgeapi.CodeHeaderReserved, Message: "header " + h.Name + " is written beneath the consumer"}
			}
		}
	}
	return nil
}

func validFieldName(name string) bool {
	return http.CanonicalHeaderKey(name) != "" && !strings.ContainsAny(name, " \t\r\n:")
}

func validFieldValue(value string) bool {
	for i := range len(value) {
		if b := value[i]; b < 0x20 && b != '\t' || b == 0x7f {
			return false
		}
	}
	return true
}

// buildTLS assembles this connection's TLS configuration, which neither
// dependency does. A supplied CA becomes the SOLE anchor rather than an addition
// to the system pool, because a self-hosted instance presenting a private CA is
// the case this serves and accepting either is how a mis-issued public
// certificate stays accepted.
func buildTLS(conn *forgeapi.Connection) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if len(conn.CABytes) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(conn.CABytes) {
			return nil, local(forgeapi.CodeConnectionInvalid, "CA bytes carry no PEM certificate")
		}
		cfg.RootCAs = pool
	}
	switch {
	case len(conn.ClientCert) > 0 && len(conn.ClientKey) > 0:
		pair, err := tls.X509KeyPair(conn.ClientCert, conn.ClientKey)
		if err != nil {
			return nil, local(forgeapi.CodeConnectionInvalid, "client certificate and key do not form a pair")
		}
		cfg.Certificates = []tls.Certificate{pair}
	case len(conn.ClientCert) > 0 || len(conn.ClientKey) > 0:
		return nil, local(forgeapi.CodeConnectionInvalid, "client certificate pair is half present")
	}
	return cfg, nil
}

// proxyFor turns this connection's proxy statement into the transport's proxy
// function. The environment's proxy variables are consulted nowhere in this
// stack, so the connection record is the whole truth about where its traffic
// goes, and a malformed URL is refused rather than dropped to a direct dial.
func proxyFor(conn *forgeapi.Connection, set *forgeapi.Settings) (func(*http.Request) (*url.URL, error), error) {
	if conn.Proxy == "" {
		return nil, nil
	}
	u, err := url.Parse(conn.Proxy)
	if err != nil || u.Host == "" {
		return nil, local(forgeapi.CodeConnectionInvalid, "proxy URL does not parse")
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, local(forgeapi.CodeConnectionInvalid, "proxy URL carries a scheme this library does not proxy over")
	}
	if !set.PrivateAddresses && !publicHost(u.Hostname()) {
		return nil, local(forgeapi.CodePrivateAddressRefused, "proxy host is private or single label and this connection did not opt in")
	}
	return http.ProxyURL(u), nil
}

// addressPolicy is this connection's address verdict, taken at the socket inside
// the transport's own control hook, which is where containment lives.
//
// It counts its own refusals, because the layer beneath logs them to the default
// sink and exposes no logger option: a refusal reported only there lands somewhere
// this library does not own, so the counter seam is what makes it observable to a
// consumer at all.
func addressPolicy(set *forgeapi.Settings, counters *forgeapi.Counters, family forgeapi.Family) ssrf.AddressPolicy {
	if set.PrivateAddresses {
		return func(netip.Addr) bool { return true }
	}
	return func(addr netip.Addr) bool {
		if ssrf.IsPublicAddr(addr) {
			return true
		}
		if counters.AddressRefusal != nil {
			// The kind is the position rather than the address, because a
			// counter label is a dimension and an address is not one.
			counters.AddressRefusal(family, "dial")
		}
		return false
	}
}

// allowedPorts is this connection's port set: the explicit port where the URL
// carries one, plus the default port of every scheme the connection permits, so a
// plaintext instance answering a redirect to https can work at all.
//
// This is the set a redirect HOP is held to, so it deliberately excludes the proxy's
// own port: a hop to another host at the port this connection's proxy happens to
// listen on is not something the proxy statement permits.
func allowedPorts(web *url.URL, set *forgeapi.Settings) []uint16 {
	ports := []uint16{443}
	if set.PlaintextHTTP {
		ports = append(ports, 80)
	}
	if p := web.Port(); p != "" {
		if n, err := strconv.ParseUint(p, 10, 16); err == nil {
			ports = append(ports, uint16(n))
		}
	}
	return ports
}

// dialablePorts is the set the SOCKET is held to, which is the set above plus the
// stated proxy's own port. On a proxied connection every socket goes to the proxy, so
// a proxy on anything but a scheme's default port would otherwise be refused at the
// socket and the connection could not be used at all.
func dialablePorts(web *url.URL, proxy string, set *forgeapi.Settings) []uint16 {
	ports := allowedPorts(web, set)
	if proxy == "" {
		return ports
	}
	u, err := url.Parse(proxy)
	if err != nil {
		return ports
	}
	if p := u.Port(); p != "" {
		if n, parseErr := strconv.ParseUint(p, 10, 16); parseErr == nil {
			ports = append(ports, uint16(n))
		}
	}
	return ports
}

// BudgetState is the governor's state for this connection, read-only and never
// written from outside.
func (c *Conn) BudgetState() forgeapi.BudgetState { return c.gov.state() }

// Logger is the injected logger, for the family's own lines.
func (c *Conn) Logger() *slog.Logger { return c.logger }

// Counters is the consumer's counter set.
func (c *Conn) Counters() forgeapi.Counters { return c.counters }

// Budget is the resolved budget.
func (c *Conn) Budget() forgeapi.Budget { return c.budget }

// WebBase is the connection's web base URL, which is where a document served at
// the instance root rather than under the API root lives.
func (c *Conn) WebBase() *url.URL { return c.web }

// APIBase is the connection's API root, the one the connection named or the one its
// family derived, which is where a document served BESIDE the REST routes lives.
func (c *Conn) APIBase() *url.URL { return c.apiBase }

// UnderAPI is the escaped path an absolute address names beneath this connection's
// API root, and whether it names one: another origin, compared as the redirect policy
// compares origins, or a path outside the root names nothing on this connection.
func (c *Conn) UnderAPI(address string) (string, bool) {
	u, err := url.Parse(address)
	if err != nil || originOf(u) != originOf(c.apiBase) {
		return "", false
	}
	rest, ok := strings.CutPrefix(u.EscapedPath(), strings.TrimSuffix(c.apiBase.EscapedPath(), "/"))
	if !ok || !strings.HasPrefix(rest, "/") {
		return "", false
	}
	return rest, true
}

// AdmitFold asks the governor whether one folded-status read may be issued inside
// the current rolling interval, and says why not when it may not.
func (c *Conn) AdmitFold(op string) (forgeapi.PartialReason, bool) { return c.gov.admitFold(op) }

// Rotate records how far the rotation of those reads got, so the next interval
// starts after the row last served rather than at the first.
func (c *Conn) Rotate(cursor forgeapi.RotationCursor) { c.gov.rotate(cursor) }

// SpendFold charges one folded-status read's measured wall clock to the current
// interval, which is what the wall-clock bound spends.
func (c *Conn) SpendFold(d time.Duration) { c.gov.spendFold(d) }

// Price replaces what the governor reports as the last call's cost, for the one
// product that prices a call by a figure in its own RESPONSE rather than by the
// number of requests that left.
//
// [Conn.Do] publishes the wire count on every arm it leaves by, so a family calling
// this AFTER Do has returned is stating the same call's cost in the units that
// product bills in. Measured: GitLab's GraphQL endpoint answers a complexity object
// beside the data, and the expectation table prices a document by that score and a
// REST request at one. A family with no such figure never calls this and the wire
// count stands.
func (c *Conn) Price(cost int) { c.gov.price(cost) }

// Close releases this connection's pool: the idle connections of the ssrf
// transport Open built, which the connection holds by its concrete type. A
// substitute wire is its author's, so Close never reaches it.
func (c *Conn) Close() { c.wire.CloseIdleConnections() }

// Request is one call this package makes for a family.
type Request struct {
	Body any
	// Headers are the FAMILY's own request headers for this request, written
	// after the credential and before the consumer's. One family needs them and
	// the reason is per request rather than per connection: its REST API-version
	// pin is sent only to an instance known to carry that version, and what
	// establishes that is a header riding a response the connection already
	// received, so the first request of a connection carries no pin and the rest
	// do. A name here is one the library writes, so it is on the reserved list and
	// a consumer's own entry can never collide with it.
	Headers []forgeapi.Header
	Query   url.Values
	Op      string
	Method  string
	Path    string
	// EscapedPath is this request's path ALREADY percent-encoded, taken in place
	// of Path where it is set. Exactly one family needs it, and the reason is
	// measured rather than stylistic: GitLab's repository selector is a full
	// namespace path its API takes as ONE path segment with the separators
	// encoded, and Path is assigned to [net/url.URL.Path], which is the DECODED
	// path. So a selector encoded into Path leaves as %252F and one left decoded
	// leaves as extra path segments, and gitlab.com answers 404 to both.
	EscapedPath string
	AbsoluteURL string
	// Answers are the non-2xx statuses this request reads as an ANSWER rather than
	// a refusal: [Conn.Do] returns such a response with its body and no error, and
	// the family decodes the outcome from it. One route needs it: GitHub's
	// asynchronous merge answers a merge already in flight with a 409 whose body
	// names that merge's handle, which is the outcome, not a conflict.
	Answers  []int
	MaxBytes int64
	// Page is which page of a paged read this request asks for, so the
	// per-request record carries it. One where the read is not paged.
	Page int
	// Document says this request carries a GraphQL document rather than a REST
	// route, which is what the per-request record's transport attribute names.
	// One family in scope reaches an instance over two arms and degrades from
	// the document one to the other at runtime, so the record has to say which
	// arm a line describes: a constant there reports the refusal that degrades a
	// connection as a REST failure, which is the one line an operator reads to
	// learn why it degraded.
	//
	// A document request is a read whatever verb carries it, unless Mutation
	// says otherwise: see [Request.mutates].
	Document bool
	// Mutation says this document is a GraphQL mutation, which travels as the
	// same POST a query does and changes the instance, so only the document's
	// own kind tells the two apart.
	Mutation bool
}

// mutates reports whether this request changes something on the instance, which is
// what the mutation gate, the write slots, the inter-mutation interval and the
// single-attempt client govern. A GraphQL query travels as a POST and changes
// nothing, so the verb alone would refuse every document read on a read-only client
// and pace it like a merge.
func (r *Request) mutates() bool {
	if r.Document {
		return r.Mutation
	}
	return r.Method != http.MethodGet
}

// Exchange is what one exchange with an instance was: the method it used and the
// arm that carried it. The per-request record names both from here rather than
// assuming either, and a family that reaches a failure AFTER a response states the
// exchange it reached it on.
//
// The ZERO value describes NO exchange, which is what a refusal reached before any
// request is. The record then carries neither attribute, because naming a method
// for an exchange that never happened is worse than naming none.
type Exchange struct {
	method string
	arm    string
}

// REST names one exchange on the REST arm, which is every route of every family.
func REST(method string) Exchange { return Exchange{method: method, arm: "rest"} }

// Document names one exchange on the GraphQL document arm, which is the two reads
// one product answers from a document and nothing else.
func Document(method string) Exchange { return Exchange{method: method, arm: "graphql"} }

// exchange is what a request in flight is, for the record: its own method on the
// arm it goes out over.
func exchange(req *Request) Exchange {
	if req.Document {
		return Document(req.Method)
	}
	return REST(req.Method)
}

// Response is what one call answered: the status, the headers, the decoded-ready
// bytes, and whether a redirect was followed to get them.
//
// Redirected is the transport's record that this exchange took a hop, which is the
// rename witness this layer owns rather than a family: one product
// answers a renamed repository with a 301 the follow-and-revalidate policy would
// otherwise follow silently, and a FINAL-PATH comparison cannot be the witness,
// because the same product answers a repo-addressed list with an id-addressed link
// header on repositories never renamed. So what is recorded is that a hop happened
// on THIS request rather than what the last URL looks like.
//
// Ended is the address the LAST request of the exchange went to, which on a followed
// hop is where that hop led. It is NOT a second witness, for the same reason: what
// it is for is resolving the successor once a hop has already been recorded, because
// the location that product sends addresses the repository by id and its canonical
// name is one read from there.
//
// Refused is the address of a hop the redirect policy refused, answered beside that
// refusal with the status and headers that asked for it. A write's hop is refused,
// so this is where a family reads a successor without sending the write again.
type Response struct {
	Header     http.Header
	quote      func(string) string
	Ended      string
	Refused    string
	Body       []byte
	Status     int
	Redirected bool
}

// Quote is text the instance answered in this exchange made safe to carry on an
// error a family builds from it, as [Conn.Do] makes a refusal's text safe: every
// secret the request carried redacted out of it, on one line, and bounded.
func (r *Response) Quote(text string) string {
	if r == nil || r.quote == nil {
		return sanitize(text)
	}
	return r.quote(text)
}

// Do issues one request, maps its outcome and records what it cost.
//
// Every read it issues is UNCONDITIONAL, and no request carries a validator. A
// validator only pays where the sender can answer a not-modified response from
// something it holds, and nothing above this core holds a response body: the
// per-connection state is the minted merge handles and the budget, a decoded body
// kept beside them would be the repository cache the boundary rules out, and no
// published return has a member that says unchanged. So a not-modified answer has
// nowhere to go, and one arriving anyway is an unexpected status the mapper reports
// like any other rather than a success arm nothing can spend. If a measured saving
// ever justifies the exchange, its shape is an opt-in caching round tripper inside
// this connection's own chain, which remembers the body it would serve.
//
// What the call spent is published on EVERY arm this function leaves by, the two
// refusals that send nothing included. The figure is the last CALL's price, so a
// call that reached no wire publishes the zero it spent: leaving the previous
// call's figure standing would report a cost for a call that had none.
func (c *Conn) Do(ctx context.Context, req *Request) (*Response, error) {
	if fe := c.permit(ctx, req); fe != nil {
		c.gov.price(price(ctx))
		return nil, fe
	}
	target := c.urlFor(req)
	ctx, cancel := c.withDeadline(ctx)
	defer cancel()
	ctx = withRecord(ctx, req.Op)
	ctx = withPrice(ctx)
	defer func() { c.gov.price(price(ctx)) }()

	// A deadline already exhausted is reported rather than raced against the
	// concurrency gate, whose select would otherwise admit the request as often
	// as not and send it on a context that is already done.
	if ctx.Err() != nil {
		return nil, c.transportError(ctx, req, ctx.Err(), time.Now())
	}

	if err := c.acquire(ctx, req); err != nil {
		return nil, c.transportError(ctx, req, err, time.Now())
	}
	defer c.release(req)

	started := time.Now()
	//nolint:bodyclose // the close is the defer below, which the analyzer cannot follow across the error branch
	resp, token, err := c.send(ctx, req, target)
	if err != nil {
		return refusedHop(resp), c.transportError(ctx, req, err, started)
	}

	defer httpx.DrainClose(resp.Body)

	c.gov.observe(resp.Header, c.signal)
	out := &Response{
		Header: resp.Header, Status: resp.StatusCode, quote: c.quoter(token),
		Redirected: followed(resp, target), Ended: ended(resp, target),
	}
	limit := req.MaxBytes
	if limit <= 0 {
		limit = maxBodyBytes
	}
	body, readErr := httpx.ReadLimitedBody(resp.Body, limit)
	if readErr != nil {
		return nil, c.transportError(ctx, req, readErr, started)
	}
	out.Body = body
	if resp.StatusCode >= 300 && !slices.Contains(req.Answers, resp.StatusCode) {
		return out, c.upstreamError(ctx, req, out, started)
	}
	c.log(ctx, req, exchange(req), out.Status, "", "", forgeapi.KindUnknown, out.Header, started, nil)
	return out, nil
}

// followed reports whether this exchange took a redirect hop, by comparing the URL
// of the request the client ENDED on against the one it was given. net/http sets
// that field to the last request of the chain, so the comparison is against this
// request's own target and never against a family's selector, which is what keeps a
// list's second page, fetched from an id-addressed link header, from reading as a
// move.
func followed(resp *http.Response, target string) bool {
	return resp.Request != nil && resp.Request.URL != nil && resp.Request.URL.String() != target
}

// refusedHop is the response a failed exchange answers beside its refusal. net/http
// returns a response with an error only where the redirect policy refused a hop, its
// body already closed, so it is nil on every other failure.
func refusedHop(resp *http.Response) *Response {
	if resp == nil {
		return nil
	}
	location, err := resp.Location()
	if err != nil {
		return nil
	}
	return &Response{Header: resp.Header, Status: resp.StatusCode, Refused: location.String()}
}

// ended is the address the exchange's last request went to, which is the target where
// the client reports none.
func ended(resp *http.Response, target string) string {
	if resp.Request == nil || resp.Request.URL == nil {
		return target
	}
	return resp.Request.URL.String()
}

// permit is the pair of refusals a request meets before anything is sent: a mutation
// on a client that refuses them, and a read the governor holds back to keep the
// mutation reserve. Both carry a diagnostic id, so both are recorded rather than
// returned with an id naming no line.
//
// Every read passes the reserve, not only the folded-status reads: the reserve is a
// floor under what a merge the user is about to click will need, so a cycle of
// ordinary reads that crossed it would empty the budget the reserve exists to hold.
func (c *Conn) permit(ctx context.Context, req *Request) *forgeapi.Error {
	if req.mutates() || writes(ctx) {
		if c.mutations {
			return nil
		}
		return c.refusal(ctx, req, c.fail(req.Op, forgeapi.CodeMutationsDisabled, forgeapi.KindForbidden, 0,
			"mutations are disabled on this client"))
	}
	if _, ok := c.gov.admit(req.Op); !ok {
		return c.refusal(ctx, req, c.deferred(req.Op))
	}
	return nil
}

// send builds and issues the request on the client [Request.mutates] chooses, and
// answers the credential it carried, which the instance's text is redacted of.
func (c *Conn) send(ctx context.Context, req *Request, target string) (*http.Response, string, error) {
	var payload []byte
	if req.Body != nil {
		encoded, marshalErr := marshal(req.Body)
		if marshalErr != nil {
			return nil, "", marshalErr
		}
		payload = encoded
	}
	r, err := http.NewRequestWithContext(ctx, req.Method, target, bodyReader(payload))
	if err != nil {
		return nil, "", err
	}
	if payload != nil {
		r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(payload)), nil }
		r.ContentLength = int64(len(payload))
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Accept", "application/json")
	r.Header.Set("User-Agent", userAgent)
	token, err := c.credential.Token(ctx)
	if err != nil {
		return nil, "", err
	}
	// Bearer is the one scheme every product reads for every kind of token it
	// issues; gitlab.com refuses the token scheme the others also accept.
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	for _, h := range req.Headers {
		r.Header.Set(h.Name, h.Value)
	}
	for _, h := range c.headers {
		r.Header.Set(h.Name, h.Value)
	}
	client := c.read
	if req.mutates() {
		if paceErr := c.pace(ctx); paceErr != nil {
			return nil, "", paceErr
		}
		client = c.mutate
	}
	resp, err := client.Do(r)
	return resp, token, err
}

func bodyReader(payload []byte) io.Reader {
	if payload == nil {
		return nil
	}
	return bytes.NewReader(payload)
}

// urlFor builds this request's absolute URL from the API base, the relative path
// and the query. A path segment is escaped here rather than stored pre-escaped.
//
// [Request.EscapedPath] is the one arm that takes an already-encoded path, and it
// is joined as TEXT rather than assigned to a URL field: assigning it to Path
// double-encodes it, because Path is the decoded path, and the return of this
// function is parsed by net/http, which keeps a non-canonical escaping in RawPath
// and sends it unchanged.
func (c *Conn) urlFor(req *Request) string {
	if req.AbsoluteURL != "" {
		return req.AbsoluteURL
	}
	if req.EscapedPath != "" {
		target := strings.TrimSuffix(c.apiBase.String(), "/") + req.EscapedPath
		if len(req.Query) > 0 {
			target += "?" + req.Query.Encode()
		}
		return target
	}
	u := *c.apiBase
	u.Path = c.apiBase.Path + req.Path
	u.RawPath = ""
	if len(req.Query) > 0 {
		u.RawQuery = req.Query.Encode()
	}
	return u.String()
}

// withDeadline applies the per-operation bound as a real context deadline, which
// is the only bound that reaches every blocking phase.
//
// A ZERO bound is the deadline its option publishes, one that has already passed,
// rather than no bound at all: there is one configuration door, so the zero a caller
// passes is the literal zero, and a client that answered an uncapped context to it
// would run every operation unbounded while the option said the opposite. A negative
// never reaches here, being refused at construction.
func (c *Conn) withDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	if c.budget.OperationTimeout <= 0 {
		return context.WithDeadline(ctx, time.Now())
	}
	return context.WithTimeout(ctx, c.budget.OperationTimeout)
}

// acquire bounds concurrency per instance, more for reads than for mutations.
func (c *Conn) acquire(ctx context.Context, req *Request) error {
	slots := c.readSlots
	if req.mutates() {
		slots = c.writeSlots
	}
	select {
	case slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Conn) release(req *Request) {
	slots := c.readSlots
	if req.mutates() {
		slots = c.writeSlots
	}
	<-slots
}

// pace holds the minimum interval between consecutive mutations, which is
// upstream's own separate instruction rather than something a consumer has to
// discover from a secondary limit.
//
// The wait selects on the operation's context, because the per-operation bound is a
// context deadline and a bound that cannot interrupt every blocking phase is not a
// bound: a timer waited on alone holds the serialization lock past the deadline and
// returns a cancellation the caller reads long after its own clock expired.
func (c *Conn) pace(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.budget.MutationInterval <= 0 {
		c.lastMutate = time.Now()
		return nil
	}
	if !c.lastMutate.IsZero() {
		if wait := c.budget.MutationInterval - time.Since(c.lastMutate); wait > 0 {
			timer := time.NewTimer(wait)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	c.lastMutate = time.Now()
	return nil
}

// marshal encodes a request body. It is a separate function because a mutation's
// body has to be replayable, which means holding the bytes rather than streaming
// them.
func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := jsonEncoder(&buf)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// Decode reads one response body under the decode bounds, which is what keeps an
// untrusted response's cost bounded before allocation scales with it.
func Decode(body []byte, v any) error {
	if len(body) == 0 {
		return errors.New("empty response body")
	}
	d := jsoncap.NewDecoder(bytes.NewReader(body), maxDecodeElements)
	if err := d.Decode(v); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	return nil
}

// fail builds this library's own refusal for one operation: it names the
// operation and the family, so it carries a diagnostic id.
func (c *Conn) fail(op, code string, kind forgeapi.ErrorKind, status int, message string) *forgeapi.Error {
	return &forgeapi.Error{
		Op: op, Code: code, Family: c.family, Status: status, Kind: kind,
		Message: message, DiagID: diagID(),
	}
}

// Fail builds a failure the family reached after a request: it names the operation
// and the family, so it carries a diagnostic id, and the line that id names is
// recorded here rather than left to the caller.
//
// The exchange is the one the family reached this failure ON, which only the family
// knows: the response is already in its hand and this core never saw which of the
// two arms answered it.
func (c *Conn) Fail(ctx context.Context, op string, ex Exchange, code string, kind forgeapi.ErrorKind, status int, message string) *forgeapi.Error {
	fe := c.fail(op, code, kind, status, message)
	c.record(ctx, op, ex, fe)
	return fe
}

// FailBody is what a response body this library could not read answers: the instance
// replied, so the failure is upstream's shape rather than a local refusal, and it
// carries the response's OWN status. Fabricating a 200 there would contradict the
// published invariant that the status is the real one always, and it is immediately
// wrong on a creation, which answers 201.
//
// The code is the validation one, which is the code a response this library will not
// accept already carries where a family meets one it can read: a consumer branches on
// that field, so leaving it empty would hand it nothing on exactly the failure it
// cannot otherwise classify.
func (c *Conn) FailBody(ctx context.Context, op string, ex Exchange, status int) *forgeapi.Error {
	fe := c.fail(op, forgeapi.CodeValidation, forgeapi.KindUpstream, status, "response body did not decode")
	c.record(ctx, op, ex, fe)
	return fe
}

// refusal records a failure this library produced instead of a request and returns it,
// so the diagnostic id the caller sees names a line here too.
func (c *Conn) refusal(ctx context.Context, req *Request, fe *forgeapi.Error) *forgeapi.Error {
	c.log(ctx, req, exchange(req), fe.Status, fe.Code, fe.DiagID, fe.Kind, nil, time.Now(), fe)
	return fe
}

// record is refusal for a failure reached with no request value in hand, which is the
// shape a family's own post-response refusals take.
func (c *Conn) record(ctx context.Context, op string, ex Exchange, fe *forgeapi.Error) {
	c.log(ctx, &Request{Op: op}, ex, fe.Status, fe.Code, fe.DiagID, fe.Kind, nil, time.Now(), fe)
}

// Refuse is the capability refusal: no request, one dedicated code, and the
// operation, the capability and the evidence detection holds for the verdict.
//
// The message is worded from the SUPPORT VALUE rather than from the refusal, because
// the two verdicts it refuses on are different facts. A capability detection read as
// absent is one thing; one it could not establish, which an instance with its
// document switched off produces, is another, and asserting the first for the second
// tells a user their forge lacks a feature it may well have. The evidence says which,
// and now so does the message.
func (c *Conn) Refuse(ctx context.Context, op string, capability forgeapi.Capability, support forgeapi.Support, ev forgeapi.Evidence) *forgeapi.Error {
	message := "no " + string(capability) + " capability could be established on this instance"
	if support == forgeapi.SupportNo {
		message = "this instance carries no " + string(capability) + " capability"
	}
	fe := &forgeapi.Error{
		Op:         op,
		Code:       forgeapi.CodeCapabilityUnsupported,
		Family:     c.family,
		Kind:       forgeapi.KindForbidden,
		Message:    message,
		DiagID:     diagID(),
		Capability: capability,
		Evidence:   ev,
	}
	// No request was sent, so the record names no exchange: the zero value is
	// what says that, and a method here would describe one that never happened.
	c.record(ctx, op, Exchange{}, fe)
	return fe
}

// deferred is what a read the governor held back answers: the request was never
// issued, so it carries no status, and its kind is the rate-limited one because
// that is the reason a consumer renders. A caller that can carry a partial marker
// turns this into one rather than into a failure; one that cannot reports it.
// It carries the throttle kind with no code and no status, which is the same pair
// this family's mapper answers an upstream throttle with minus the status: a
// consumer tells the two apart by that status and by the remaining figure the
// governor reports, which a deferral leaves above zero.
func (c *Conn) deferred(op string) *forgeapi.Error {
	fe := c.fail(op, "", forgeapi.KindRateLimited, 0,
		"this read was deferred to hold the mutation reserve")
	fe.Retryable = true
	return fe
}

// IsDeferred reports whether an error is the governor's deferral rather than an
// answer an instance gave, which is what lets a list report the rows it did not
// fetch as partial instead of failing the call.
func IsDeferred(err error) bool {
	fe, ok := errors.AsType[*forgeapi.Error](err)
	return ok && fe.Kind == forgeapi.KindRateLimited && fe.Status == 0
}

// IsThrottled reports whether an error is a throttle the INSTANCE answered rather
// than a deferral this library chose, which the status is what separates: a deferral
// sent nothing and so carries none. It is what lets a fold refused by a throttle mark
// its row with the reason the envelope assigns that refusal instead of failing the
// list around it.
func IsThrottled(err error) bool {
	fe, ok := errors.AsType[*forgeapi.Error](err)
	return ok && fe.Kind == forgeapi.KindRateLimited && fe.Status != 0
}

// Local re-exports the local-refusal shape for a family's own pre-request checks.
func Local(code, message string) *forgeapi.Error { return local(code, message) }

// local builds a refusal reached before any operation began: no operation, no
// family, no status and no diagnostic id, because none of the four exists for a
// validator to report.
func local(code, message string) *forgeapi.Error {
	return &forgeapi.Error{Code: code, Kind: forgeapi.KindUnknown, Message: message}
}

// transportError maps a failure that never reached a status. A context sentinel is
// returned UNCHANGED, which is the third arm of the three-way error contract.
//
// A refusal this library itself produced below the call is recovered rather than
// rebuilt. The redirect policy answers a *forgeapi.Error, net/http wraps whatever
// a policy returns in *url.Error, and stringifying that chain would hand the caller
// an empty code, a transient kind and a retryable true for a permanent containment
// refusal: the four refusal codes the redirect policy owns would be unreachable
// through the field a consumer branches on, and a stale reference would lose its
// successor.
func (c *Conn) transportError(ctx context.Context, req *Request, err error, started time.Time) error {
	if ctxErr := contextSentinel(err); ctxErr != nil {
		c.log(ctx, req, exchange(req), 0, "", "", forgeapi.KindUnknown, nil, started, ctxErr)
		return ctxErr
	}
	if own, ok := errors.AsType[*forgeapi.Error](err); ok {
		if own.Op == "" {
			own.Op = req.Op
		}
		if own.DiagID == "" {
			own.DiagID = diagID()
		}
		c.log(ctx, req, exchange(req), own.Status, own.Code, own.DiagID, own.Kind, nil, started, err)
		return own
	}
	fe := c.fail(req.Op, "", forgeapi.KindTransient, 0, sanitize(httpx.LogSafeError(err).Error()))
	fe.Retryable = true
	c.log(ctx, req, exchange(req), 0, fe.Code, fe.DiagID, fe.Kind, nil, started, err)
	return fe
}

// ContextSentinel re-exports the context-sentinel test for a package that reads a
// transport failure before choosing its own answer.
func ContextSentinel(err error) error { return contextSentinel(err) }

// contextSentinel reports the context sentinel to return unchanged, because the
// error contract is three-way: nil, a *Error, or a context sentinel.
func contextSentinel(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	}
	return nil
}

// upstreamError maps a non-2xx the instance answered, by family plus operation
// plus status plus selected headers rather than by status alone.
func (c *Conn) upstreamError(ctx context.Context, req *Request, resp *Response, started time.Time) *forgeapi.Error {
	answer := readRefusal(resp.Body)
	kind, code := c.mapper(req.Op, resp.Status, resp.Header, answer.Error)
	fe := c.fail(req.Op, code, kind, resp.Status, resp.Quote(answer.text(code)))
	fe.Retryable = kind == forgeapi.KindTransient
	if kind == forgeapi.KindRateLimited {
		fe.RetryAfter = httpx.ParseRetryAfter(resp.Header.Get("Retry-After"))
		if c.counters.RateLimited != nil {
			c.counters.RateLimited(c.family)
		}
	}
	c.log(ctx, req, exchange(req), resp.Status, code, fe.DiagID, kind, resp.Header, started, nil)
	return fe
}

// refusal is the part of a refusal's body this package reads: the instance's own
// message, and RFC 6750's error code, its description and the scope it names.
//
// The message is held as the raw value it arrived as, because GitLab answers it as
// a string, a list of strings or an object of field errors depending on the route,
// and a member decoded as one shape would fail the whole decode on the others,
// losing the error code beside it.
type refusal struct {
	Error       string          `json:"error"`
	Description string          `json:"error_description"`
	Scope       string          `json:"scope"`
	Message     json.RawMessage `json:"message"`
}

// readRefusal decodes what a refusal's body states, the zero refusal where the
// body is not JSON.
func readRefusal(body []byte) refusal {
	var r refusal
	if err := Decode(body, &r); err != nil {
		return refusal{}
	}
	return r
}

// text is upstream's own words for a refusal mapped to code, which the caller
// quotes at the emit site and nothing branches on.
//
// The instance's message member comes first, then RFC 6750's description, then
// its bare error code. A scope refusal reads the description first instead, since
// that is the text naming what the credential lacks and so the promise
// [forgeapi.CodeScopeInsufficient] makes: the instance's message stands in where no
// description came, and the error code only where neither did.
func (r *refusal) text(code string) string {
	instance := r.instanceText()
	if code == forgeapi.CodeScopeInsufficient {
		return r.scoped(cmp.Or(r.Description, instance, r.Error))
	}
	return cmp.Or(instance, r.scoped(cmp.Or(r.Description, r.Error)))
}

// instanceText is the message member as text: a string as it came, and any other
// shape as its compact JSON, which keeps a field error's field name beside its
// complaint.
func (r *refusal) instanceText() string {
	if len(r.Message) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(r.Message, &text); err == nil {
		return text
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, r.Message); err != nil || compact.String() == "null" {
		return ""
	}
	return compact.String()
}

// scoped is text led by the scope an RFC 6750 body names, so the bound the caller's
// message is cut to can never cut the scope off behind a long description.
func (r *refusal) scoped(text string) string {
	if r.Scope == "" {
		return text
	}
	return strings.TrimSpace("(scope: " + r.Scope + ") " + text)
}

// sanitize bounds and rune-sanitizes untrusted upstream text at the emit site,
// because this library cannot know its consumer's log handler.
func sanitize(s string) string {
	out, _ := runesafe.SanitizeSingleLineCapped(s, maxMessageBytes, "...")
	return out
}

// quoter makes an instance's text from one exchange safe to carry on an error:
// every secret the request carried, its credential and each value of the
// connection's own headers, is redacted out of the text, the text made one line,
// the secrets redacted again, and the text bounded last.
//
// Both redactions are needed, because the sanitizer rewrites runes: it can turn a
// byte inside an echoed secret into a space, which only the first pass still
// matches, and it can turn a control byte into the space a secret carries, which
// only the second pass matches. The bound comes last so it cannot cut a secret into
// a prefix no needle matches. A secret is matched as it was sent and as JSON
// escapes it, since a message member that is not a string is quoted as its JSON.
func (c *Conn) quoter(token string) func(string) string {
	var needles []string
	for _, secret := range append([]string{token}, headerValues(c.headers)...) {
		if secret == "" {
			continue
		}
		needles = append(needles, secret)
		for _, escapeHTML := range []bool{false, true} {
			if form := jsonForm(secret, escapeHTML); form != secret {
				needles = append(needles, form)
			}
		}
	}
	slices.Sort(needles)
	needles = slices.Compact(needles)
	// The longest goes first, so a secret holding another as a substring is not
	// left partly unredacted by the shorter one's pass.
	slices.SortStableFunc(needles, func(a, b string) int { return cmp.Compare(len(b), len(a)) })
	redact := func(text string) string {
		for _, n := range needles {
			text = httpx.RedactSecretString(text, httpx.Secret(n))
		}
		return text
	}
	return func(text string) string {
		return sanitize(redact(runesafe.SanitizeSingleLine(redact(text))))
	}
}

// headerValues is the value of every header a connection sends of its own.
func headerValues(headers []forgeapi.Header) []string {
	values := make([]string, 0, len(headers))
	for _, h := range headers {
		values = append(values, h.Value)
	}
	return values
}

// jsonForm is s as a JSON string's contents spell it, with or without the HTML
// escapes an encoder may apply.
func jsonForm(s string, escapeHTML bool) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(escapeHTML)
	if err := enc.Encode(s); err != nil {
		return s
	}
	return strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(buf.String()), `"`), `"`)
}

// log is the per-request record: operation, family, sanitized instance, transport,
// status, kind, code, diagnostic id, method, attempt, duration, page, cache result
// and the upstream request id where the instance returned one. It never carries a
// token, a URL's userinfo, a header value or an unbounded body.
//
// The transport and the method are the exchange's own, so both are EMPTY on a line
// that describes a verdict this library reached rather than an exchange it had: a
// capability refusal sends nothing, and a line claiming a method and an arm for it
// describes a request that never existed.
//
// The page is the request's own, which is what a paged read has in hand; the
// attempt is the count the retry seam reported onto this operation's context, which
// is one where nothing was repeated.
//
// The diagnostic id is what makes the id on a user-visible error worth showing: the
// value is minted per failed operation and retained only here, so a line without it
// leaves the id a user reads out correlating with nothing. It is empty on the lines
// that carry no failure and on a context sentinel, which names no operation for an
// id to correlate.
func (c *Conn) log(ctx context.Context, req *Request, ex Exchange, status int, code, diag string, kind forgeapi.ErrorKind, header http.Header, started time.Time, err error) {
	level := slog.LevelDebug
	if status >= 400 || err != nil {
		level = slog.LevelWarn
	}
	// The record goes out on a fresh context rather than the operation's own,
	// because most of these lines describe a context that is already done: slog
	// hands the context to the handler, and a handler that honours cancellation
	// would drop exactly the failure line the diagnostic id has to correlate with.
	c.logger.Log(context.Background(), level, "forgeapi request",
		"op", req.Op,
		"family", c.family.String(),
		"instance", c.apiBase.Host,
		"transport", ex.arm,
		"status", status,
		"kind", kind.String(),
		"code", code,
		"diag_id", diag,
		"method", ex.method,
		"attempt", attempts(ctx),
		"page", max(req.Page, 1),
		"duration_ms", time.Since(started).Milliseconds(),
		"cache", cacheResult(status),
		"upstream_request_id", upstreamRequestID(header),
	)
}

// upstreamRequestIDHeaders is every name an in-scope product returns its own
// request id under, in the order the evidence rule reads them. A product that
// returns none leaves the attribute empty, which is not an omission.
var upstreamRequestIDHeaders = []string{"X-Request-Id", "X-GitHub-Request-Id", "X-Gitlab-Meta"}

// upstreamRequestID is the id the instance named for this exchange, empty where it
// named none.
func upstreamRequestID(header http.Header) string {
	for _, name := range upstreamRequestIDHeaders {
		if v := header.Get(name); v != "" {
			return sanitize(v)
		}
	}
	return ""
}

func cacheResult(status int) string {
	if status == http.StatusNotModified {
		return "not_modified"
	}
	return "miss"
}
