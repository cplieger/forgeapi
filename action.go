package forgeapi

import (
	"context"
	"net/http"

	"github.com/cplieger/forgeapi/v3/internal/callmark"
)

// ActionReads marks ctx for the reads one consumer action makes around its
// write: a precondition read before it and the read-back after it. Up to n
// reads made under the returned context, across goroutines, pass the
// [WithMutationReserve] reserve; later ones are governed as any read. A pool the
// forge reports at zero still defers the read with no request sent, a client
// built with WithMutations(false) ignores the mark, and a nested ActionReads
// keeps the outer bound. n is clamped to 1..20.
func ActionReads(ctx context.Context, n int) context.Context {
	return callmark.WithActionReads(ctx, n)
}

// Mutates reports whether r, as a [WithAttemptObserver] or [WithRequestObserver]
// hook sees it, is a request that changes forge state: the REST POST, PATCH, PUT
// or DELETE a mutating operation sends as its write, or a GraphQL mutation
// document. It is false for every read: a GraphQL query, which GitHub sends as a
// POST, and the reads a mutating operation makes around its write (a label
// pre-read, a node-id read, a merge's own status read). forgeapi marks the
// context of each request it builds, so the answer never depends on the method
// or the body, and a request forgeapi did not build answers false.
func Mutates(r *http.Request) bool {
	return callmark.IsWrite(r.Context())
}
