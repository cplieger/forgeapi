// Package gitlab is the forgeapi client for GitLab: gitlab.com and self-managed
// instances, served by the same code. [Client] is its one exported type, so no
// wire type of this product, the merge request included, reaches a forgeapi
// signature.
//
// Reads use this family's GraphQL documents where the instance accepts them and
// REST where it does not. [Client.ConnectionCaps] states how a connection
// decides, and each method states what its own path costs.
package gitlab
