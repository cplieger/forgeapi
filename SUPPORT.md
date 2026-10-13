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

## Rate-limit answers

Every operation reports these answers as `KindRateLimited`, with `RetryAfter` set from the `Retry-After` header where the answer carries one.

| Product | Status | Header | Answer |
| --- | --- | --- | --- |
| GitHub | 403 | `X-RateLimit-Remaining: 0` | the primary or a secondary limit spent, with no budget left in the window |
| GitHub | 403 | `Retry-After`, any value | a secondary limit, whatever X-RateLimit-Remaining reads, as [GitHub documents it](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api) |
| GitHub | 429 | none: the status alone | a primary or a secondary limit |
| GitLab | 429 | none: the status alone | a rate limit |
| Gitea | 403 | `RateLimit-Remaining: 0` | a rate limit with no budget left in the window |
| Gitea | 429 | none: the status alone | a rate limit |
| Forgejo | 403 | `RateLimit-Remaining: 0` | a rate limit with no budget left in the window |
| Forgejo | 429 | none: the status alone | a rate limit |

## Conditional reads

The read cache revalidates a REST read whose earlier answer it holds, outside the reads [the read cache](docs/read-cache.md#what-is-never-cached) leaves out, and serves that answer only where the instance answers `304 Not Modified`. A product that sends no validator is never revalidated. The price is what one not-modified answer bills.

| Product | Validators | Price of a 304 | Documents | Answer |
| --- | --- | --- | --- | --- |
| GitHub | `ETag` on most REST routes, weak on the lists measured, and `Last-Modified` on many ([best practices](https://docs.github.com/en/rest/using-the-rest-api/best-practices-for-using-the-rest-api#use-conditional-requests-if-appropriate)) | 0 | not revalidated: a document is a POST ([forming calls](https://docs.github.com/en/graphql/guides/forming-calls-with-graphql#communicating-with-graphql)), and conditional requests for unsafe methods are unsupported (best practices) | a not-modified answer to a conditional request made with an `Authorization` header does not count against the primary rate limit ([best practices](https://docs.github.com/en/rest/using-the-rest-api/best-practices-for-using-the-rest-api#use-conditional-requests-if-appropriate)); measured with an authorized request, the answer carries `ETag`, `Vary`, `Cache-Control` and the `x-ratelimit-*` headers but no `Link` and no `X-OAuth-Scopes`, and two answers with one body and different `Link` headers carried one `ETag`, which therefore covers the body alone: a revalidated page of a list whose body states no total continues wherever it is full, and the identity read, whose scopes ride that header, is never revalidated |
| GitLab | a weak `ETag` on every REST read answered 200 with a body that sets no validator of its own, computed by Rack's ETag middleware over the body ([rack/etag.rb](https://github.com/rack/rack/blob/v2.2.23/lib/rack/etag.rb#L26-L63)), and no `Last-Modified` on the routes measured | 1 | not revalidated: a document is a POST; the ETag polling GitLab documents serves its own frontend routes ([polling](https://docs.gitlab.com/ee/development/polling.html)) | a not-modified answer is billed like any request ([rate limits](https://docs.gitlab.com/administration/settings/user_and_ip_rate_limits/)); measured with an authenticated request, `ratelimit-observed` rose across a 304 that carried `link`, `x-next-page`, `x-total` and `x-total-pages` as the 200 did, so revalidation saves the body's transfer, not the request |
| Gitea | none on the JSON API: its writer sets only `Content-Type` ([services/context/base.go](https://github.com/go-gitea/gitea/blob/v28.1.0/services/context/base.go#L114-L120)); the raw, media and archive download routes send them ([routers/common/serve.go](https://github.com/go-gitea/gitea/blob/v28.1.0/routers/common/serve.go#L20-L21), [routers/api/v1/repo/file.go](https://github.com/go-gitea/gitea/blob/v28.1.0/routers/api/v1/repo/file.go#L141), [services/repository/archiver/archiver.go](https://github.com/go-gitea/gitea/blob/v28.1.0/services/repository/archiver/archiver.go#L355-L408)), and this library reads none of them; measured on gitea.com | 1 | no GraphQL API: the API is the REST surface its swagger describes ([API usage](https://docs.gitea.com/development/api-usage)), mounted at `/api/v1` with no GraphQL route beside it ([routers/init.go](https://github.com/go-gitea/gitea/blob/v28.1.0/routers/init.go#L183-L185)) | no validator rides a JSON answer, so no read is revalidated, no not-modified answer arises, and the cache stays inert; the price of 1 is this library's own for a product whose API documentation states no exemption ([API usage](https://docs.gitea.com/development/api-usage)), and the product sends no budget signal (measured) |
| Forgejo | none on the JSON API: its writer sets only `Content-Type` ([services/context/base.go](https://codeberg.org/forgejo/forgejo/src/tag/v16.0.5/services/context/base.go#L137-L148)); the raw, media and archive download routes send them ([routers/common/serve.go](https://codeberg.org/forgejo/forgejo/src/tag/v16.0.5/routers/common/serve.go#L18-L19), [routers/api/v1/repo/file.go](https://codeberg.org/forgejo/forgejo/src/tag/v16.0.5/routers/api/v1/repo/file.go#L147) and [#L371-L374](https://codeberg.org/forgejo/forgejo/src/tag/v16.0.5/routers/api/v1/repo/file.go#L371-L374)), and this library reads none of them; measured on codeberg.org | 1 | no GraphQL API: the API is the REST surface its swagger describes ([API usage](https://forgejo.org/docs/latest/user/api-usage/)), mounted at `/api/v1` and `/api/forgejo/v1`, both REST ([routers/init.go](https://codeberg.org/forgejo/forgejo/src/tag/v16.0.5/routers/init.go#L180-L181)) | no validator rides a JSON answer, so no read is revalidated, no not-modified answer arises, and the cache stays inert; the price of 1 is this library's own for a product whose API documentation states no exemption ([API usage](https://forgejo.org/docs/latest/user/api-usage/)), and codeberg.org meters every request (`ratelimit: "baseline";r=1999;t=600`, measured) |
