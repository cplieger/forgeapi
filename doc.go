// Package forgeapi holds the types, options and role interfaces shared by the
// github, gitlab and gitea clients for GitHub, GitLab, Gitea and Forgejo. The
// guides are at https://github.com/cplieger/forgeapi#documentation.
//
// Every operation in this package and in every family package answers nil, a
// [*Error], or an unchanged context.Canceled or context.DeadlineExceeded. Recover
// the pointer form with errors.As and branch on [Error.Code] and [Error.Kind],
// never on [Error.Message]. [Error] wraps nothing. Every enumeration's zero value
// is its unknown member, except [MergeIntent]'s.
//
// A role method's doc ends with a "Per forge:" block generated from one table:
// per product, the route, its request price and the capability that can refuse
// it, then each field the product departs on or that no measurement has read.
//
//go:generate go test -count=1 -run TestGeneratedFilesAreCurrent ./internal/gen -update
package forgeapi
