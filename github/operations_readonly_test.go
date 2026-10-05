package github

import (
	"context"
	"errors"
	"testing"

	"github.com/cplieger/forgeapi"
)

// reads is every read operation of this family's client, each called once, the two
// arms of each cross-repository list included.
var reads = []struct {
	call func(ctx context.Context, c *Client) error
	name string
}{
	{name: "Whoami", call: func(ctx context.Context, c *Client) error { _, err := c.Whoami(ctx); return err }},
	{name: "ListRepos", call: func(ctx context.Context, c *Client) error { _, err := c.ListRepos(ctx); return err }},
	{name: "ListPRs", call: func(ctx context.Context, c *Client) error { _, err := c.ListPRs(ctx, testRef()); return err }},
	{name: "ListMyPRs", call: func(ctx context.Context, c *Client) error { _, err := c.ListMyPRs(ctx); return err }},
	{name: "ListMyPRs_owner", call: func(ctx context.Context, c *Client) error {
		_, err := c.ListMyPRs(ctx, forgeapi.WithOwner(testOwner))
		return err
	}},
	{name: "ListMyIssues", call: func(ctx context.Context, c *Client) error { _, err := c.ListMyIssues(ctx); return err }},
	{name: "ListMyIssues_owner", call: func(ctx context.Context, c *Client) error {
		_, err := c.ListMyIssues(ctx, forgeapi.WithOwner(testOwner))
		return err
	}},
	{name: "ReadPR", call: func(ctx context.Context, c *Client) error { _, err := c.ReadPR(ctx, testRef(), testPR()); return err }},
	{name: "ListRuns", call: func(ctx context.Context, c *Client) error { _, err := c.ListRuns(ctx, testRef()); return err }},
	{name: "MergeStatus", call: func(ctx context.Context, c *Client) error {
		_, err := c.MergeStatus(ctx, testRef(), testPR())
		return err
	}},
	{name: "CommitStatus", call: func(ctx context.Context, c *Client) error {
		_, err := c.CommitStatus(ctx, testRef(), testHeadSHA)
		return err
	}},
	{name: "ListIssues", call: func(ctx context.Context, c *Client) error { _, err := c.ListIssues(ctx, testRef()); return err }},
	{name: "ListReleases", call: func(ctx context.Context, c *Client) error { _, err := c.ListReleases(ctx, testRef()); return err }},
	{name: "ListLabels", call: func(ctx context.Context, c *Client) error { _, err := c.ListLabels(ctx, testRef()); return err }},
}

// TestEveryReadReachesTheInstanceOnAReadOnlyClient holds WithMutations(false) to the
// boundary its doc names, the mutating operations: every read, the ones this product
// answers from a GraphQL document sent as a POST included, reaches the instance,
// whatever the instance then answers.
func TestEveryReadReachesTheInstanceOnAReadOnlyClient(t *testing.T) {
	for _, read := range reads {
		t.Run(read.name, func(t *testing.T) {
			h := newHarness(t, nil, forgeapi.WithMutations(false))
			err := read.call(t.Context(), h.client)
			var fe *forgeapi.Error
			if asForgeError(err, &fe) && fe.Code == forgeapi.CodeMutationsDisabled {
				t.Errorf("%s on a read-only client = code %q, want the read sent: it mutates nothing", read.name, fe.Code)
			}
			if got := h.instance.count(); got == 0 {
				t.Errorf("%s on a read-only client sent no request, want at least one", read.name)
			}
		})
	}
}

// TestARerunOnAReadOnlyClientSendsNothing holds WithMutations(false) to the operation
// boundary its doc names for RerunFailedChecks: the read that resolves the run is part
// of the mutating operation, so a read-only client refuses before anything is sent.
func TestARerunOnAReadOnlyClientSendsNothing(t *testing.T) {
	h := newHarness(t, nil, forgeapi.WithMutations(false))
	before := h.instance.count()
	err := h.client.RerunFailedChecks(t.Context(), testRef(), testPR(), testHeadSHA)
	var fe *forgeapi.Error
	if !errors.As(err, &fe) || fe.Code != forgeapi.CodeMutationsDisabled {
		t.Errorf("RerunFailedChecks on a read-only client = %v, want code %q", err, forgeapi.CodeMutationsDisabled)
	}
	if sent := h.instance.count() - before; sent != 0 {
		t.Errorf("RerunFailedChecks on a read-only client sent %d requests, want none", sent)
	}
}
