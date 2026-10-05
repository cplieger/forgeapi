# Request prices and the budget

This page covers what each call costs in requests and how a client paces itself against a forge's rate limit. Read it if your tool polls several repositories or shares a token with other tools.

## Request prices

Each operation's godoc publishes what the call costs on each product, under the line `Per forge:`, as one figure or as a range. A range means the operation has more than one path, such as a cached answer against a fresh read, a paginated list against a single page, or a further read that a moved repository forces. A call never spends more than its operation's published ceiling, plus the connection reads described below where the connection does not hold them yet. The test suite checks the figure for each operation on each product.

## The per-product block

The same `Per forge:` block names the capability a detection can refuse the call on. It also lists every field where that product departs from the normalized contract, because it cannot supply the field, supplies another shape or spells the value differently. A field arriving exactly as the contract states has no row, which keeps the blocks short. The blocks and [SUPPORT.md](../SUPPORT.md) are generated from one table, so they cannot disagree.

## Connection reads

What a connection learns about its own instance is priced on `Capabilities.ConnectionCaps`, not on the operation that happens to need it. So every other figure is an operation's cost on a connection that already holds those facts.

On Gitea and Forgejo, that includes the page size the instance states, `max_response_items` from `GET /api/v1/settings/api`. Every page the family asks for is at most that size, so a short page is the end of a list.

An operation that needs a connection read on a connection that has not made it yet makes the read first. The request belongs to the connection, though that call's `BudgetState.LastCost` counts it. Where that read fails, the operation fails with its error and sends nothing more. Where the budget holds the read back, a list answers the empty page a deferral answers, marked `PartialRateLimited`.

Detecting the family with `families.Open` has its own cost, described in [Connecting to a forge](connecting.md#detecting-the-family).

## The budget

`Budget`, `BudgetState` and `RotationCursor` describe how a client spends requests. The client paces itself from what a response reports. On GitHub and GitLab, that figure is the whole account's quota, not this client's share of it.

- `WithMutationReserve` sets how much budget reads leave for writes. A read that would take `BudgetState.Remaining` below the reserve is deferred and marked `PartialRateLimited`.
- A client rotates expensive reads over a window of its own keeping. Store the `RotationCursor` it reports and pass it back with `WithRotationCursor` when you build the next client.
- `BudgetRemainingUnknown` is what a product that reports nothing answers. Gitea is that product, so the reserve has no effect there.
- `DefaultBudget` publishes the defaults, including the deadline each operation runs under.
