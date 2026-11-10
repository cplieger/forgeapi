# Credentials

This page covers the `creds` and `gitcred` packages. Read it if your tool lets users connect their own forge accounts, or if it acts as a GitHub App.

`creds` holds two kinds of credential source:

- `creds.StoredSource` serves a credential kept in a store: a personal access token, or an OAuth grant whose token it refreshes. The device grant signs a user in, and `gitcred` hands the stored token to git.
- `creds.AppSource` serves a GitHub App installation token, minted from the App's private key. Nothing is stored: the key is yours, and the token lives in memory.

## Signing in

`creds.StartDeviceGrant` and `DeviceGrant.Poll` run the OAuth device grant on GitHub and GitLab with your own OAuth application's client id. `DeviceGrant.Close` ends a pending grant early. On Gitea and Forgejo the library runs no device grant and answers `CodeGrantUnsupported`, so use a personal access token there.

For a static personal access token, a type of your own that implements `forgeapi.CredentialSource` is enough. `Example` in [example_test.go](../example_test.go) declares one. `creds.NewStoredSource` is the refreshing source this library ships for a stored record.

The `Example` in the `creds` package shows the whole flow. It renders the user code, polls at the interval the grant states, saves the record and builds a client on a source over that record.

## Keeping the token fresh

`creds.StoredSource` is a `CredentialSource` over one stored record. It refreshes a rotating token once the token's expiry is inside the refresh lead, which `WithRefreshLead` sets. One caller refreshes at a time while the others wait. A record it cannot refresh answers `CodeReconnectRequired`.

A refresh that learns the stored pair is dead writes a mark to the record's `Usability`:

- `UsabilitySpent` when the forge rotated the pair and its answer carrying the new one could not be read.
- `UsabilityReconnectRequired` when the forge refused the refresh.

Every reader of the store honours the mark, so a marked record is never handed over or refreshed. `Token` fails with `CodeReconnectRequired` and `State` answers `CredReconnectRequired`. A reconnect saves the record unmarked.

A rotation whose write to the store fails keeps the new pair in memory. That call fails and `State` answers `CredRefreshDue`. The next `Token` call in the same process stores the pair before it does anything else.

`StoredToken` reads the stored token and its `State` in one read of the store, and never refreshes, writes or waits on a refresh in flight. Use it when another part of your program owns refreshing, so a request only reads what is stored. It hands over a token that is valid, refresh-due or refreshing and not yet expired. It fails with `CodeReconnectRequired` where `State` answers `CredReconnectRequired`. A token whose refresh is due or in flight fails with a retryable transient error once it has expired, until a `Token` call renews it. Where a rotated pair waits in memory to replace the stored token, `StoredToken` hands over the pair's token, which the forge issued and the next `Token` call stores. It fails the same retryable way once the pair's token has expired or its refresh token has lapsed.

## The store

`creds.OpenFileStore` keeps every record in one file at mode 0600, in a directory at mode 0700 that the process owns.

Run exactly one refreshing process per store directory, the one that calls `Token`. The git credential helper only reads the store. Nothing detects a second refreshing process, and two of them can lose a rotated pair and leave the connection needing a reconnect.

## The git credential helper

`gitcred.Helper.Serve` answers one git credential-helper invocation from a `creds.Store`.

- A `get` hands over the token of the connection whose web base URL has the remote's origin. Where several records share an origin, build the helper with `gitcred.NewFor` and the keys git may be handed, read again on every invocation. `gitcred.New` answers from every key in the store, the first by key.
- It declines an expired token or a marked record with one line naming the instance. For a marked record, the line says to reconnect.
- A `store` is ignored, and an `erase` keeps the connection.

The helper never refreshes a token. Registering it with git, and the command line git runs it with, are yours to set up. It serves stored records only, not App installation tokens.

## GitHub App credentials

### The App's key

`creds.AppKey` holds a GitHub App's identity: its issuer and the signer of its private key.

- `creds.ParseAppKeyPEM` reads the private key file GitHub issued, one PEM block holding a PKCS #1 or PKCS #8 RSA key.
- `creds.NewAppKey` takes any `crypto.Signer`, so a key held in a KMS or an HSM never enters your process.
- The issuer is the App's client ID or its numeric App ID. GitHub accepts both and recommends the client ID.
- Both constructors refuse, with `CodeAppKeyInvalid`, a key that is not RSA or is under 2048 bits, malformed PEM, and an issuer that is empty or holds whitespace. No refusal quotes the key or the issuer.

`AppKey.JWT` answers a fresh App JWT, valid from 60 seconds in the past to 9 minutes ahead, so a minute of clock skew either way stays inside GitHub's 10-minute limit. Its signature matches the `JWTProviderFunc` of [actions/scaleset](https://github.com/actions/scaleset), so you can hand that client `scaleset.JWTProviderFunc(key.JWT)` and never the key. The JWT can list every installation of the App and mint a token for any of them, so treat it as broader than any installation token.

### The installation source

`creds.NewAppSource` builds the `CredentialSource` of one installation from a `forgeapi.Connection`, an `AppKey` and an `AppSourceConfig`. The connection decides the API base, github.com or a GitHub Enterprise Server, and its trust material, proxy and address policy, as it does for a client. The source sends nothing until it is first used.

The config names the installation with one of five selectors:

| Selector | How the installation is found |
| --- | --- |
| `InstallationByID(id)` | No lookup |
| `InstallationOfUser(login)` | `GET /users/{login}/installation` |
| `InstallationOfOrg(login)` | `GET /orgs/{login}/installation` |
| `InstallationOfRepo("owner/repo")` | `GET /repos/{owner}/{repo}/installation` |
| `OnlyInstallation()` | `GET /app/installations`, which must list exactly one. For an App installed more than once, the error names each installation's account and ID. The source reads one page of 100, so past that the error says at least 100 |

The config's `Grant` says what each token may do. Name the permissions your tool needs, such as `"metadata": "read"`, and optionally up to 500 repositories under the installation's account. `creds.FullGrant()` asks for everything the installation holds. A zero grant is refused with `CodeAppGrantInvalid`, as is a `FullGrant()` with permissions or repositories added, a level other than `read`, `write` or `admin`, and more than 500 repositories. The source keeps its own copy of the grant.

A plain `http` base is refused with `CodePlaintextRefused`, even under `WithPlaintextHTTP(true)`, because the App JWT the source sends is App-wide.

### Tokens

- `Token` mints a token on first use and hands it out until the refresh lead before its expiry, then mints again while other callers wait. The lead is the smaller of `WithRefreshLead` and half the token's lifetime, so a token is handed out for at least half its life.
- `Mint` always mints a new token and answers it as an `InstallationToken` with its `Expiry`, for a process that reads its token once. It leaves the source's own token in place.
- `Resolve` answers the installation's ID, looking it up once, so you can build a client that needs the ID before any token exists.
- A mint changes no forge object, so the source mints under `WithMutations(false)`, and mints for a client's write whatever its own mutation setting.
- `InstallationToken.Token` is a `creds.Secret`. It prints as `[redacted]` under every `fmt` verb, slog and JSON, and only `Reveal` answers the token.

Treat a token as opaque. GitHub's newer installation tokens are long, and their length and format can change.

An installation token acts for the App, not for a person, so GitHub refuses it on the calls about the signed-in user. With an `AppSource`, `Whoami`, `GrantCaps` and `ListRepos` without `WithOwner` fail, because each reads `GET /user` or `GET /user/repos`. Pass `WithOwner` with the installation's account to list its repositories. `ListMyPRs` and `ListMyIssues` without `WithOwner` search for the credential's own items, and an installation token acts for the App, so pass `WithOwner` there too. GitHub lists the REST calls an installation token can make in [Endpoints available for GitHub App installation access tokens](https://docs.github.com/en/rest/authentication/endpoints-available-for-github-app-installation-access-tokens).

### When GitHub refuses a mint

- A 401 means GitHub rejected the key, or the host's clock is beyond GitHub's skew limit. A 404 means the installation is gone, a 403 that is no rate limit means it is suspended or lacks a permission, and a 422 means GitHub refused the grant. An `OnlyInstallation` lookup that lists no installation, or more than one, counts the same. Only a human can fix these.
- After one of those answers, `Token` answers a token that has not expired, and otherwise `Token` and `Mint` fail at once with `CodeReconnectRequired`. The fix is made on GitHub or on the host, so the source sends one mint again after 5 minutes, then waits twice as long after each further refusal, up to an hour. A re-probe that fails any other way keeps that wait.
- A 429, or a 403 carrying GitHub's rate-limit signs, is a rate limit. No request is sent until the wait GitHub asked for has passed, or one minute when it named none, doubling to 15 minutes. Meanwhile `Token` answers a token that has not expired, and otherwise `Token` and `Mint` fail with `KindRateLimited` and `RetryAfter` set to the time left.
- Any other refusal, such as a 400, would come back if the same request were sent again. No request is sent for a minute, doubling to 15 minutes after each further one. Meanwhile `Token` answers a token that has not expired, and otherwise `Token` and `Mint` fail with `KindUpstream` and `RetryAfter` set to the time left.
- Anything else, a 5xx, a lost connection, a timeout or an answer with no usable token, is retried up to the budget's retries (`WithRetries`), 1 second apart and then twice as long each time. The installation lookup a mint or `Resolve` sends is retried the same way. A retry the operation's deadline cannot wait for is not sent. A retry the deadline cuts short answers the attempt before it, so the error is GitHub's last answer, unless your own context ended or the first attempt ran out the operation's deadline.

A call you cancel yourself changes nothing, and the state, the waits and the next re-probe stay as they were.

`State` answers `CredReconnectRequired` after one of the answers in the first item, until a mint succeeds. Otherwise it answers `CredValid` while the source's own token is outside the refresh lead and `CredRefreshing` while a `Token` call mints. In every other case it answers `CredRefreshDue`, a source that has minted nothing yet included.

A `Token` call whose token has not expired answers it whatever a renewal's outcome. After a 404, and before each mint sent again after a refusal, the source forgets the installation ID, so a reinstall's new ID is found. `InstallationByID` keeps its ID.

### A token GitHub answers 401

GitHub can revoke a token before it expires, and a newly minted token can be refused for its first few seconds while it reaches all of GitHub. `AppSource.Discard(token)` covers both:

- Discard acts only on the token `Token` hands out. Any other token, one from `Mint` included, changes nothing, and Discard answers false.
- For a token minted under 5 seconds earlier, counted from when GitHub's answer arrived, Discard keeps it and answers true, so the request can be sent again with it.
- Otherwise Discard drops the token, and the next `Token` call mints a new one.
- A token dropped under a minute after its mint means a new token is not curing the 401. `Token` and `Mint` then fail with `KindUnauthorized` and a `RetryAfter` for 5 minutes, doubling to an hour after each further early drop, and `State` answers `CredRefreshDue`. A token handed out past its first minute without a Discard resets the wait.

The `github`, `gitlab` and `gitea` clients call Discard themselves. When an instance answers a request 401, the client looks for a `Discard(token string) (retry bool)` method on its `CredentialSource` and calls it with the token that request carried. While Discard answers true, the client sends the request again after 1 second, then 2 seconds, within the operation's deadline. A resend the deadline leaves no time for is not sent, and the call answers the 401. Any source of your own can have the method. `StoredSource` has none.

If you send requests with an `AppSource` token outside forgeapi's clients, call `Discard` with that token on a 401 yourself.
