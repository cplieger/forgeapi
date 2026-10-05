// Package github is the forgeapi client for GitHub: github.com and Enterprise
// Server, served by the same code. [Client] is its one exported type, so no wire
// type of this product reaches a forgeapi signature.
//
// Where this product differs from the others, the method concerned says so:
// [Client.MergePR] is asynchronous, [Client.ListMyPRs] and [Client.ListMyIssues]
// read a search, and [APIVersionHeader] states how the REST version is pinned.
package github
