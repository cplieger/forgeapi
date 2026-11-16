package gitea

import "github.com/cplieger/forgeapi/v3"

// The roles this family claims, asserted at compile time beside the type that
// claims them, so a method added to a published role stops compiling here. Both
// products have a release object, so the family claims the optional [forgeapi.Releases]
// as well; where the two differ on one operation, that is a capability, since
// withdrawing the role would withdraw it from the product that serves it.
var (
	_ forgeapi.Identity     = (*Client)(nil)
	_ forgeapi.Repos        = (*Client)(nil)
	_ forgeapi.PullRequests = (*Client)(nil)
	_ forgeapi.Merges       = (*Client)(nil)
	_ forgeapi.Checks       = (*Client)(nil)
	_ forgeapi.Issues       = (*Client)(nil)
	_ forgeapi.Capabilities = (*Client)(nil)
	_ forgeapi.Governor     = (*Client)(nil)
	_ forgeapi.Releases     = (*Client)(nil)
	_ forgeapi.Labels       = (*Client)(nil)
)
