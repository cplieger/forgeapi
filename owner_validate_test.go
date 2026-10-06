package forgeapi_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
)

// An owner names one account on GitHub and the Gitea family and a group path on
// GitLab, so a path is refused before any request wherever the family's owner is a
// single name, and the unknown family takes the single-name rule as a selector does.
func TestValidateOwner_refuses_a_path_where_the_family_owner_is_one_name(t *testing.T) {
	for _, test := range []struct {
		name   string
		family forgeapi.Family
		owner  string
	}{
		{name: "github_group_path", family: forgeapi.FamilyGitHub, owner: "acme/team"},
		{name: "gitea_group_path", family: forgeapi.FamilyGitea, owner: "acme/team"},
		{name: "unknown_group_path", family: forgeapi.FamilyUnknown, owner: "acme/team"},
		{name: "github_nested_path", family: forgeapi.FamilyGitHub, owner: "acme/team/sub"},
	} {
		t.Run(test.name, func(t *testing.T) {
			checkOwnerRefused(t, test.family, test.owner)
		})
	}
}

// A GitLab owner is a group's full path, subgroups included, and a single name is an
// owner on every family.
func TestValidateOwner_accepts_an_owner_the_family_can_scope(t *testing.T) {
	for _, test := range []struct {
		name   string
		family forgeapi.Family
		owner  string
	}{
		{name: "gitlab_group_path", family: forgeapi.FamilyGitLab, owner: "acme/team"},
		{name: "gitlab_nested_path", family: forgeapi.FamilyGitLab, owner: "acme/team/sub"},
		{name: "github_login", family: forgeapi.FamilyGitHub, owner: "Example-org_2.dev"},
		{name: "github_letter_and_digit_bounds", family: forgeapi.FamilyGitHub, owner: "azAZ09"},
		{name: "gitlab_letter_and_digit_bounds", family: forgeapi.FamilyGitLab, owner: "az/AZ/09"},
		{name: "gitlab_login", family: forgeapi.FamilyGitLab, owner: "acme"},
		{name: "gitea_login", family: forgeapi.FamilyGitea, owner: "acme"},
		{name: "github_at_the_bound", family: forgeapi.FamilyGitHub, owner: strings.Repeat("a", 255)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := forgeapi.ValidateOwner(test.family, test.owner); err != nil {
				t.Errorf("ValidateOwner(%v, %q) = %v, want nil", test.family, test.owner, err)
			}
		})
	}
}

// The shared form binds every family, GitLab included: an owner reaches a search
// string or a path on every product, so a byte outside the form is never sent.
func TestValidateOwner_refuses_an_owner_outside_the_shared_form_on_every_family(t *testing.T) {
	outside := map[string]string{
		"empty":          "",
		"space":          "an owner",
		"colon":          "user:example",
		"backtick":       "a`b",
		"open_brace":     "a{b",
		"at_sign":        "a@b",
		"open_bracket":   "a[b",
		"empty_segment":  "example//group",
		"dot_segment":    "example/./group",
		"leading_slash":  "/example",
		"trailing_slash": "example/",
		"over_the_bound": strings.Repeat("a", 256),
	}
	for _, family := range []forgeapi.Family{forgeapi.FamilyGitHub, forgeapi.FamilyGitLab, forgeapi.FamilyGitea} {
		for name, owner := range outside {
			t.Run(family.String()+"_"+name, func(t *testing.T) {
				checkOwnerRefused(t, family, owner)
			})
		}
	}
}

// checkOwnerRefused holds one owner to the local refusal [forgeapi.ValidateOwner]
// answers: the owner code and no operation, family or status.
func checkOwnerRefused(t *testing.T, family forgeapi.Family, owner string) {
	t.Helper()
	err := forgeapi.ValidateOwner(family, owner)
	var fe *forgeapi.Error
	if !errors.As(err, &fe) {
		t.Fatalf("ValidateOwner(%v, %q) = %v, want a *forgeapi.Error", family, owner, err)
	}
	if fe.Code != forgeapi.CodeListOwnerInvalid {
		t.Errorf("ValidateOwner(%v, %q) = code %q, want %q", family, owner, fe.Code, forgeapi.CodeListOwnerInvalid)
	}
	if fe.Op != "" || fe.Family != forgeapi.FamilyUnknown || fe.Status != 0 {
		t.Errorf("ValidateOwner(%v, %q) = Op %q family %v status %d, want the local refusal's empty three", family, owner, fe.Op, fe.Family, fe.Status)
	}
}
