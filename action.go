package forgeapi

import (
	"context"
	"net/http"

	"github.com/cplieger/forgeapi/v3/internal/callmark"
)

// ActionReads marks ctx for the reads one consumer action makes around its
// write. Up to n reads under the returned context, across goroutines, pass the
// [WithMutationReserve] reserve; later ones are governed as any read. A pool at
// zero still defers, a client built with WithMutations(false) ignores the mark,
// a nested ActionReads keeps the outer bound, and n is clamped to 1..20.
func ActionReads(ctx context.Context, n int) context.Context {
	return callmark.WithActionReads(ctx, n)
}

// Mutates reports whether r, as a [WithAttemptObserver] or [WithRequestObserver]
// hook sees it, changes forge state: a REST write or a GraphQL mutation. A
// GraphQL query sent as a POST, the reads a mutating operation makes around its
// write, and a request forgeapi did not build answer false; forgeapi marks the
// context of each write it builds, so the method and body decide nothing.
func Mutates(r *http.Request) bool {
	return callmark.IsWrite(r.Context())
}
