# Contributing to forgeapi

The [shared rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) for commits, releases, synced files and checks apply here.

## Scope

A change that removes, renames or changes the signature of an entry in `api/forgeapi.txt` needs an issue first. Regenerating that file with `UPDATE_GOLDEN=1` turns the surface test green on the next run, and every consumer of the name still breaks.

## Rules

- `internal/spec/spec.go` is generated from measurements kept outside this repository, so a pull request cannot change an entry. Report a wrong one in an issue.
- `SUPPORT.md` and the `Per forge:` block ending each role method's doc comment in `roles.go` are generated from `spec.go`. Write above the marker, because `go generate ./...` rewrites from it to the comment's end.
- A family package comment states a per-product route, price or departure only where that family's behavior is its whole subject, because a second copy can disagree with `spec.go`.
- The JSON files under `conformance/testdata` and `families/testdata` are trimmed recordings of real responses. Never edit one to make a case pass, or the offline case asserts an answer no forge sent. Report a stale one in an issue.
- A new option in `options.go` with a `Default:` line needs a row in `TestRuledDefaultsAreStated`. The golden file omits doc comments, so without that row nothing checks the default.
- Never reorder the fields of an exported struct to satisfy `fieldalignment`, because that breaks every unkeyed composite literal. Put `//nolint:govet // fieldalignment: <reason>` on the type instead.

## Checks

The pull request gate runs the conformance suite offline only, so after a change to a family's requests or parsing, run the live read cases. A public instance needs no token:

```sh
FORGEAPI_LIVE_GITEA_URL=https://gitea.com go test -count=1 -run TestLive ./conformance/
```

Each product-specific variable is `FORGEAPI_LIVE_<PRODUCT>_<NAME>`, with `<PRODUCT>` one of `GITHUB`, `GITLAB`, `GITEA` or `FORGEJO`. `_URL` opts a product in and `_TOKEN` adds the credentialed cases.

With your own `_REPO`, also set `_REF` and `_PR`, whose defaults belong to the default project.

The write cases run only where `_SANDBOX` names the same repository as `_REPO`. Use one from an account that owns nothing else. On GitLab, keep it under a group, because the owner-scoped cases read a group route.

That repository needs a `main` branch, a `seed` branch one commit ahead, an open issue, and an open pull request from `seed` into `main` that carries the `_LABEL` label. Set `_PR` to that pull request's number.

Start no name or title there with `forgeapi-live-` or `forgeapi-ci-live-`, because the run closes or deletes everything that does.

Set `_RUN_WAIT=0` unless the sandbox's CI gives one passing and one failing job on a push to a `forgeapi-ci-*` branch, or the re-run case waits five minutes to skip.

The write cases need a GitLab token with `api`, or a GitHub token of either kind. A classic GitHub token needs `repo`, `read:org` and `workflow`. A fine-grained one needs access to the sandbox repository only. Give it read and write on Actions, Contents, Issues and Pull requests, and read on Commit statuses and Metadata. With `FORGEAPI_LIVE_GITHUB_TOKEN` exported, a GitHub write run is:

```sh
FORGEAPI_LIVE_GITHUB_URL=https://github.com \
FORGEAPI_LIVE_GITHUB_REPO=you/forgeapi-sandbox \
FORGEAPI_LIVE_GITHUB_SANDBOX=you/forgeapi-sandbox \
FORGEAPI_LIVE_GITHUB_REF=main \
FORGEAPI_LIVE_GITHUB_PR=7 \
FORGEAPI_LIVE_GITHUB_LABEL=your-label \
go test -count=1 -v -timeout 25m -run TestLive ./conformance/
```
