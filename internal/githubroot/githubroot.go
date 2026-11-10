// Package githubroot derives a GitHub instance's API roots from its web base,
// for every package that reaches GitHub's API: the github client and the creds
// package's App source.
package githubroot

import (
	"net/url"
	"strings"
)

// dotcomHost is the hosted instance, whose API and web bases are two DIFFERENT
// hosts. Every other instance is an appliance, whose API root sits under its
// own web base, so the two derivations are one branch on this name.
const dotcomHost = "github.com"

// dotcomAPIBase is the hosted instance's API root, which is the addressing the
// expectation table spells this product's routes in: no path prefix at all.
const dotcomAPIBase = "https://api.github.com"

// applianceAPIPath and applianceDocumentPath are an appliance's two roots under its
// own web base. The REST root carries a version segment and the document endpoint
// sits BESIDE it rather than under it, which is why the document URL is derived by
// replacing that segment rather than by appending to it.
const (
	applianceAPIPath      = "/api/v3"
	applianceDocumentPath = "/api/graphql"
)

// API is the REST root under the web base, used where the connection names no
// API base of its own.
//
// The hosted instance serves its API on a host of its OWN rather than under the web
// base, which is why this is a branch rather than a suffix: the routes the
// expectation table spells carry no prefix because that host's root is the prefix.
// Every other instance is an appliance, whose documented root is a version segment
// under its own web base.
func API(web *url.URL) string {
	if isDotcom(web) {
		return dotcomAPIBase
	}
	return strings.TrimSuffix(web.String(), "/") + applianceAPIPath
}

// isDotcom reports whether a URL addresses the hosted instance, by host rather than
// by anything a response carries, because the answer decides where the first request
// goes.
func isDotcom(u *url.URL) bool {
	host := strings.ToLower(u.Hostname())
	return host == dotcomHost || host == "www."+dotcomHost || host == "api."+dotcomHost
}

// Document is where a connection posts its GraphQL documents, derived from the
// REST root it resolved: beside the REST routes on an appliance, whose root
// carries a version segment the document endpoint does not, and under the API
// host on the hosted instance.
func Document(api *url.URL) string {
	base := strings.TrimSuffix(api.String(), "/")
	if trimmed, ok := strings.CutSuffix(base, applianceAPIPath); ok {
		return trimmed + applianceDocumentPath
	}
	return base + "/graphql"
}
