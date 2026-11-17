package families_test

import (
	"context"
	"testing"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/forgeapi/v3/families"
)

// Open's signature is pinned here rather than only reviewed, because its return
// type is the whole of what the factory promises: widening it past the
// intersection would promise a role that depends on which family answered, and
// narrowing it would take one away.
var _ func(context.Context, forgeapi.Connection, ...forgeapi.Option) (forgeapi.Core, forgeapi.Family, error) = families.Open

// narrowFamily stands in for a family package whose product has no release object.
// It embeds the intersection rather than writing its methods out, so moving a
// release operation into Core hands it the promoted method and turns the test below
// red.
type narrowFamily struct{ forgeapi.Core }

// The positive half is compile-only, and it is also the claim that such a family
// is openable: this is the type [families.Open] returns.
var _ forgeapi.Core = narrowFamily{}

// Every family serves the label role, so it is part of the intersection.
var _ forgeapi.Labels = forgeapi.Core(nil)

// The two release operations, each as a one-method interface with the signature
// its role declares, so one method leaking into Core is caught where the
// two-method role assertion stays green.
type (
	listReleasesOp interface {
		ListReleases(ctx context.Context, repo forgeapi.RepoRef, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.Release], error)
	}
	createReleaseOp interface {
		CreateRelease(ctx context.Context, repo forgeapi.RepoRef, req forgeapi.NewRelease) (forgeapi.Release, error)
	}
)

// Each copy is bound to the role that owns it at compile time, so a signature
// change fails the build here rather than leaving a stale copy that no leak can
// satisfy.
var (
	_ listReleasesOp  = forgeapi.Releases(nil)
	_ createReleaseOp = forgeapi.Releases(nil)
)

// TestNarrowFamilyLacksTheOptionalRole is the negative half of the role contract,
// a runtime assertion because Go has no compile-time form for "does not
// implement": a family whose product has no release object fails the assertion
// rather than offering methods that always fail.
func TestNarrowFamilyLacksTheOptionalRole(t *testing.T) {
	var client forgeapi.Core = narrowFamily{}
	if _, ok := any(client).(forgeapi.Releases); ok {
		t.Error("narrowFamily satisfies forgeapi.Releases through forgeapi.Core, want it not to: a family whose product has no release object must fail to satisfy the role rather than stub it")
	}
	if _, ok := any(client).(listReleasesOp); ok {
		t.Error("narrowFamily has ListReleases through forgeapi.Core, want it not to: one operation of the optional role is as far outside the intersection as the whole role")
	}
	if _, ok := any(client).(createReleaseOp); ok {
		t.Error("narrowFamily has CreateRelease through forgeapi.Core, want it not to: one operation of the optional role is as far outside the intersection as the whole role")
	}
}
