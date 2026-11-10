package creds

import (
	"maps"
	"slices"
	"strings"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/forgeapi/v2/internal/transport"
)

// maxGrantRepositories is the most repositories GitHub's token endpoint takes in
// one request:
// https://docs.github.com/en/rest/apps/apps#create-an-installation-access-token-for-an-app
const maxGrantRepositories = 500

// AppGrant is what an installation token may do: the named permissions,
// optionally over named repositories, or the installation's full grant through
// [FullGrant]. The zero value grants nothing and is refused.
type AppGrant struct {
	// Permissions maps a permission name to its level, "read", "write" or
	// "admin", such as "metadata": "read".
	Permissions map[string]string
	// Repositories are repository names under the installation's account, at
	// most 500; none means every repository the installation can reach.
	Repositories []string
	full         bool
}

// FullGrant asks for everything the installation holds, by name, so a
// forgotten field can never ask for it.
func FullGrant() AppGrant { return AppGrant{full: true} }

// tokenRequest is the token endpoint's request body for a narrowed grant.
type tokenRequest struct {
	Permissions  map[string]string `json:"permissions"`
	Repositories []string          `json:"repositories,omitempty"`
}

// checked answers a deep copy of g, refusing with [forgeapi.CodeAppGrantInvalid]
// a grant [NewAppSource] does not send. Names are held to GitHub's form so no
// refusal needs to quote one.
func (g *AppGrant) checked() (AppGrant, error) {
	if g.full {
		if len(g.Permissions) > 0 || len(g.Repositories) > 0 {
			return AppGrant{}, grantInvalid("the full App grant has permissions or repositories filled in")
		}
		return FullGrant(), nil
	}
	if len(g.Permissions) == 0 {
		return AppGrant{}, grantInvalid("the App grant names no permission: name the permissions the source needs, or use FullGrant")
	}
	for name, level := range g.Permissions {
		if !permissionName(name) {
			return AppGrant{}, grantInvalid("an App grant permission name is not lowercase letters, digits and underscores")
		}
		switch level {
		case "read", "write", "admin":
		default:
			return AppGrant{}, grantInvalid("an App grant permission level is not read, write or admin")
		}
	}
	if len(g.Repositories) > maxGrantRepositories {
		return AppGrant{}, grantInvalid("the App grant names more than 500 repositories")
	}
	for _, repo := range g.Repositories {
		if !segment(repo) {
			return AppGrant{}, grantInvalid("an App grant repository is not one repository name")
		}
	}
	return AppGrant{Permissions: maps.Clone(g.Permissions), Repositories: slices.Clone(g.Repositories)}, nil
}

// body is what the token request carries: nothing for the full grant, so
// GitHub grants the installation's own permissions over its own repositories.
func (g *AppGrant) body() any {
	if g.full {
		return nil
	}
	return tokenRequest{Permissions: g.Permissions, Repositories: g.Repositories}
}

// permissionName reports whether name has the form of GitHub's permission names.
func permissionName(name string) bool {
	return name != "" && strings.Trim(name, "abcdefghijklmnopqrstuvwxyz0123456789_") == ""
}

// segment reports whether name is one path segment a request may carry: the
// library's path-value form, with no separator.
func segment(name string) bool {
	return !strings.Contains(name, "/") && forgeapi.ValidateRef(name) == nil
}

func grantInvalid(message string) error {
	return transport.Local(forgeapi.CodeAppGrantInvalid, message)
}
