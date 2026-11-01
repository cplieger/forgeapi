# Request prices and the budget

This page covers what each call costs in requests and how a client paces itself against a forge's rate limit. Read it if your tool polls several repositories or shares a token with other tools.

## Request prices

Each operation's godoc publishes what the call costs on each product, under the line `Per forge:`, as one figure or as a range. A range means the operation has more than one path, such as a cached answer against a fresh read, a paginated list against a single page, or a further read that a moved repository forces. A call never spends more than its operation's published ceiling, plus the connection reads described below where the connection does not hold them yet. The test suite checks the figure for each operation on each product.

On GitLab, an owner named by digits alone costs one more request on every page of `ListRepos`, `ListMyPRs` and `ListMyIssues`. Every GitLab namespace route reads such a name as a numeric id, so the library first resolves the name to the id of the group or user that holds it.

On GitHub, the run route serves one created filter through a result window of 1,000 runs, ending with the whole page that reaches it. A `ListRuns` walk under `WithSince` that goes past the window costs more pages than its runs fill. Each page still costs what the block states. But each time the walk narrows its created range, it reads the runs of one second twice, so it pays once more for the pages those runs fill.

## Revalidated reads

A published figure counts the requests the instance bills. On GitHub, a `304 Not Modified` answer to an authorized conditional request bills nothing, so an authorized REST read the [read cache](read-cache.md) answers from a held copy costs `0`, and so does a call answered whole that way. Those GitHub cells publish a range starting at `0`. A failed attempt still counts, and on every other product a `304` counts as one request.

## The per-product block

The same `Per forge:` block names the capability a detection can refuse the call on. It also lists every field where that product departs from the normalized contract, because it cannot supply the field, supplies another shape or spells the value differently. A field arriving exactly as the contract states has no row, which keeps the blocks short. The blocks and [SUPPORT.md](../SUPPORT.md) are generated from one table, so they cannot disagree.

## Connection reads

What a connection learns about its own instance is priced on `Capabilities.ConnectionCaps`, not on the operation that happens to need it. So every other figure is an operation's cost on a connection that already holds those facts.

On Gitea and Forgejo, that includes the page size the instance states, `max_response_items` from `GET /api/v1/settings/api`. Every page the family asks for is at most that size, so a short page is the end of a list. Two lists are the exception. The run listing continues from the total its body states. `ListRepos` under an owner continues from the `X-Total-Count` header, because that route drops the rows the caller cannot read after it pages.

An operation that needs a connection read on a connection that has not made it yet makes the read first. The request belongs to the connection, though that call's `BudgetState.LastCost` counts it. Where that read fails, the operation fails with its error and sends nothing more. Where the budget holds the read back, a list answers the empty page a deferral answers, marked `PartialRateLimited`.

Detecting the family with `families.Open` has its own cost, described in [Connecting to a forge](connecting.md#detecting-the-family).

## The budget

`Budget`, `BudgetState` and `RotationCursor` describe how a client spends requests. The client paces itself from what a response reports. On GitHub and GitLab, that figure is the whole account's quota, not this client's share of it.

- `WithMutationReserve` sets how much budget reads leave for writes. A read is deferred and marked `PartialRateLimited` while the rate-limit pool it draws on is at the reserve or below. From that pool's reset on, reads are sent again, and the next response reports the renewed budget. GitHub and GitLab name the pool each response drew on, and a route can draw on a pool of its own: on GitHub, GraphQL documents draw on another pool than REST requests, and on gitlab.com the project listings draw on a pool of 100 requests a minute. So a read is held against the pool its operation drew on last, and an operation that has had no answer yet has no pool of its own to be held against. After a deferral, `BudgetState` reports the pool that held the read. A pool whose whole limit is the reserve or less is never held, because it would defer every read after its first of each window. GitLab's pools also overlap: a request under a gitlab.com burst limit or a protected-path throttle counts against the authenticated API throttle too, though its headers name only the narrower limit. Only the packages, files and deprecated API throttles take a request apart from it. So on GitLab every read is also held against the authenticated API throttle's last figure, and each answer naming another limit takes one request off that figure until an answer reports it again. That includes the answer to an attempt that was retried and to a redirect that was followed, because each was a request GitLab counted. Forgejo reports one figure for every route, so every read is held against it.
- A client rotates expensive reads over a window of its own keeping. Store the `RotationCursor` it reports and pass it back with `WithRotationCursor` when you build the next client.
- `BudgetState.Remaining` counts in the instance's own units, requests on a REST endpoint and points on a GraphQL endpoint. A `304` answer still updates it from its own rate headers, except the one the [read cache](read-cache.md) cannot vouch for and sends once more.
- `BudgetRemainingUnknown` is what a product that reports nothing answers. Gitea is that product, so the reserve has no effect there.
- `DefaultBudget` publishes the defaults, including the deadline each operation runs under.
