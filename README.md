# forgeapi

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/forgeapi.svg)](https://pkg.go.dev/github.com/cplieger/forgeapi) [![Go version](https://img.shields.io/github/go-mod/go-version/cplieger/forgeapi)](https://github.com/cplieger/forgeapi/blob/main/go.mod) [![Mutation](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/cplieger/forgeapi/badges/mutation.json)](https://github.com/cplieger/forgeapi/issues?q=label%3Agremlins-tracker)

forgeapi lets your Go code read and write pull requests, issues, merges, checks, releases and labels on GitHub, GitLab, Gitea and Forgejo through one set of types.

It replaces four API clients and the differences in routes, field names and value shapes you would otherwise handle yourself. It needs Go 1.27.1 or later. Its code imports six modules, all by the same author, and none of their types appears in its API. It is licensed under Apache-2.0.

## Why use it

forgeapi is built for Go tools that work with more than one forge.

- Each operation is one method that returns the same type on all four products. Where a product lacks a feature, the field always reads unknown, empty or zero there.
- Each remote operation's godoc lists its route, its request cost and every field that differs per product. [SUPPORT.md](SUPPORT.md) shows which operations each product supports.
- It serves github.com, gitlab.com and Gitea and Forgejo instances. GitHub Enterprise Server and self-managed GitLab run the same code but were not [measured](docs/tested-against.md). `families.Open` detects which family a URL points at.
- A client [paces itself from the forge's rate limit](docs/request-prices.md) and [reports a repository that moved](docs/renames.md).
- `creds` runs the OAuth device grant on GitHub and GitLab and refreshes tokens. `gitcred` hands them to git.

Consider [go-github](https://github.com/google/go-github) if you target GitHub alone and want a client organized around GitHub's REST API. Consider [go-scm](https://github.com/drone/go-scm) if you need Bitbucket, Gitee or Gogs, or comment and file endpoints.

## Install

```sh
go get github.com/cplieger/forgeapi@latest
```

## Usage

```go
client, err := github.New(
    forgeapi.Connection{WebBaseURL: "https://github.com"},
    forgeapi.WithCredentialSource(cred),
)
if err != nil {
    return err
}
defer client.Close()

repos, err := client.ListRepos(ctx, forgeapi.WithPageBound(1))
if err != nil {
    return err
}

prs, err := client.ListPRs(ctx, repos.Items[0].Ref, forgeapi.WithState(forgeapi.ListStateOpen))
```

`Example` in [example_test.go](example_test.go) is the compiling form of this, with the credential source and the error branch written out.

A client asks its `CredentialSource` for a token on every request, so a rotating token and a static one use the same code. Use the `RepoRef` values a call returns rather than building your own, because each one records the family it belongs to. Call `Close` when you are done with a client, because it holds a pool of connections.

- When you do not know the family, `families.Open` detects it and returns a `forgeapi.Core`. [Connecting to a forge](docs/connecting.md) covers detection and what it costs.
- An instance on a private network or on plain `http` is refused until you pass `forgeapi.WithPrivateAddresses(true)` or `forgeapi.WithPlaintextHTTP(true)`. Plain HTTP sends the token and every request in cleartext.
- `forgeapi.WithMutations(false)` makes a client read-only. In tests, `forgeapi.WithWireTransport` takes an `http.RoundTripper` that answers requests without a network.
- The `Example` in the `creds` package signs a user in through the device grant and runs a client on the stored credential. Run one refreshing process per credential store, because two can lose a rotated token and force the user to reconnect. [Credentials](docs/credentials.md) covers the store and the git credential helper.

## API

- `Core` is what every client satisfies: `Whoami`, `ListRepos`, `ListPRs`, `ListMyPRs`, `ReadPR`, `CreatePR`, `ClosePR`, `ReopenPR`, `RerunFailedChecks`, `MergePR`, `MergeStatus`, `CommitStatus`, `ListRuns`, `ListIssues`, `ListMyIssues`, `CreateIssue`, `CloseIssue`, the capability reads, `BudgetState` and `Close`. Declare the narrowest role your code uses, and use `Core` only where one value must support every core operation.
- `Releases`, with `ListReleases` and `CreateRelease`, and `Labels`, with `ListLabels`, are optional roles. Check for one with a type assertion, `rel, ok := client.(forgeapi.Releases)`. A family that cannot serve it fails the assertion.
- `Connection` describes an instance, `RepoRef` and `PRRef` address a repository and a pull request, and `Option` and `ListOption` configure a client and a list call.
- Every list answers a `Page` with a `Next` cursor. [Pages and continuations](docs/pagination.md) covers short pages and resuming a walk.
- `creds` holds the device grant, the refreshing source and the file store. `gitcred` holds the git credential helper.
- No operation reads or writes comments, reviews or file contents.

Per-symbol detail is on [pkg.go.dev](https://pkg.go.dev/github.com/cplieger/forgeapi).

## Every call answers nil, an Error or a context error

A caller can branch on these three outcomes completely. Recover the pointer form, which is the form every operation returns, and branch on the code:

```go
var ferr *forgeapi.Error
if errors.As(err, &ferr) && ferr.Code == forgeapi.CodeRepoRefStale { ... }
```

`Error` carries an `ErrorKind` and a `Code`, because one status means different things per product and per operation. Never branch on `Error.Message`, which is the forge's own text. `Error` wraps nothing and the library declares no sentinel of its own, so the chain ends there.

A cancelled or expired context returns Go's own sentinel unchanged, so `errors.Is(err, context.Canceled)` works. Each operation also runs under a deadline of its own, `Budget.OperationTimeout`, and its expiry is a context error too. To know whether your own context ended, check `ctx.Err()` rather than the returned error.

## Unsupported by design

forgeapi leaves these out on purpose. [Non-goals](docs/non-goals.md) gives the reason for each.

- A generic request method, or routes you add to a client
- A vendor SDK behind any of the clients
- Receiving webhooks
- Git operations such as clone, fetch and push
- A "partly supported" mark in [SUPPORT.md](SUPPORT.md). Each difference is stated on the field it affects.
- Reading a proxy from the environment
- A polling loop in the API
- Code scanning
- The OAuth authorization-code grant. To use it, implement `CredentialSource` yourself.

## Documentation

- [Connecting to a forge](docs/connecting.md) covers connections, family detection, options and capabilities.
- [Pages and continuations](docs/pagination.md) explains paging and resuming a list.
- [Renamed and moved repositories](docs/renames.md) says what a call answers after a move.
- [Request prices and the budget](docs/request-prices.md) covers call costs and pacing.
- [Credentials](docs/credentials.md) covers the device grant, token refresh, the store and the git credential helper.
- [Tested against](docs/tested-against.md) lists the measured instances and versions.
- [Non-goals](docs/non-goals.md) explains what is left out and why.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI-assisted tooling using [Claude](https://claude.com), [GPT](https://openai.com), and [Kiro](https://kiro.dev). The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

Apache-2.0. See [LICENSE](LICENSE).
