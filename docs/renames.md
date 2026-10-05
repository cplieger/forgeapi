# Renamed and moved repositories

This page covers what a call answers when the repository it addresses has a new name or a new owner. Read it if you store `RepoRef` values between runs.

## What a call answers

GitHub's REST API, Gitea and Forgejo answer the old address of a renamed or transferred repository with a redirect. The library reads that redirect as the move rather than following it silently.

- A list, a commit status or a merge-state read answers from the repository it moved to, and names that repository in the answer's `Successor`.
- `ReadPR` and `RepoAffordances` have no such field in their answers. They refuse with `CodeRepoRefStale` and name the new repository in `Error.Successor`.
- A write that the old address answers with a 301 is refused the same way, and the library does not resend it.
- Any read whose redirect leads to a refusal is refused the same way too.

Where a product's answer names no successor at all, the operation's `Per forge:` rows say so.

## What to do

Point the stored `RepoRef` at the successor. If the move is detected but the successor cannot be named, `Successor` is nil. In that case, reconnect and list the repositories again instead.

## What it costs

Over GitHub's REST API, the successor costs one further read, because its redirect names the repository by id. On Gitea and Forgejo, the successor is read off the redirect at no further request.
