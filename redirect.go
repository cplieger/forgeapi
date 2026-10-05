package forgeapi

// MaxRedirectHops is how many redirect hops one request may take.
//
// It is this library's own number and it is not configurable. A non-nil redirect
// policy REPLACES net/http's default policy, whose only content is a ten-hop
// bound, so supplying our own removes that bound rather than adding to it: the
// cap is counted in this library's policy, beside the scheme, port, form and
// address checks. Exceeding it is refused with [CodeRedirectHopCap] and counted,
// never left to become an operation-deadline expiry reported as transient with
// nothing naming the cause.
const MaxRedirectHops = 5
