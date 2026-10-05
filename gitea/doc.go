// Package gitea is the forgeapi client for Gitea and Forgejo, two products that
// share one API. [Client] is its one exported type, so no wire type of either
// product reaches a forgeapi signature.
//
// Neither product has a GraphQL API, so every operation is REST and pays in
// requests for what a document carries on the other families.
// [Client.CommitStatus] states the largest such cost.
package gitea
