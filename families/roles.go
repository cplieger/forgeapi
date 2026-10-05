package families

import (
	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/gitea"
	"github.com/cplieger/forgeapi/github"
	"github.com/cplieger/forgeapi/gitlab"
)

// The aggregate half of the role contract: every role each family claims,
// asserted over the type that family's exported constructor returns.
//
// Each of these is the SAME assertion the family package already makes beside its
// own client, because a family declares one client type and exports it; there is
// no unexported implementation for the two to differ about. The redundancy is kept
// for two things it does buy. Locality: the family's own copy fails that package's
// build first and names the family in the error. And the factory's contract:
// [forgeapi.Core] is what [Open] returns, so a family that stopped satisfying it
// breaks this package rather than a consumer, which is why Core is asserted here
// and not there.

var (
	_ forgeapi.Core         = (*github.Client)(nil)
	_ forgeapi.Identity     = (*github.Client)(nil)
	_ forgeapi.Repos        = (*github.Client)(nil)
	_ forgeapi.PullRequests = (*github.Client)(nil)
	_ forgeapi.Merges       = (*github.Client)(nil)
	_ forgeapi.Checks       = (*github.Client)(nil)
	_ forgeapi.Issues       = (*github.Client)(nil)
	_ forgeapi.Capabilities = (*github.Client)(nil)
	_ forgeapi.Governor     = (*github.Client)(nil)
	_ forgeapi.Releases     = (*github.Client)(nil)
	_ forgeapi.Labels       = (*github.Client)(nil)
)

var (
	_ forgeapi.Core         = (*gitlab.Client)(nil)
	_ forgeapi.Identity     = (*gitlab.Client)(nil)
	_ forgeapi.Repos        = (*gitlab.Client)(nil)
	_ forgeapi.PullRequests = (*gitlab.Client)(nil)
	_ forgeapi.Merges       = (*gitlab.Client)(nil)
	_ forgeapi.Checks       = (*gitlab.Client)(nil)
	_ forgeapi.Issues       = (*gitlab.Client)(nil)
	_ forgeapi.Capabilities = (*gitlab.Client)(nil)
	_ forgeapi.Governor     = (*gitlab.Client)(nil)
	_ forgeapi.Releases     = (*gitlab.Client)(nil)
	_ forgeapi.Labels       = (*gitlab.Client)(nil)
)

var (
	_ forgeapi.Core         = (*gitea.Client)(nil)
	_ forgeapi.Identity     = (*gitea.Client)(nil)
	_ forgeapi.Repos        = (*gitea.Client)(nil)
	_ forgeapi.PullRequests = (*gitea.Client)(nil)
	_ forgeapi.Merges       = (*gitea.Client)(nil)
	_ forgeapi.Checks       = (*gitea.Client)(nil)
	_ forgeapi.Issues       = (*gitea.Client)(nil)
	_ forgeapi.Capabilities = (*gitea.Client)(nil)
	_ forgeapi.Governor     = (*gitea.Client)(nil)
	_ forgeapi.Releases     = (*gitea.Client)(nil)
	_ forgeapi.Labels       = (*gitea.Client)(nil)
)
