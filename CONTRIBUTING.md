# Contributing to forgeapi

The [shared rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) for commits, releases, synced files and checks apply here.

## Scope

A change that removes, renames or changes the signature of an entry in `api/forgeapi.txt` needs an issue first. Regenerating the file with `UPDATE_GOLDEN=1` turns the surface test green on the next run, and every consumer of that name still breaks.

## Rules

- The `Per forge:` block that ends each role method's doc comment in `roles.go` is rendered from `internal/spec/spec.go`, like `SUPPORT.md`. Write above the marker. `go generate ./...` rewrites everything from the marker to the end of the comment.
- `internal/spec/spec.go` holds each product's routes, request prices and departures from the normalized contract, generated from measurements kept outside this repository. A pull request cannot change one, so report a wrong one in an issue.
- Those facts live only in `spec.go` and the `Per forge:` blocks and `SUPPORT.md` it renders. A family package's comment restates one only where that family's behavior is the whole subject, because a second copy can disagree with the table.
- The JSON files under `conformance/testdata` and `families/testdata` are trimmed cuts of responses recorded from real instances, as each file's `//` key says. Never edit one to make a case pass, or the offline case asserts an answer no forge sent. Report a stale or wrong one in an issue.
- Never reorder the fields of an exported struct to satisfy `fieldalignment`, because that breaks every unkeyed composite literal of it. Put `//nolint:govet // fieldalignment: <reason>` on the type instead, as `items.go` does.

## Checks

The pull request gate runs the conformance suite offline only. `.github/workflows/live.yaml` runs it against real instances weekly, never on a pull request.

After a change to a family's requests or parsing, run the live read cases yourself. A public instance needs no token:

```sh
FORGEAPI_LIVE_GITEA_URL=https://gitea.com go test -count=1 -run TestLive ./conformance/
```

Each product-specific variable is `FORGEAPI_LIVE_<PRODUCT>_<NAME>`, with `<PRODUCT>` one of `GITHUB`, `GITLAB`, `GITEA` or `FORGEJO`. `_URL` opts a product in and `_TOKEN` adds the credentialed cases.

Set `_REF` and `_PR` with any `_REPO`, because their defaults belong to the default project.

The write cases run only where `_SANDBOX` names the same repository as `_REPO`. Use a repository owned by an account that owns nothing else. On GitLab, keep it under a group, because the owner-scoped cases read a group route.

That repository needs a `main` branch, a `seed` branch one commit ahead, an open pull request from `seed` into `main` carrying the `_LABEL` label, and an open issue. Start neither title with `forgeapi-live-` or `forgeapi-ci-live-`, because the run sweeps those away.

Set `_PR` to that pull request. On an instance with no CI runner, set `_RUN_WAIT=0`.

The write cases need a GitHub classic token with `repo`, `read:org` and `workflow`, or a GitLab token with `api`.

With `FORGEAPI_LIVE_GITHUB_TOKEN` exported, a GitHub write run is:

```sh
FORGEAPI_LIVE_GITHUB_URL=https://github.com \
FORGEAPI_LIVE_GITHUB_REPO=you/forgeapi-sandbox \
FORGEAPI_LIVE_GITHUB_SANDBOX=you/forgeapi-sandbox \
FORGEAPI_LIVE_GITHUB_REF=main \
FORGEAPI_LIVE_GITHUB_PR=7 \
FORGEAPI_LIVE_GITHUB_LABEL=your-label \
go test -count=1 -v -timeout 25m -run TestLive ./conformance/
```
