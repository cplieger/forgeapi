# Credentials

This page covers the `creds` and `gitcred` packages, which sign a user in, keep the token fresh, store it and hand it to git. Read it if your tool lets users connect their own forge accounts.

## Signing in

`creds.StartDeviceGrant` and `DeviceGrant.Poll` run the OAuth device grant on GitHub and GitLab with your own OAuth application's client id. `DeviceGrant.Close` ends a pending grant early. On Gitea and Forgejo the library runs no device grant and answers `CodeGrantUnsupported`, so use a personal access token there.

For a static personal access token, a type of your own that implements `forgeapi.CredentialSource` is enough. `Example` in [example_test.go](../example_test.go) declares one. `creds.NewSource` is the rotating source this library ships.

The `Example` in the `creds` package shows the whole flow. It renders the user code, polls at the interval the grant states, saves the record and builds a client on a source over that record.

## Keeping the token fresh

`creds.Source` is a `CredentialSource` over one stored record. It refreshes a rotating token once the token's expiry is inside the refresh lead, which `WithRefreshLead` sets. One caller refreshes at a time while the others wait. A record it cannot refresh answers `CodeReconnectRequired`.

A refresh that learns the stored pair is dead writes a mark to the record's `Usability`:

- `UsabilitySpent` when the forge rotated the pair and its answer carrying the new one could not be read.
- `UsabilityReconnectRequired` when the forge refused the refresh.

Every reader of the store honours the mark, so a marked record is never handed over or refreshed. `Token` fails with `CodeReconnectRequired` and `State` answers `CredReconnectRequired`. A reconnect saves the record unmarked.

A rotation whose write to the store fails keeps the new pair in memory. That call fails and `State` answers `CredRefreshDue`. The next `Token` call in the same process stores the pair before it does anything else.

## The store

`creds.OpenFileStore` keeps every record in one file at mode 0600, in a directory at mode 0700 that the process owns.

Run exactly one refreshing process per store directory, the one that calls `Token`. The git credential helper only reads the store. Nothing detects a second refreshing process, and two of them can lose a rotated pair and leave the connection needing a reconnect.

## The git credential helper

`gitcred.Helper.Serve` answers one git credential-helper invocation from a `creds.Store`.

- A `get` hands over the token of the connection whose web base URL has the remote's origin.
- It declines an expired token or a marked record with one line naming the instance. For a marked record, the line says to reconnect.
- A `store` is ignored, and an `erase` keeps the connection.

The helper never refreshes a token. Registering it with git, and the command line git runs it with, are yours to set up.
