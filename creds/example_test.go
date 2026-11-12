package creds_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/forgeapi/v2/creds"
	"github.com/cplieger/forgeapi/v2/github"
)

// Example signs a user in to github.com through the OAuth device grant, saves
// the record under the consumer's own key, and runs a client behind the source
// that keeps that credential fresh, so the client never holds a token.
//
// It has no Output comment, so it is compiled and not run: running it would
// open a real grant against github.com.
func Example() {
	ctx := context.Background()
	conn := forgeapi.Connection{WebBaseURL: "https://github.com"}

	// The store is one file in a directory this process owns, at an absolute path
	// the consumer chooses.
	store, err := creds.OpenFileStore("/var/lib/example/credentials")
	if err != nil {
		fmt.Println(err)
		return
	}

	// The client id is the consumer's own OAuth application's.
	grant, err := creds.StartDeviceGrant(ctx, conn, forgeapi.FamilyGitHub, creds.GrantRequest{
		ClientID: "your-oauth-app-client-id",
		Scopes:   []string{"repo"},
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Printf("Open %s and enter %s\n", grant.VerificationURI, grant.UserCode)

	var rec creds.Record
	timer := time.NewTimer(grant.Interval)
	defer timer.Stop()
	for approved := false; !approved; {
		select {
		case <-ctx.Done():
			fmt.Println(ctx.Err())
			return
		case <-timer.C:
		}
		rec, approved, err = grant.Poll(ctx)
		var ferr *forgeapi.Error
		switch {
		case errors.As(err, &ferr) && (ferr.Code == forgeapi.CodeGrantDenied || ferr.Code == forgeapi.CodeGrantExpired):
			// Both end the grant: start a new one to try again.
			fmt.Println(ferr.Code)
			return
		case err != nil:
			fmt.Println(err)
			return
		}
		// A slow_down answer widens the interval, so the wait is read again.
		timer.Reset(grant.Interval)
	}

	const key = "github.com"
	if err := store.Save(key, rec); err != nil {
		fmt.Println(err)
		return
	}

	// The source refreshes the stored credential before it expires, so the client
	// never holds a token of its own.
	src, err := creds.NewStoredSource(store, key, conn)
	if err != nil {
		fmt.Println(err)
		return
	}
	client, err := github.New(conn, forgeapi.WithCredentialSource(src))
	if err != nil {
		fmt.Println(err)
		return
	}

	me, err := client.Whoami(ctx)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("signed in as", me.Login)
}

// ExampleAppSource runs a client as a GitHub App installation and mints a
// token for a child process that reads it once. It is compiled and not run:
// running it would mint against github.com.
func ExampleAppSource() {
	ctx := context.Background()
	conn := forgeapi.Connection{WebBaseURL: "https://github.com"}

	pemBytes, err := os.ReadFile("/etc/example/app-private-key.pem")
	if err != nil {
		fmt.Println(err)
		return
	}
	key, err := creds.ParseAppKeyPEM("your-app-client-id", pemBytes)
	if err != nil {
		fmt.Println(err)
		return
	}

	// The grant says what each token may do, here read pull requests on
	// your-repo.
	src, err := creds.NewAppSource(conn, key, creds.AppSourceConfig{
		Installation: creds.InstallationOfRepo("your-org/your-repo"),
		Grant: creds.AppGrant{
			Permissions:  map[string]string{"metadata": "read", "pull_requests": "read"},
			Repositories: []string{"your-repo"},
		},
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	client, err := github.New(conn, forgeapi.WithCredentialSource(src))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer client.Close()

	// An installation token cannot list GET /user/repos, so the list names the
	// installation's account. Each RepoRef comes from that answer.
	repos, err := client.ListRepos(ctx, forgeapi.WithOwner("your-org"))
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, repo := range repos.Items {
		prs, err := client.ListPRs(ctx, repo.Ref, forgeapi.WithState(forgeapi.ListStateOpen))
		if err != nil {
			fmt.Println(err)
			return
		}
		fmt.Println(repo.Ref.DisplayPath, len(prs.Items), "open pull requests")
	}

	tok, err := src.Mint(ctx)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("token valid until", tok.Expiry.Format(time.RFC3339))
}
