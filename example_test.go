package forgeapi_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/forgeapi/v3/github"
)

// staticPAT is the smallest credential source there is: one personal access token,
// held for the life of the process. The client asks a source for its token on every
// request it sends, so a rotating source, such as the one the creds package
// builds, answers a fresh token from the same method. Kind and State report what
// the source knows about itself, for the consumer to read.
type staticPAT string

func (p staticPAT) Token(context.Context) (string, error) { return string(p), nil }
func (staticPAT) Kind() forgeapi.CredKind                 { return forgeapi.CredKindStaticPAT }
func (staticPAT) State() forgeapi.CredState               { return forgeapi.CredValid }

// Example lists the open pull requests of the first repository a credential reaches.
// It is the whole shape of a call against this library: construct one family's
// client and close it when done, take a repository identifier from what the forge
// answered rather than minting one, then call the operation and branch on the
// error's code.
//
// The example carries no Output comment, so the toolchain compiles it and does not
// run it. What it is here to prove is that the published surface holds together at
// the call site, which no prose can state and a build can.
func Example() {
	ctx := context.Background()

	client, err := github.New(
		forgeapi.Connection{WebBaseURL: "https://github.com"},
		forgeapi.WithCredentialSource(staticPAT("a-personal-access-token")),
	)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer client.Close()

	repos, err := client.ListRepos(ctx, forgeapi.WithPageBound(1))
	if err != nil {
		fmt.Println(err)
		return
	}
	if len(repos.Items) == 0 {
		fmt.Println("this credential reaches no repository")
		return
	}

	prs, err := client.ListPRs(ctx, repos.Items[0].Ref, forgeapi.WithState(forgeapi.ListStateOpen))
	var ferr *forgeapi.Error
	switch {
	case errors.As(err, &ferr):
		// Branch on the code, never on the message, which is upstream's own text.
		fmt.Printf("%s answered %s\n", ferr.Op, ferr.Code)
		return
	case err != nil:
		fmt.Println(err)
		return
	}

	for _, pr := range prs.Items {
		fmt.Printf("#%d %s\n", pr.Ref.Number, pr.Title)
	}
}

// ExampleParseRemote keys two spellings of one repository alike; only the https
// remote has a web origin.
func ExampleParseRemote() {
	for _, raw := range []string{"git@GitLab.com:group/sub/app.git", "https://gitlab.com:8443/group/sub/app"} {
		r, ok := forgeapi.ParseRemote(raw)
		fmt.Printf("%v %s owner=%s name=%s web=%q\n", ok, r.Key(), r.Owner(), r.Name(), r.WebOrigin)
	}
	// Output:
	// true gitlab.com/group/sub/app owner=group/sub name=app web=""
	// true gitlab.com/group/sub/app owner=group/sub name=app web="https://gitlab.com:8443"
}
