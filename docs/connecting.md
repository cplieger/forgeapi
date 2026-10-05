# Connecting to a forge

This page covers how a client reaches an instance, with the connection settings, family detection, the options a client takes and the capability reads. Read it when you connect to a self-hosted instance or let users pick their own.

## The connection

`forgeapi.Connection` describes one instance, and a client is built per connection.

- `WebBaseURL` is required. It is the origin of the web pages the instance serves, which is also the origin a git remote carries. A URL with a username, a query or a fragment is refused with `CodeConnectionInvalid`.
- `APIBaseURL` is optional. When it is empty, each family derives it from `WebBaseURL`.
- `CABytes` is an optional PEM bundle for an instance with a private certificate authority. It replaces the system trust store for that connection rather than adding to it.
- `ClientCert` and `ClientKey` are an optional pair for mutual TLS. A pair with only one half is refused.
- `Proxy` is the proxy this connection's traffic leaves through, as a URL. The library never reads `HTTPS_PROXY`, `HTTP_PROXY` or `NO_PROXY`.
- `Headers` are extra request headers sent on every request to this connection, for a gateway in front of a self-hosted instance. A redirect that changes the scheme, host or port drops them. The names `forgeapi.ReservedHeaders` returns are refused rather than overridden.

The credential is the `WithCredentialSource` option. A family's constructor refuses a connection without one rather than sending anonymous requests.

## Private networks and plain HTTP

A self-hosted forge is usually on a private network. A connection refuses a private-range address or a single-label host until you pass `forgeapi.WithPrivateAddresses(true)`. With a `Proxy` set, the proxy resolves the destination, so this check applies to the proxy's own address and the proxy decides which destinations are reachable. A connection refuses `http` until you pass `forgeapi.WithPlaintextHTTP(true)`. With plain HTTP, the token and every request travel in cleartext on that network. A redirect from `https` down to `http` stays refused whatever you set.

## Detecting the family

`families.Open` is the entry point for when the family is a fact about the instance rather than about your configuration. It asks GitLab's connection read first, then GitHub's, then the one Gitea and Forgejo share. It answers the first family that recognizes the instance, beside a `forgeapi.Core` client for it that already holds the connection's capabilities.

GitLab and GitHub recognize themselves by a response header, and Gitea and Forgejo by the version the instance reports. Each family asked before the one that answers costs one request, which no client's budget counts. Detection costs at most 2 requests on GitLab, 3 on GitHub and 5 on Gitea or Forgejo. Every detection read carries the connection's credential and goes only to the instance the connection names.

An instance that no family recognizes is refused with `CodeFamilyUndetected`, which carries the last read's status. A caller that knows its family calls that family's own constructor, `github.New`, `gitlab.New` or `gitea.New`, and spends nothing on detection. Gitea and Forgejo share the `gitea` package because they share an API.

The two optional roles, `Releases` and `Labels`, are reached by type assertion on a `Core`. A family that cannot serve one fails the assertion rather than offering a method that always fails.

## Repository identifiers

A `RepoRef` comes from what the forge answered and is never built by hand. It is derived from the family's own canonical selector and carries the family with it. `RepoRef.Encode` and `DecodeRepoRef` round-trip an identifier through a store.

## Options

`Option` configures a client and `ListOption` a single list call. Every option takes a parameter, and none is signalled by its presence alone. Leaving an option out gives its documented default, which `DefaultBudget` and `DefaultRefreshLead` publish.

- `WithMutations(false)` refuses every write with `CodeMutationsDisabled`. The default is `true`, so a client can merge and close pull requests unless you say otherwise.
- `WithOwner` scopes the cross-repository lists, `ListMyPRs` and `ListMyIssues`, to every open item under one owner, in place of what the credential authored. The owner is a user or an organization, or a group on GitLab. `ValidateOwner` checks an owner against a family's rule before any request.
- `WithWireTransport` replaces the wire under a client's own HTTP stack, which is what a test uses to answer requests without a network. It bypasses the address policy, the trust anchor, the redirect policy and the proxy, because those live in the transport it replaces.

## Capabilities

`ConnectionCaps`, `GrantCaps`, `RepoAffordances` and `ActionState` are four separate scopes. A capability can be disabled for one repository, refused to one credential, or unknown because no safe probe exists, and each scope answers one of those questions.

## Closing a client

A client holds a pool of connections to its instance. Call `Close` when you are done with it, for example when the user disconnects or the connection moves to another address.
