# Support matrix

<!-- Generated from internal/spec by internal/gen. Edit the table, then run go generate ./... -->

This table has one row per operation and one column per forge product. Each operation returns the same types on every product, and what those types promise is the normalized contract.

`pending` says the library claims no support on that product yet. Either no family implements the operation there, or the measured instance answered it in a way the normalized contract does not cover. In that second case, the operation's godoc states the difference field by field.

`unsupported` says the product has no such operation, and no implementation can change that.

Where a product departs from the normalized contract, the operation's own godoc on [pkg.go.dev](https://pkg.go.dev/github.com/cplieger/forgeapi) lists each departure, so this table holds only the status.

| Operation | GitHub | GitLab | Gitea | Forgejo |
| --- | --- | --- | --- | --- |
| `Identity.Whoami` | supported | supported | supported | supported |
| `Repos.ListRepos` | supported | supported | supported | supported |
| `PullRequests.ListPRs` | supported | supported | supported | supported |
| `PullRequests.ListMyPRs` | supported | supported | supported | supported |
| `PullRequests.ReadPR` | supported | supported | supported | supported |
| `PullRequests.CreatePR` | supported | supported | supported | supported |
| `PullRequests.ClosePR` | supported | supported | supported | supported |
| `PullRequests.ReopenPR` | supported | supported | supported | supported |
| `PullRequests.RerunFailedChecks` | supported | supported | supported | unsupported |
| `Merges.MergePR` | supported | supported | supported | supported |
| `Merges.MergeStatus` | supported | supported | supported | supported |
| `Checks.CommitStatus` | supported | supported | supported | supported |
| `Checks.ListRuns` | supported | supported | supported | supported |
| `Issues.ListIssues` | supported | supported | supported | supported |
| `Issues.ListMyIssues` | supported | supported | supported | supported |
| `Issues.CreateIssue` | supported | supported | supported | supported |
| `Issues.CloseIssue` | supported | supported | supported | supported |
| `Capabilities.ConnectionCaps` | supported | supported | supported | supported |
| `Capabilities.GrantCaps` | supported | supported | supported | supported |
| `Capabilities.RepoAffordances` | supported | supported | supported | supported |
| `Governor.BudgetState` | supported | supported | supported | supported |
| `Releases.ListReleases` | supported | supported | supported | supported |
| `Releases.CreateRelease` | supported | supported | supported | supported |
| `Labels.ListLabels` | supported | supported | supported | supported |
