# forgeapi

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/forgeapi.svg)](https://pkg.go.dev/github.com/cplieger/forgeapi) [![Go version](https://img.shields.io/github/go-mod/go-version/cplieger/forgeapi)](https://github.com/cplieger/forgeapi/blob/main/go.mod) [![Mutation](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/cplieger/forgeapi/badges/mutation.json)](https://github.com/cplieger/forgeapi/issues?q=label%3Agremlins-tracker)

forgeapi lets your Go code read and write pull requests, issues, merges, checks, releases and labels on GitHub, GitLab, Gitea and Forgejo through one set of types.

It replaces four API clients and the differences in routes, field names and value shapes you would otherwise handle. It has no operations for comments, reviews or file contents. It needs Go 1.27.1 or later, is at v1 and is licensed under Apache-2.0. It depends only on atomicfile, httpx, jsoncap, runesafe, ssrf and urlform, six modules by the same author.

## Why use it

forgeapi is built for Go tools that span several forges.

- Each operation is one method with the same result types on all four products. A field a product does not supply keeps its zero value, the unknown member for an enumeration.
- Each operation's godoc lists its route, request cost and differing fields per product, and [SUPPORT.md](SUPPORT.md) maps operations to products.
- It serves github.com, gitlab.com, and Gitea and Forgejo instances. GitHub Enterprise Server and self-managed GitLab run the same code but were not [measured](docs/tested-against.md).
- A client [paces itself to the rate limit](docs/request-prices.md) and [reports a repository that moved](docs/renames.md).
- `creds` runs the OAuth device flow on GitHub and GitLab and refreshes tokens. `gitcred` hands them to git.

Consider [go-github](https://github.com/google/go-github) if you target GitHub alone and want services that mirror its API documentation. Consider [go-scm](https://github.com/drone/go-scm) if you need Bitbucket, Gitee or Gogs, or comment and file endpoints. Consider [git-pkgs/forge](https://github.com/git-pkgs/forge) if you need reviews, branches, Bitbucket Cloud or Gerrit, or a command-line tool.

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

[example_test.go](example_test.go) holds the compiling form, with a credential source and error handling.

The `github`, `gitlab` and `gitea` packages each hold one client, and `gitea` serves Forgejo too. A client asks its `CredentialSource` for a token on every request, so a rotating token and a static one use the same code. Take each `RepoRef` from a call such as `ListRepos` rather than building one, because it carries its family's own spelling of the repository path. Call `Close` when you are done, to release the client's connection pool.

- `families.Open` detects which forge a URL points at and returns a `forgeapi.Core`. [Connecting to a forge](docs/connecting.md) covers what detection costs.
- An instance on a private network or on plain `http` is refused until you pass `forgeapi.WithPrivateAddresses(true)` or `forgeapi.WithPlaintextHTTP(true)`. Plain HTTP sends the token and every request in cleartext.
- `forgeapi.WithMutations(false)` makes a client read-only. In tests, `forgeapi.WithWireTransport` takes an `http.RoundTripper` that answers requests without a network.
- The `creds` package `Example` signs a user in and builds a client on the stored credential. Let only one process refresh tokens from a store, because two can lose a rotated token and force the user to sign in again. [Credentials](docs/credentials.md) covers the store and the git credential helper.

## API

- `Core` is what every client satisfies. It joins eight interfaces, `Identity`, `Repos`, `PullRequests`, `Merges`, `Checks`, `Issues`, `Capabilities` and `Governor`, plus `Close`. Declare the narrowest one your code uses.
- `Releases` and `Labels` sit outside `Core`, and all three clients implement both. Reach them from a `Core` with a type assertion such as `rel, ok := client.(forgeapi.Releases)`.
- The writes are `CreatePR`, `ClosePR`, `ReopenPR`, `MergePR`, `RerunFailedChecks`, `CreateIssue`, `CloseIssue` and `CreateRelease`. Every other operation reads.
- `Checks.CommitStatus` returns a commit's combined check verdict. `Checks.ListRuns` lists GitHub Actions runs, GitLab pipelines, and Gitea and Forgejo Actions runs.
- `Connection` describes an instance, `RepoRef` and `PRRef` address a repository and a pull request, and `Option` and `ListOption` configure a client and a list call.
- Every list answers a `Page` with a `Next` cursor, and [Pages and continuations](docs/pagination.md) covers short pages and resuming.
- `creds` holds the device flow, the refreshing source and the file store. `gitcred` holds the git credential helper.

Per-symbol detail is on [pkg.go.dev](https://pkg.go.dev/github.com/cplieger/forgeapi).

## Every call answers nil, an Error or a context error

A caller can branch on these three outcomes completely. Every operation of a client, and `families.Open`, returns a forge or library failure as the `*forgeapi.Error` pointer, so recover it with `errors.As` and branch on its code:

```go
var ferr *forgeapi.Error
if errors.As(err, &ferr) && ferr.Code == forgeapi.CodeRepoRefStale { ... }
```

`Error` carries an `ErrorKind` and a `Code`, because one HTTP status means different things per product and per operation. `Error.Message` is the forge's own text, so never branch on it. `Error` wraps no other error, and the library declares no sentinel errors of its own.

A cancelled or expired context returns Go's own `context.Canceled` or `context.DeadlineExceeded` unchanged, so `errors.Is` works on it. Each operation also runs under its own deadline, `Budget.OperationTimeout`, 20 seconds by default, which expires as `context.DeadlineExceeded` too. To tell whether your own context ended, check `ctx.Err()`. The `creds` file store and the `gitcred` helper return ordinary Go errors when a read or write fails.

## Unsupported by design

forgeapi leaves these out on purpose. [Non-goals](docs/non-goals.md) gives the reason for each.

- A generic request method, or routes you add to a client
- A vendor SDK behind any of the clients
- Receiving webhooks
- Git operations such as clone, fetch and push
- A "partly supported" mark in [SUPPORT.md](SUPPORT.md). A difference between products is stated on the field it affects
- Reading a proxy from the environment
- A polling loop in the API
- Code scanning
- The OAuth authorization-code grant. To use it, implement `CredentialSource` yourself.

## Documentation

- [Connecting to a forge](docs/connecting.md) covers connections, forge detection, options and capabilities.
- [Pages and continuations](docs/pagination.md) explains paging and resuming a list.
- [Renamed and moved repositories](docs/renames.md) says what a call answers after a move.
- [Request prices and the budget](docs/request-prices.md) covers call costs and pacing.
- [Credentials](docs/credentials.md) covers the device flow, token refresh, the store and the git credential helper.
- [Tested against](docs/tested-against.md) lists the measured instances and versions.
- [Non-goals](docs/non-goals.md) explains what is left out and why.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI-assisted tooling using [Claude](https://claude.com), [GPT](https://openai.com), and [Kiro](https://kiro.dev). The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

Apache-2.0. See [LICENSE](LICENSE).
