# Tested against

This page lists the instances the library's per-product behaviour was measured on and the build each ran then. Each hosted instance also shows what it reported when it was checked on 2026-10-05. Read it to judge how close your own instance is to what was measured.

## Measured instances

| Product | Instance | Build the behaviour was measured on | Reported on the 2026-10-05 check | Source |
| --- | --- | --- | --- | --- |
| GitHub | github.com | REST API version `2026-03-10`, the version the library pins | `2026-03-10` and `2022-11-28` offered, `2026-03-10` selected for a request that asks for it | [`/versions`](https://api.github.com/versions) |
| GitLab | gitlab.com | `19.5.0-pre`, over REST v4 and the GraphQL endpoint | `19.5.0-pre` | [`/api/v4/metadata`](https://gitlab.com/api/v4/metadata), which refuses an anonymous caller |
| Gitea | gitea.com | `1.27.0+dev-954-g1f3981a301`, and `1.27.0+dev-955-g37488799e1` for the owner scope and the cross-repository issue list | `1.27.0+dev-1118-ge629c4fdc2`, a newer build | [`/api/v1/version`](https://gitea.com/api/v1/version) |
| Gitea | a local instance | `28.0.0`, for the run listing, which gitea.com refuses an anonymous caller, and for what the writes answer | Not checked, a pinned image | [`live.yaml`](../.github/workflows/live.yaml) |
| Forgejo | codeberg.org | `16.0.0-dev-753-6bcc6da0+gitea-1.22.0`, whose `+gitea-` marker is what separates this product from Gitea | The same build | [`/api/v1/version`](https://codeberg.org/api/v1/version) |
| Forgejo | local instances | `16.0.5+gitea-1.22.0` and `15.0.9+gitea-1.22.0`, for how the run listing pages and for what the writes answer | Not checked, pinned images | [`live.yaml`](../.github/workflows/live.yaml) |

github.com echoes the selected REST API version back as `X-Github-Api-Version-Selected` on every REST response. A GraphQL response carries no API version header.

The hosted instances update on their own schedule, so they can report a newer version than the one checked here. The local instances are the images [`live.yaml`](../.github/workflows/live.yaml) pins.

This table is the one place a version appears. The per-product facts in the godoc and in SUPPORT.md name no version, so a product's new release changes this table and no other line.

## The weekly live run

A scheduled workflow, [`live.yaml`](../.github/workflows/live.yaml), runs the test suite against github.com, gitlab.com and released Gitea and Forgejo images every week, writes included. It runs the github.com cases twice, once with a classic token and then with a fine-grained token scoped to the sandbox repository. On 2026-10-04 it passed all 26 cases against github.com and all 26 against gitlab.com. On 2026-10-05 all 26 github.com cases passed with the fine-grained token. GitHub Enterprise Server and self-managed GitLab are served by the same code, and neither the measurement nor the weekly run reaches them.
