package github

import "github.com/cplieger/forgeapi"

// The roles this family claims, asserted at compile time beside the type that
// claims them. This is where a method added to a published role stops compiling,
// and where a method renamed or dropped on this side does too: the opt-in
// alternative, a consumer type-asserting at run time, leaves both silently
// unimplemented with nothing red anywhere.
//
// This product has a release object and pull-request labels, so it claims the two
// OPTIONAL roles as well. A family that could not serve one would not implement
// it at all, and the absence would be a type fact rather than a stub returning an
// error. That absence has no compile-time form, because the assertion that would
// state it is the one that fails the build. The negative half is therefore a runtime
// type assertion in a test, over a declared stand-in, since no family in scope is
// missing a role.
var (
	_ forgeapi.Identity     = (*Client)(nil)
	_ forgeapi.Repos        = (*Client)(nil)
	_ forgeapi.PullRequests = (*Client)(nil)
	_ forgeapi.Merges       = (*Client)(nil)
	_ forgeapi.Checks       = (*Client)(nil)
	_ forgeapi.Issues       = (*Client)(nil)
	_ forgeapi.Capabilities = (*Client)(nil)
	_ forgeapi.Governor     = (*Client)(nil)
	_ forgeapi.Releases     = (*Client)(nil)
	_ forgeapi.Labels       = (*Client)(nil)
)
