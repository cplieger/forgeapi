# Pages and continuations

This page covers how a list call pages and how to resume a walk. Read it before you walk a long list or store a cursor between runs.

## Pages

Every list answers a `Page`. It holds the `Items`, a `Next` cursor that `WithAfter` continues the walk from, and a `Partial` that is nil when the page holds everything asked for.

A page can hold fewer rows than `WithPageBound` asked for and still carry `Next`. A family asks for fewer rows where its instance or its query serves fewer. Gitea and Forgejo ask for at most the page size the instance states. GitHub's `ListPRs`, and the search behind `ListMyPRs` and `ListMyIssues`, ask for at most a page size measured to answer on every attempt. So walk until `Next` is empty. On GitHub, a full page of `ListRepos`, `ListIssues`, `ListReleases` or `ListLabels` answered from the [read cache](read-cache.md) always carries `Next`, so such a walk can end on a page with no row.

A partial page names its `PartialReason`. `PartialResultWindow` is the one reason that arrives without `Next`. It means the forge stopped serving rows while its own total states more, so the call cannot reach the rest. On GitHub, `ListMyPRs` and `ListMyIssues` read a search that ends a walk this way. On Gitea and Forgejo, `ListRuns` ends a walk this way when a page serves no row while the stated total says more. A page whose GraphQL document also answered errors carries `PartialGraphQLPartial` instead, because its own rows are in question.

GitHub serves `ListRuns` under `WithSince` through a result window of 1,000 runs per created filter, ending with the whole page that reaches it. A walk that reaches the window goes on under a created range from the bound to the oldest second the walk has reached. The page that reaches the window holds back that second's runs, and the next page serves them again. In three cases the walk reads that second on its own instead: an earlier page already answered some of its runs, the range already ends at that second, or the second holds more runs than a continuation can name. That read drops the runs the walk already answered, then the walk goes on below that second. Such a page can hold no row and still carry `Next`.

Only a second holding more runs than the window can put runs out of reach. Even then, the range that reached that second and the second's own read can together serve all of its runs, and the walk answers every one of them. Where they still fall short of that second's total, the walk goes on below that second. Its last page then carries `PartialResultWindow`, with `OmittedAtLeast` counting the runs of such seconds that the walk did not answer. The call fails with `CodeValidation` where no narrower range can reach the rest of a stated total, because GitHub stated runs it did not serve. That happens when a range served no row while its total says more, or when one second's own range ends short of its total before it reaches the window.

A row can carry its own `Partial` on a page that has none. On GitHub, a fine-grained token cannot read check runs, so on a private repository the forge refuses a pull request's checks and answers the rest of the row. That row carries `PartialForbidden` with its checks unknown, and the page stays whole. Any other error beside the rows still marks the page.

`WithSince` reads its bound at whole seconds, because GitHub, Gitea and Forgejo stamp a run's creation at whole seconds. On Gitea and Forgejo the run route has no time filter, so a bounded walk drops the older rows from each page. Both answer runs newest id first and stamp a run's creation when it is inserted, so the walk ends, complete, at the first page whose every run was created more than a minute before the bound. The minute absorbs a host clock stepped back between two runs.

## Continuations

Pass a `Next` back only to the call that produced it, on a client for the same API base. Every continuation names the call and the connection that produced it, which means the family, the API base URL, the operation, the repository, the state filter, the scope and the since bound. Handed to a call that differs in any of them, it is refused with `CodeCursorInvalid` before any request.

The API base is compared after its scheme and host are lowercased, a default port is dropped and a trailing slash is trimmed. So a continuation survives a restart and resumes on any client built for the same base. A client that names the same instance by another host name starts the walk again.

A list read over REST counts its pages by number, so its continuation also names the page bound. Resumed under another page bound, it is refused the same way, and the message names the bound the walk began at. On Gitea and Forgejo it also names the page size the walk asked the instance for. A connection that would ask for another size refuses it before any page is requested. To change a REST walk's page bound, start it again from the first page.

A list read over GraphQL continues from the forge's own position, which resumes at any page bound. That includes the search behind GitHub's `ListMyPRs` and `ListMyIssues`.
