package creds

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/forgeapi/v2/internal/transport"
)

// selector is which of the five ways an [Installation] names its installation.
type selector int

const (
	// selectNone is the zero Installation, which names nothing and is refused.
	selectNone selector = iota
	selectByID
	selectUser
	selectOrg
	selectRepo
	selectOnly
)

// Installation names the one GitHub App installation an [AppSource] mints
// for, in the way its consumer knows it. Build one with [InstallationByID],
// [InstallationOfUser], [InstallationOfOrg], [InstallationOfRepo] or
// [OnlyInstallation]; the zero value names nothing and is refused.
type Installation struct {
	name string
	id   int64
	by   selector
}

// InstallationByID names the installation by its ID, so no lookup is sent.
func InstallationByID(id int64) Installation { return Installation{id: id, by: selectByID} }

// InstallationOfUser names the App's installation on the user account login,
// looked up at GET /users/{login}/installation.
func InstallationOfUser(login string) Installation {
	return Installation{name: login, by: selectUser}
}

// InstallationOfOrg names the App's installation on the organization login,
// looked up at GET /orgs/{login}/installation.
func InstallationOfOrg(login string) Installation {
	return Installation{name: login, by: selectOrg}
}

// InstallationOfRepo names the App's installation that reaches the repository
// fullName, spelled owner/repo as GitHub spells it, looked up at
// GET /repos/{owner}/{repo}/installation.
func InstallationOfRepo(fullName string) Installation {
	return Installation{name: fullName, by: selectRepo}
}

// OnlyInstallation names the App's one installation, looked up at
// GET /app/installations, which must list exactly one.
func OnlyInstallation() Installation { return Installation{by: selectOnly} }

// check refuses with [forgeapi.CodeAppGrantInvalid] a selector that names no
// installation, without quoting it.
func (in *Installation) check() error {
	switch in.by {
	case selectByID:
		if in.id <= 0 {
			return grantInvalid("the installation ID is not positive")
		}
	case selectUser, selectOrg:
		if !segment(in.name) {
			return grantInvalid("the installation's account login is not one account name")
		}
	case selectRepo:
		if forgeapi.ValidateSelector(forgeapi.FamilyGitHub, in.name) != nil {
			return grantInvalid("the installation's repository is not a GitHub owner and name")
		}
	case selectOnly:
	default:
		return grantInvalid("the installation selector is the zero Installation")
	}
	return nil
}

// lookup is the request that resolves the selector's installation ID. An
// [InstallationByID] selector holds its ID and is never looked up.
func (in *Installation) lookup(op string) *transport.Request {
	req := &transport.Request{Op: op, Method: http.MethodGet, Answers: appAnswers, Credential: true}
	switch in.by {
	case selectUser:
		req.Path = "/users/" + in.name + "/installation"
	case selectOrg:
		req.Path = "/orgs/" + in.name + "/installation"
	case selectRepo:
		req.Path = "/repos/" + in.name + "/installation"
	case selectOnly:
		req.Path, req.Query = "/app/installations", url.Values{"per_page": {strconv.Itoa(maxListedInstallations)}}
	}
	return req
}

// maxListedInstallations is the one page [OnlyInstallation] reads, GitHub's
// largest; a full page proves only that many installations, not the total.
const maxListedInstallations = 100

// listed is one installation as GET /app/installations lists it.
type listed struct {
	Account struct {
		Login string `json:"login"`
	} `json:"account"`
	ID int64 `json:"id"`
}

// accounts names each listed installation by account and ID, for the refusal
// of an App installed more than once.
func accounts(list []listed) string {
	names := make([]string, 0, len(list))
	for _, inst := range list {
		names = append(names, inst.Account.Login+" ("+strconv.FormatInt(inst.ID, 10)+")")
	}
	return strings.Join(names, ", ")
}
