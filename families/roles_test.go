package families_test

import (
	"context"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/families"
)

// Open's signature is pinned here rather than only reviewed, because its return
// type is the whole of what the factory promises: widening it past the
// intersection would promise a role that depends on which family answered, and
// narrowing it would take one away.
var _ func(context.Context, forgeapi.Connection, ...forgeapi.Option) (forgeapi.Core, forgeapi.Family, error) = families.Open

// narrowFamily stands in for a fourth family package whose product has neither a
// release object nor pull-request labels. No such family is in scope, so there is
// no client to assert against, and all three families that ARE in scope claim
// every role, which is why the negative half of the contract needs a stand-in at
// all.
//
// It is declared by EMBEDDING the intersection interface rather than by writing
// nineteen methods out, and that is what makes it sensitive to the property under
// test rather than merely shaped like a family: moving either optional role into
// the intersection would hand this type the promoted methods and turn the test
// below red, which is exactly the change that must not happen quietly.
type narrowFamily struct{ forgeapi.Core }

// The positive half is compile-only, and it is also the claim that such a family
// is openable: this is the type [families.Open] returns.
var _ forgeapi.Core = narrowFamily{}

// The three optional OPERATIONS, each as a one-method interface carrying the
// exact signature its role declares.
//
// They exist because the whole-role assertions below cannot see the smaller leak:
// a Core that gained ListReleases alone still lacks CreateRelease, so it still
// fails to satisfy the two-method [forgeapi.Releases] and every assertion stays
// green while the intersection has silently grown an operation no family without
// releases can serve. Measured before these interfaces existed: adding
// ListReleases to Core left this test passing. The per-operation form is what
// the role rule for the exported surface actually says, since the rule is about what a
// family can DO and not about how the roles are grouped.
type (
	listReleasesOp interface {
		ListReleases(ctx context.Context, repo forgeapi.RepoRef, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.Release], error)
	}
	createReleaseOp interface {
		CreateRelease(ctx context.Context, repo forgeapi.RepoRef, req forgeapi.NewRelease) (forgeapi.Release, error)
	}
	listLabelsOp interface {
		ListLabels(ctx context.Context, repo forgeapi.RepoRef, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.Label], error)
	}
)

// Each of those three signatures is a COPY of the role's, so each is an oracle
// that can go stale, and the runtime assertions below then pass while the leak
// they exist to catch is live: measured on a copy of the tree, changing
// ListReleases in [forgeapi.Releases] and all three clients and adding the
// changed method to [forgeapi.Core] left every assertion green, because the
// leaked method no longer satisfied the old one-method interface and the type
// still lacked CreateRelease.
//
// These bind each copy to the role that owns it at COMPILE time, so a signature
// change fails the build here naming the method and printing both signatures,
// and the oracle cannot drift away from its subject in the same edit that leaks
// it. Assigning the ROLE interface rather than a family client is what makes
// this a statement about the declared surface rather than about one
// implementation.
var (
	_ listReleasesOp  = forgeapi.Releases(nil)
	_ createReleaseOp = forgeapi.Releases(nil)
	_ listLabelsOp    = forgeapi.Labels(nil)
)

// TestNarrowFamilyLacksTheOptionalRoles is the NEGATIVE half of the role
// contract, as a runtime type assertion because Go has no compile-time form for
// it: the construct that would state "this type does not implement Releases" is
// the assertion that fails the build.
//
// It is what makes "a family that cannot serve a role does not implement it"
// checkable at the surface a consumer actually receives. Without it, an optional
// role could drift into the intersection and every family would satisfy it by
// promotion with nothing red anywhere.
//
// It is held at BOTH granularities. The two whole-role assertions catch a role
// moving into Core; the three per-operation assertions catch one method moving,
// which the whole-role pair cannot see because a partial role is still not the
// role.
func TestNarrowFamilyLacksTheOptionalRoles(t *testing.T) {
	var client forgeapi.Core = narrowFamily{}
	if _, ok := any(client).(forgeapi.Releases); ok {
		t.Error("narrowFamily satisfies forgeapi.Releases through forgeapi.Core, want it not to: a family whose product has no release object must fail to satisfy the role rather than stub it, which it cannot do if Releases is reachable from the intersection")
	}
	if _, ok := any(client).(forgeapi.Labels); ok {
		t.Error("narrowFamily satisfies forgeapi.Labels through forgeapi.Core, want it not to: a family whose product has no pull-request labels must fail to satisfy the role rather than stub it, which it cannot do if Labels is reachable from the intersection")
	}
	if _, ok := any(client).(listReleasesOp); ok {
		t.Error("narrowFamily has ListReleases through forgeapi.Core, want it not to: one operation of an optional role is as far outside the intersection as the whole role, and a family whose product has no release object cannot serve it")
	}
	if _, ok := any(client).(createReleaseOp); ok {
		t.Error("narrowFamily has CreateRelease through forgeapi.Core, want it not to: one operation of an optional role is as far outside the intersection as the whole role, and a family whose product has no release object cannot serve it")
	}
	if _, ok := any(client).(listLabelsOp); ok {
		t.Error("narrowFamily has ListLabels through forgeapi.Core, want it not to: one operation of an optional role is as far outside the intersection as the whole role, and a family whose product has no pull-request labels cannot serve it")
	}
}
