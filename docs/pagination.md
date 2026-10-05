# Pages and continuations

This page covers how a list call pages and how to resume a walk. Read it before you walk a long list or store a cursor between runs.

## Pages

Every list answers a `Page`. It holds the `Items`, a `Next` cursor that `WithAfter` continues the walk from, and a `Partial` that is nil when the page holds everything asked for.

A page can hold fewer rows than `WithPageBound` asked for and still carry `Next`. A family asks for fewer rows where its instance or its query serves fewer. Gitea and Forgejo ask for at most the page size the instance states. GitHub's `ListPRs`, and the search behind `ListMyPRs` and `ListMyIssues`, ask for at most a page size measured to answer on every attempt. So walk until `Next` is empty.

A partial page names its `PartialReason`. `PartialResultWindow` is the one reason that arrives without `Next`. It means the forge stopped serving rows while its own total states more, so the call cannot reach the rest. On GitHub, `ListMyPRs` and `ListMyIssues` read a search that ends a walk this way.

## Continuations

Pass a `Next` back only to the call that produced it, on a client for the same API base. Every continuation names the call and the connection that produced it, which means the family, the API base URL, the operation, the repository, the state filter and the scope. Handed to a call that differs in any of them, it is refused with `CodeCursorInvalid` before any request.

The API base is compared after its scheme and host are lowercased, a default port is dropped and a trailing slash is trimmed. So a continuation survives a restart and resumes on any client built for the same base. A client that names the same instance by another host name starts the walk again.

A list read over REST counts its pages by number, so its continuation also names the page bound. Resumed under another page bound, it is refused the same way, and the message names the bound the walk began at. On Gitea and Forgejo it also names the page size the walk asked the instance for. A connection that would ask for another size refuses it before any page is requested. To change a REST walk's page bound, start it again from the first page.

A list read over GraphQL continues from the forge's own position, which resumes at any page bound. That includes the search behind GitHub's `ListMyPRs` and `ListMyIssues`.
