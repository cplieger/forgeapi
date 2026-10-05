package creds_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
	"github.com/cplieger/forgeapi/github"
)

// Example signs a user in to github.com through the OAuth device grant, stores the
// credential the grant answers, and runs a client behind the source that keeps that
// credential fresh. It is the whole lifecycle a consumer wires: render the user
// code, poll at the interval the grant states, save the record under the
// consumer's own key for the connection, and hand the client a source over that
// key rather than a token.
//
// The example carries no Output comment, so the toolchain compiles it and does not
// run it: running it would open a real grant against github.com.
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
	src, err := creds.NewSource(store, key, conn)
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
