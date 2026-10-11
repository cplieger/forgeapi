# Lists across repositories

This page covers `ListMyPRs` and `ListMyIssues`, the two lists that read across every repository a credential can reach. Read it before you poll a user's or an owner's open work.

## Why they exist

Listing open pull requests one repository at a time costs one request per repository on every poll, so the cost grows with how many repositories the user has. On a workspace of 62 repositories, one such poller used about 3,700 of a per-user quota of 5,000 requests an hour, which starved every other application on the same account. `ListMyPRs` answers the same question with one request per page. A poll is then `Identity.Whoami` plus this call, two requests as its base on every product, and no product adds a request per repository.

No product adds a request per row either. Gitea and Forgejo rows carry neither a check state nor a head commit, and a folded status is read by head commit, so their rows answer `CheckUnknown` with a zero total. `ListPRs` stays for the view of one repository. `ListPRs`, `ReadPR` and `Checks.CommitStatus` are the calls that answer a check verdict.

## Scopes

By default each call lists what the credential authored. `WithOwner` lists every open item in the repositories under that owner instead, whoever authored it. It does so at the same price, with the same rows and the same continuation. The two scopes are exclusive. An owner the instance does not resolve answers `CodeOwnerUnresolved` where the product can say so, and each operation's `Per forge:` block states which products can.

The state is open by construction, so `WithState` is refused with `CodeListStateInvalid`. Each product's query already carries the open state, so a filter would have nothing to select. Widening this later is an addition, while narrowing a published filter would break callers. That is why the refusal comes first.

## Rows

Neither call names a repository. So `ValidateSelector` has nothing to check, and neither `CodeRepoRefInvalid` nor `CodeRepoRefStale` can come from the call itself. Each row carries its own repository, `PullRequest.Repo` or `Issue.Repo`, which is what you act on. `Page.Successor` is never set, as on `ListRepos`, because neither call addresses a repository that could have moved. A rename shows up as a row whose repository differs from the one you held.

Rows from archived repositories are listed, except where the operation's `Per forge:` block says otherwise. A close or merge of such a row is refused with `CodeRepoArchived`. A pull request row carries no affordance object. To read a row's merge strategies, call `Capabilities.RepoAffordances` for its repository, which costs one read per distinct repository with open pull requests.

## Pages

Both calls are paged and continued as the other lists are, one request per page up to `Budget.ListPages`. The page count grows with how many open items the credential has, not with how many repositories it watches. On GitLab, an owner named by digits alone costs one more request per page, as [Request prices and the budget](request-prices.md) explains.

Where a product answers from a search with a result window, a walk ends at the window's edge with no continuation. The page that ends it carries `PartialResultWindow` where the search's own total states more than the walk was served. The `Next` row of the operation's `Per forge:` block states the window, and [Pages and continuations](pagination.md) covers partial pages.
