# The read cache

Every client keeps a read cache in its own process memory. It is on by default, and it changes what a read costs, never what it answers: a held answer is served only when the instance confirms, in the same exchange, that it is unchanged. Read this page if your tool polls, shares a token with other tools, or needs to bound or switch off what a client holds.

## Why reads are revalidated

Releases before this one sent every read unconditionally. A poller that shares a user's quota then spends most of it re-reading lists that have not changed. Measured on one account with 77 repositories and a 15-minute scan, the run listing alone took about a third of GitHub's 5,000 requests an hour. GitHub does not count a conditional request answered `304 Not Modified` against the primary rate limit when the request carried an `Authorization` header, so the same scan can cost a fraction of that.

No answer is served on age alone. Every read reaches the instance, and only its `304` lets the client answer from the held copy.

## What it does

A REST read whose earlier answer the cache holds carries that answer's validator, `If-None-Match` from its `ETag` or `If-Modified-Since` from its `Last-Modified`, unless it is one of the reads [What is never cached](#what-is-never-cached) lists. On a `304` that vouches for the held copy, the client answers from it. The rest of this page says when a `304` does not.

A held copy keeps the answer's body and only the few headers the library reads from an answer: `Link`, and the paging, total and product headers (`x-next-page`, `X-Total-Count`, `X-Gitlab-Meta`, `X-Gitea-Version`, `X-Forgejo-Version`). Every other header of a served answer, the rate headers and any cookie included, is the `304`'s own. A held header the `304` repeats takes the `304`'s value. Where the `304` leaves out a held paging, total or product header, the held copy is not served and the read is sent once more without a validator. `Link` keeps its held value where the `304` leaves it out, and the validator may not vouch for `Link`. GitHub's `304` carries no `Link` and no `X-OAuth-Scopes`, and its `ETag` covers the body alone:

- A revalidated GitHub list page that is full continues to the next page, whose own read answers whether rows lie beyond it. A page that is not full ends the list, since a row added after it would change its body.
- The GitHub identity read, `Whoami`, is never revalidated, because its scopes ride that header and are never held.
- GitLab's `304` repeats its paging and total headers, measured, so its lists read them as current.

What that saves depends on the product. [SUPPORT.md](../SUPPORT.md) states, per product, which validators each sends and what a `304` bills.

- On GitHub, a `304` to an authorized request carrying a validator bills nothing, so an unchanged read costs no request.
- On GitLab, a `304` is billed like any request. Revalidation saves the body's transfer there, and the client decodes only the smaller held copy, but it does not save the request.
- Gitea and Forgejo send no validator on their JSON API, so the cache never engages there and every read costs what it did before.

## Reads of only what changed

The cache does not ask a forge for only the rows changed since an earlier read, although GitLab ([`updated_after`](https://docs.gitlab.com/api/issues/), also on [merge requests](https://docs.gitlab.com/api/merge_requests/)), Gitea ([`since`](https://github.com/go-gitea/gitea/blob/v28.1.0/routers/api/v1/repo/issue.go#L293-L295)), Forgejo ([`since`](https://codeberg.org/forgejo/forgejo/src/tag/v16.0.5/routers/api/v1/repo/issue.go#L381)) and GitHub ([`since`](https://docs.github.com/en/rest/issues/issues#list-repository-issues) on the issue list, [`updated:`](https://docs.github.com/en/search-github/searching-on-github/searching-issues-and-pull-requests#search-by-when-an-issue-or-pull-request-was-created-or-last-updated) in search) offer such filters. This was considered and rejected, because a list built from changes alone can keep a row the forge has closed or deleted, with no way to notice:

- No forge guarantees that every change moves a row's update time forward. On GitLab, an administrator or project owner may set an issue's update time when editing it ([update an issue](https://docs.gitlab.com/api/issues/#update-an-issue)). On Forgejo, any issue edit may name its own update time ([modules/structs/issue.go](https://codeberg.org/forgejo/forgejo/src/tag/v16.0.5/modules/structs/issue.go#L116-L117)), and the update writes it as given ([models/issues/issue_update.go](https://codeberg.org/forgejo/forgejo/src/tag/v16.0.5/models/issues/issue_update.go#L37-L44)). A close dated before the last read then never appears among the changes.
- No forge lists the rows deleted or hidden since an instant: each of the filters above selects by update time among the rows that still exist and are visible, so a deletion leaves no trace in a read of changes.

Every list page is therefore read in full, and revalidated where the forge sends a validator.

## What is never cached

- A write, and every read made inside a write, such as the read a merge makes before it merges.
- A GraphQL document. Documents are POST requests, which conditional requests do not cover. On GitHub, documents also bill a separate points pool.
- An error. Any answer that is not kept forgets the held answer for its request: a refusal, a failure in transit, a body that cannot be read or decoded, and every case below.
- An answer with no validator, or one whose `Cache-Control` says `no-store`. A `304` that says `no-store` answers its call from the held copy, which is then forgotten. An `ETag` counts as a validator only when it is a single entity-tag, and a `Last-Modified` only when it is an HTTP date.
- A read whose request carries `Cache-Control: no-store`, for example from `Connection.Headers`. It is sent without a validator and its answer is not kept.
- A read that followed a redirect. A validator names a representation of one resource only, so a hop leaves without one and its answer is not kept.
- A read that a request or attempt observer changed. An observer that changes the method, the URL, the `Host` or a header sends that read without a validator, and its answer is not kept, because the key describes the request before the observer ran. An observer that changes the answer, its status, its headers or its body, has its answer neither served from the held copy nor kept. An observer that adds a header to every request, such as a tracing header, therefore switches the cache off. So does a `WithWireTransport` substitute whose answers report, as their `Request`, a request other than the one it was given, or name none: a substitute's reads are cached only where each answer reports the request it answered, as `net/http`'s `Transport` does. If you observe reads on a product that sends validators, check that `BudgetState().ReadCache.Hits` rises after you adopt the cache. A `304` an observer changes is sent once more without a validator, so each such read takes two requests, and on GitLab both are billed; under such an observer, switch the cache off with `WithReadCache(0)`.

A `304` that answers a request carrying no validator has nothing to be served from, so the client sends the request once more without one, even when the read is not cached. So does a `304` naming another `ETag` or `Last-Modified` than the held copy's, because it vouches for another representation, and a `304` an observer changed. The rate headers on that first `304` still update `BudgetState`, except on a cached read whose `304` the client cannot vouch for, such as one whose request or answer an observer or a `WithWireTransport` substitute changed, or one that came through a redirect. On GitHub the resend is billed. The resend's own answer is kept only if it is a `200` with a validator, sent with the same token. If the resend fails, is answered `304` again, or carries a refreshed token, the held copy is forgotten and nothing is kept.

## The key

Each client has a cache of its own, so two clients never share an entry. An entry's key is a SHA-256 digest of a digest of the token the request carried, the method, the host and the request URL, every other request header but the validators, such as `Accept`, `User-Agent` and the API version, and the Go type the answer decodes into. Every attempt is checked against the request the key describes before it is sent, and every answer against the request it reports it answered, so an answer is never served to, or kept from, a request with another credential or header. The token itself is never kept. A rotated token starts with nothing held, so it never revalidates an answer another credential received.

## Memory

The cache holds at most `DefaultReadCacheBytes`, 32 MiB, per client, and evicts the least recently used entry first. An entry is charged everything it holds, its key, its validators, the headers it keeps with the storage each line takes, and the full capacity of its body, not only the body's length. An entry larger than a quarter of the bound is not kept, so one large answer cannot flush the rest. Entries hold the fields the library reads from each answer, not the raw body, which is far smaller: one GitHub page of 100 runs is about 3.5 MB on the wire and about 39 KB held.

- `forgeapi.WithReadCache(n)` sets the bound in bytes, and `WithReadCache(0)` switches the cache off.
- `BudgetState().ReadCache` reports `Hits`, `Misses`, `Saved` and the bytes held. `Saved` counts the `304` answers a held copy was served for on a product that bills nothing for them, each a request the instance did not bill.
- Every read, a revalidated one included, runs inside its operation's `WithOperationTimeout` bound, from the credential to the decoded answer. An operation of several requests shares that one bound. A revalidated read that outlasts it answers the deadline and keeps nothing.
- `Close` empties the cache.

## Why the cache lives in memory only

A tier on disk or in a shared store was also considered and left out. It would let a restarted process revalidate what an earlier one read, but it would hold response bodies, private repository data included, outside the process, and every entry would then have two copies that every change must keep in step. The saving, one full read of each answer after a restart, does not outweigh that. A restarted client starts with nothing held.

## What observers see

A request observer and an attempt observer see the `304` answers as the instance sent them, although the caller receives the held body. `BudgetState.LastCost` is `0` for a call answered whole by `304`s to authorized conditional requests on GitHub. `ReadCache.Hits` counts the cacheable REST reads a held copy answered, and `ReadCache.Misses` every other cacheable REST read the instance answered in full, error statuses included. A read sent once more after a `304` counts once, although it took two requests. One call can make several reads, so these count reads, while `LastCost` describes the call.

## Caching requests your own client sends

For a route no forgeapi operation covers, `readcache.NewTransport(next, opts...)` puts this same cache on an `http.RoundTripper` you build. It is one implementation with the cache above: the same validators, the same rules for when a `304` serves the held copy, the same bound and eviction. It knows nothing of any forge, so you state what this page otherwise takes from the product:

- `readcache.WithFreeNotModified(true)` where the server bills nothing for a `304` to a conditional request carrying an `Authorization` header, as GitHub's REST API does. It decides only what `Saved` counts. Default: false.
- `readcache.WithMaxBytes(n)` bounds what it holds, and `0` switches it off. Default: `DefaultReadCacheBytes`. A negative bound is refused.
- `readcache.WithHeldFields(names...)` names the response header fields you read from a served answer, such as `Link` for paging. A held field the `304` carries takes the `304`'s value, and one it omits keeps the held value, which the `304` did not confirm. A field sent on several lines, such as `Set-Cookie`, keeps each line. Every other field of a served answer is the `304`'s own. Default: none.

The transport differs from a client's cache only where it has to:

- It revalidates a `GET` carrying no validator of its own in `If-None-Match` or `If-Modified-Since`. Every other request, a conditional one you built included, goes to `next` as given.
- The key is a SHA-256 digest of the request's `Authorization` lines, its host, its URL and every other request header but the validators, `User-Agent` included, however each header name is cased. Requests with different credentials never share an entry, and the credential is never kept.
- It keeps the body as `next` delivers it, decoded where `next` decoded it, up to a quarter of the bound. It does not keep an answer larger than that, one with a `Content-Encoding`, because the `304` that later serves it need not repeat the coding, or any answer this page says is never cached, and such an answer forgets what was held for the request. Each redirect hop your client follows is a request of its own.
- A served answer reaches you as a `200` with the held body, the `304`'s headers but its `Content-Length` and `Content-Encoding`, and the held fields.
- A `304` nothing held may serve is sent once more without a validator, as above, with the cache switched off or the request carrying `no-store` too. A second `304` reaches you as the `304` it is.
- A read whose request context ends before the transport returns, even after its answer was read in full and whatever that answer was, answers the context's error and keeps nothing.
- `State()` reports `Hits`, `Misses`, `Saved` and `Bytes` as `BudgetState().ReadCache` does. `Misses` counts every other `GET` it looked up that the server answered, an error status included.

An observer you wrap around `next` sees every request that reaches the server and a `304` as the `304` it is; one around the transport sees a served answer as a `200`. Each answer from `next` must report the request it answered as its `Request`, as `net/http`'s `Transport` does. An answer to a request `next` changed, or one naming no request, is neither served from the held copy nor kept. So a round tripper that sets a credential or any other request header, such as an OAuth or signing transport, wraps the transport rather than sitting inside `next`, where the key would not see what it sets. The transport never answers without the server. It is only the cache: your requests get none of a client's pacing, retries, budget, error mapping or address policy, which stay whatever `next` does.
