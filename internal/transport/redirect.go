package transport

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/ssrf/v4"
)

// redirectPolicy is this library's OWN redirect policy, on the client rather than
// on the transport.
//
// Neither dependency's policy can be it. Go's default forwards every sensitive
// header class on a hop it considers same-host and its match is the HOSTNAME
// alone, so a subdomain change, a scheme downgrade and a port change all pass
// through. One library's policy caps hops at ten through an unexported constant,
// and the other's per-URL predicate never sees the hop chain, so neither can
// supply this library's cap at its own number.
//
// Per hop it performs the scheme check against this connection's scheme set, the
// port check against its allowlist, the empty-host and canonical-form check, and
// the address verdict from this connection's own policy value. It does not RESOLVE
// inside the check, because that would judge one answer and dial another: a
// name-addressed hop takes its form checks here and its address verdict at the
// socket, from the same policy value inside the transport's control hook.
func (c *Conn) redirectPolicy(web *url.URL, set *forgeapi.Settings) func(*http.Request, []*http.Request) error {
	ports := allowedPorts(web, set)
	origin := originOf(web)
	standard := !set.PrivateAddresses && !set.PlaintextHTTP
	policy := ssrf.NewURLPolicy(schemesOf(set)...)

	return func(req *http.Request, via []*http.Request) error {
		if err := c.checkHop(req, via, set, ports, standard, policy); err != nil {
			return err
		}
		if originOf(req.URL) != origin {
			dropOnHop(req, c.headers)
		}
		return nil
	}
}

// checkHop is the per-hop verdict: the form checks, the address policy, and the two
// refusals a mutating method earns, in that order.
func (c *Conn) checkHop(req *http.Request, via []*http.Request, set *forgeapi.Settings, ports []uint16, standard bool, policy ssrf.URLPolicy) error {
	if err := c.checkHopForm(req, via, set, ports); err != nil {
		return err
	}
	// The standard URL validation is called only where this connection's address
	// policy IS the default public-only one, the single posture whose
	// unconditional refusals and the connection's own stance coincide.
	if standard {
		if err := c.checkAddress(req, via, policy); err != nil {
			return err
		}
	}
	return c.checkHopMethod(req, via)
}

// checkHopForm is the half of the verdict taken on the hop's own shape: the hop cap,
// the host, the downgrade, the scheme and the port.
func (c *Conn) checkHopForm(req *http.Request, via []*http.Request, set *forgeapi.Settings, ports []uint16) error {
	if len(via) >= forgeapi.MaxRedirectHops {
		if c.counters.RedirectHopCapExceeded != nil {
			c.counters.RedirectHopCapExceeded(c.family)
		}
		return c.fail(opOf(via), forgeapi.CodeRedirectHopCap, forgeapi.KindUpstream, 0,
			"redirect chain is over this library's hop cap of "+strconv.Itoa(forgeapi.MaxRedirectHops))
	}
	if req.URL.Host == "" {
		return c.fail(opOf(via), forgeapi.CodeConnectionInvalid, forgeapi.KindUpstream, 0, "redirect hop carries no host")
	}
	previous := via[len(via)-1]
	if previous.URL.Scheme == schemeHTTPS && req.URL.Scheme == schemeHTTP {
		return c.fail(opOf(via), forgeapi.CodePlaintextRefused, forgeapi.KindUpstream, 0,
			"redirect hop downgrades from https to http")
	}
	if !allowedScheme(req.URL.Scheme, set) {
		return c.fail(opOf(via), forgeapi.CodeConnectionInvalid, forgeapi.KindUpstream, 0,
			"redirect hop carries a scheme this connection does not permit")
	}
	if !portAllowed(req.URL, ports) {
		if c.counters.RedirectPortRefused != nil {
			c.counters.RedirectPortRefused(c.family)
		}
		return c.fail(opOf(via), forgeapi.CodeRedirectPortRefused, forgeapi.KindUpstream, 0,
			"redirect hop carries a port outside this connection's allowlist")
	}
	return nil
}

// checkHopMethod is the half a MUTATING method earns, and it is two refusals rather
// than one because the three statuses net/http rewrites and the two that preserve the
// body are different hazards.
func (c *Conn) checkHopMethod(req *http.Request, via []*http.Request) error {
	previous := via[len(via)-1]
	// A hop net/http would rewrite into a bodyless read is REFUSED rather
	// than followed, because a merge against a renamed repository would
	// otherwise answer 2xx from a read that merged nothing.
	if rewritable(previous.Method, statusOf(req)) {
		return c.fail(opOf(via), forgeapi.CodeRepoRefStale, forgeapi.KindNotFound, 0,
			"redirect answered to a mutating method would be rewritten into a bodyless read")
	}
	// A hop that PRESERVES the method is refused off origin, which the
	// rewrite check above cannot cover: the two statuses that carry the
	// method also carry the BODY, so following one sends the caller's
	// creation, merge or close to a host the consumer never named and
	// returns that host's answer as the operation's own. Dropping the
	// credential and the consumer's headers is not enough, because the
	// request itself is the thing that must not travel.
	if mutating(previous.Method) && originOf(req.URL) != originOf(previous.URL) {
		return c.fail(opOf(via), forgeapi.CodeRepoRefStale, forgeapi.KindNotFound, 0,
			"redirect answered to a mutating method leaves this connection's origin")
	}
	return nil
}

// checkAddress is the form half of the address verdict, taken at the hop. The
// verdict at the SOCKET is the same policy value inside the transport's control
// hook, which is why nothing resolves here: resolving would judge one answer and
// dial another.
func (c *Conn) checkAddress(req *http.Request, via []*http.Request, policy ssrf.URLPolicy) error {
	err := policy.Validate(req.URL.String())
	if err == nil {
		return nil
	}
	kind := "unknown"
	if se, ok := errors.AsType[*ssrf.Error](err); ok {
		kind = strconv.Itoa(int(se.Kind))
	}
	if c.counters.AddressRefusal != nil {
		c.counters.AddressRefusal(c.family, kind)
	}
	return c.fail(opOf(via), forgeapi.CodePrivateAddressRefused, forgeapi.KindUpstream, 0,
		"redirect hop does not pass this connection's address policy")
}

// statusOf recovers the status of the response that produced this hop. net/http
// does not hand the policy the response, so the status rides the hop's own
// method: a rewritten hop arrives as a GET where the request before it was not
// one, which is exactly the class this library refuses.
func statusOf(req *http.Request) int {
	if req.Method == http.MethodGet {
		return http.StatusSeeOther
	}
	return http.StatusTemporaryRedirect
}

// rewritable reports whether this hop is one of the three statuses net/http
// downgrades to a bodyless method. It reads the rewrite that already happened
// rather than a status the policy is not given.
func rewritable(previousMethod string, status int) bool {
	if !mutating(previousMethod) {
		return false
	}
	return status == http.StatusMovedPermanently ||
		status == http.StatusFound ||
		status == http.StatusSeeOther
}

// mutating reports whether a method carries a body a redirect must not replay
// elsewhere.
func mutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// dropOnHop removes the credential and every consumer-set header on a hop whose
// scheme, host or port differs from the connection's, so a header only ever
// reaches the instance the user named.
func dropOnHop(req *http.Request, headers []forgeapi.Header) {
	req.Header.Del("Authorization")
	for _, h := range headers {
		req.Header.Del(h.Name)
	}
}

// originOf is scheme, host and port together, which is the comparison this policy
// makes: a hostname-only match is what Go's own default gets wrong.
func originOf(u *url.URL) string {
	port := u.Port()
	if port == "" {
		port = defaultPort(u.Scheme)
	}
	return u.Scheme + "://" + strings.ToLower(u.Hostname()) + ":" + port
}

func defaultPort(scheme string) string {
	if scheme == schemeHTTP {
		return "80"
	}
	return "443"
}

func allowedScheme(scheme string, set *forgeapi.Settings) bool {
	if scheme == schemeHTTPS {
		return true
	}
	return scheme == schemeHTTP && set.PlaintextHTTP
}

func schemesOf(set *forgeapi.Settings) []string {
	if set.PlaintextHTTP {
		return []string{schemeHTTPS, schemeHTTP}
	}
	return []string{schemeHTTPS}
}

func portAllowed(u *url.URL, ports []uint16) bool {
	port := u.Port()
	if port == "" {
		port = defaultPort(u.Scheme)
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return false
	}
	return slices.Contains(ports, uint16(n))
}

// opOf names the operation a refused hop belongs to. The policy is not told, so
// the value is empty and the caller's own error carries the operation: a redirect
// refusal reaches the caller wrapped by the request that made it.
func opOf([]*http.Request) string { return "" }
