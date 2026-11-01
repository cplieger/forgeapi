// Package spec is forgeapi's expectation table: what each operation does on each
// product, held once, each entry stating what a forge was measured or documented
// to do.
//
// Three consumers read it and nothing else states these facts. The generator
// under internal/gen renders the per-forge block of every role method's godoc
// and the support matrix in SUPPORT.md; gen_test.go fails when either would
// change; and the conformance suite iterates one case per entry.
//
//nolint:goconst // each entry spells its method and route literally, so a row reads whole without a constant to look up
package spec

// Product is one of the four forge products, spelled as every rendering spells it.
type Product string

// The four products. Products fixes the column order: GitHub first because it is
// the measured column, then GitLab, then the two the Gitea family serves.
const (
	GitHub  Product = "GitHub"
	GitLab  Product = "GitLab"
	Gitea   Product = "Gitea"
	Forgejo Product = "Forgejo"
)

// Products is the column order of every generated rendering.
var Products = [...]Product{GitHub, GitLab, Gitea, Forgejo}

// Support is how much of an operation a product serves.
type Support string

// The support statuses a matrix cell can carry. Pending claims no support on
// that product yet: either no family implements the operation there, or the
// instance measured answered it in a way the normalized contract does not cover,
// so a claim of support would be a claim the measurement does not make.
// Unsupported is the one status an implementation cannot change, because it says
// the PRODUCT carries no such operation, so detection refuses the call on every
// instance of it.
//
// There is no partly-supported status. A departure is per FIELD and rides the
// generated godoc beside the field it qualifies, so a cell standing for
// "supported with a note" would state the same fact at a grain that cannot name
// which field it is about.
const (
	Supported   Support = "supported"
	Unsupported Support = "unsupported"
	Pending     Support = "pending"
)

// Kind is why a product's field departs from the normalized contract.
type Kind string

// The four departure kinds. The first three are a field the product cannot
// supply, one that arrives in another structure, and one that arrives in another
// form. The fourth is a cell no measurement lane could read,
// which is a departure from a CONFIRMED contract rather than from the contract
// itself, and its Evidence names the instrument that closes it.
const (
	CannotSupply      Kind = "cannot supply"
	DiffersInShape    Kind = "differs in shape"
	DiffersInSpelling Kind = "differs in spelling"
	NotMeasured       Kind = "not measured"
)

// Requests is one call's price in requests the instance bills: every request sent,
// failed attempts included, less a not-modified answer a product prices at zero in
// Revalidations. Min and Max are equal for one figure and differ for a range, whose
// arms are a degradation path or a fold's pages rather than an estimate.
type Requests struct {
	Min int
	Max int
	// PerItem counts the further requests each row costs: the Gitea family's
	// folded status, one read per row.
	PerItem int
	// PerItemCeiling counts the further requests a row may cost beyond PerItem:
	// GitLab's fork lookup on its cross-repository list, one per distinct fork.
	PerItemCeiling int
	// Paged says the figure is per page of a continued list, capped by
	// Budget.ListPages.
	Paged bool
}

// Departure is one field a product does not deliver as the contract states.
//
// Says is the measurement's own words about that cell, kept verbatim so the
// generated documentation states what was read rather than a paraphrase.
// Evidence is empty on a measured departure and names the instrument on one that
// is not measured: CREDENTIAL and HOST for a read nobody has taken, the SANDBOX
// forms for an answer only a write could give, and a PARTLY prefix where one
// half of the cell was read and the other was not.
type Departure struct {
	Field    string
	Kind     Kind
	Says     string
	Evidence string
}

// Where is the part of one request a [Sent] names.
type Where string

// The two parts of a request a decision can ride. A product that takes a filter
// as a query parameter and one that takes the same fact in a body are stating the
// same decision in two places, which is why the part is per product rather than
// per operation.
const (
	InBody  Where = "body"
	InQuery Where = "query"
)

// Holds is what decides the value one [Sent] names, which is what lets the table
// state a request fact without holding a fixture's own values: a literal is
// spelled here, and everything else is the value the CALLER supplied, resolved by
// whoever drives the call.
type Holds string

// What a sent field can hold. HoldsNothing is the absence of the field itself,
// which is a decision as much as a value is: a request that omits a flag gets the
// product's default for it, and a default that arms something is the defect this
// column exists to catch.
const (
	HoldsLiteral    Holds = "literal"
	HoldsHeadSHA    Holds = "head SHA"
	HoldsLabelNames Holds = "label names"
	HoldsLabelIDs   Holds = "label ids"
	HoldsSinceBound Holds = "since bound"
	HoldsNothing    Holds = "nothing"
)

// Sent is one fact about what a call PUTS ON THE WIRE: a request field a consumer
// relies on, where a wrong default would change the request silently and no
// output assertion would notice.
//
// Field is the product's own wire spelling. Value is the JSON the field carries
// and is read only where Holds is HoldsLiteral. Says states the decision the
// field carries, and it is what the generated documentation publishes, so the
// library says what it sends as well as what it answers.
type Sent struct {
	Field string
	Where Where
	Holds Holds
	Value string
	Says  string
}

// Entry is one operation on one product.
//
// Exercises names the endpoint or the GraphQL document the call reaches. It is
// EMPTY in two cases the Support status tells apart, because an unrouted call is
// not one thing: on a Pending entry no route is settled YET, which the
// generator renders as unsettled and which is the family implementer's to decide;
// on an Unsupported entry the product carries no such verb, which the generator
// renders as the refusal it is and which a departure row of that entry states.
// Needs is the capability a detection can refuse the call on, empty where none
// gates it. Departures holds
// only the fields that depart: a field arriving exactly as the contract states
// has no row here, which is what keeps the generated blocks short. Sends holds
// the request facts a consumer relies on, and it is empty on the operations whose
// request carries no decision and on a column whose family nobody has implemented,
// where a claim about what the code sends would be a claim about code that does
// not run.
type Entry struct {
	Method     string
	Product    Product
	Exercises  string
	Needs      string
	Support    Support
	Departures []Departure
	Sends      []Sent
	Requests   Requests
}

// Fields counts the output fields each operation publishes, which is one row of
// its measured table and the same count on all four products. A rendering reads
// it to tell a column that departs on every field from one that departs on most.
var Fields = map[string]int{
	"Identity.Whoami":                5,
	"Repos.ListRepos":                17,
	"PullRequests.ListPRs":           30,
	"PullRequests.ListMyPRs":         30,
	"PullRequests.ReadPR":            27,
	"PullRequests.CreatePR":          27,
	"PullRequests.ClosePR":           27,
	"PullRequests.ReopenPR":          27,
	"PullRequests.RerunFailedChecks": 2,
	"Merges.MergePR":                 4,
	"Merges.MergeStatus":             4,
	"Checks.CommitStatus":            10,
	"Checks.ListRuns":                15,
	"Issues.ListIssues":              17,
	"Issues.ListMyIssues":            17,
	"Issues.CreateIssue":             14,
	"Issues.CloseIssue":              14,
	"Capabilities.ConnectionCaps":    4,
	"Capabilities.GrantCaps":         2,
	"Capabilities.RepoAffordances":   6,
	"Governor.BudgetState":           5,
	"Releases.ListReleases":          10,
	"Releases.CreateRelease":         7,
	"Labels.ListLabels":              6,
}

// Table is the expectation table: one entry per operation per product, in the
// operation order the roles declare and the product order every rendering uses.
var Table = []Entry{
	{
		Method:    "Identity.Whoami",
		Product:   GitHub,
		Exercises: "GET /user, whose X-OAuth-Scopes header carries the scopes",
		Requests:  Requests{Min: 1, Max: 1},
		Support:   Supported,
	},
	{
		Method:    "Identity.Whoami",
		Product:   GitLab,
		Exercises: "GET /api/v4/user",
		Requests:  Requests{Min: 1, Max: 1},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Scopes",
				Kind:  CannotSupply,
				Says:  "nil: the user route carries no scope list among its 41 keys, and the token route's scopes describe one token rather than the grant",
			},
			{
				Field:    "error, scope insufficient",
				Kind:     NotMeasured,
				Says:     "unmeasured, needs a GitLab token holding no user read: the fine-grained token measured holds User: Read and the user route answered it 200",
				Evidence: "CREDENTIAL",
			},
		},
	},
	{
		Method:    "Identity.Whoami",
		Product:   Gitea,
		Exercises: "GET /api/v1/user",
		Requests:  Requests{Min: 1, Max: 1},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Scopes",
				Kind:  CannotSupply,
				Says:  "nil: the user route carries no scope list and its response no scope header",
			},
		},
	},
	{
		Method:    "Identity.Whoami",
		Product:   Forgejo,
		Exercises: "GET /api/v1/user",
		Requests:  Requests{Min: 1, Max: 1},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Scopes",
				Kind:  CannotSupply,
				Says:  "nil: the user route carries no scope list and its response no scope header",
			},
		},
	},
	{
		Method:    "Repos.ListRepos",
		Product:   GitHub,
		Exercises: "GET /user/repos; under an owner scope the RepoOwned document, repositoryOwner(login: {owner}) with ownerAffiliations [OWNER], which resolves a user and an organization alike",
		Requests:  Requests{Min: 0, Max: 1, Paged: true},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Items[].Affordances.MergeStrategies",
				Kind:  CannotSupply,
				Says:  "empty: absent on a listing row without an owner scope",
			},
			{
				Field: "Items[].Affordances.MergeTrain",
				Kind:  CannotSupply,
				Says:  "no record field: a ruleset property",
			},
			{
				Field: "Items[].CloneURL, under an owner scope",
				Kind:  DiffersInShape,
				Says:  "the document has no HTTPS clone field, so it is the repository url with .git appended",
			},
		},
	},
	{
		Method:    "Repos.ListRepos",
		Product:   GitLab,
		Exercises: "GET /api/v4/projects; under an owner scope GET /api/v4/groups/{owner}/projects with include_subgroups=true and with_shared=false, and where that answers 404 for a one-segment owner, GET /api/v4/users/{owner}/projects, the user's personal namespace, on the first page alone, since the continuation names the route; an owner named by digits alone, which every namespace route reads as a numeric id, is first resolved by name through the OwnerLookup document to the id the route takes, one request more on every page, the group or the user it names read with no fallback",
		Requests:  Requests{Min: 1, Max: 2, Paged: true},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Items[].Ref",
				Kind:  DiffersInSpelling,
				Says:  "namespace path selector",
			},
			{
				Field: "Items[].Affordances.HasIssues",
				Kind:  DiffersInShape,
				Says:  "varies by permission",
			},
			{
				Field: "Items[].Affordances.CanPush",
				Kind:  DiffersInShape,
				Says:  "from a role integer",
			},
			{
				Field: "Items[].Affordances.CanPush, under a group owner scope",
				Kind:  CannotSupply,
				Says:  "SupportUnknown: the group route answers its rows without the permissions object the other two routes carry",
			},
			{
				Field: "Items[].Affordances.Ev",
				Kind:  CannotSupply,
				Says:  "no source on the row for its three keys, anonymous",
			},
			{
				Field: "Items[].Private",
				Kind:  DiffersInShape,
				Says:  "visibility string, no private key",
			},
			{
				Field: "error, owner unresolved",
				Kind:  CannotSupply,
				Says:  "no refusal for a user whose profile is private: the user route answers it an empty list, so such an owner reads as owning nothing",
			},
		},
	},
	{
		Method:    "Repos.ListRepos",
		Product:   Gitea,
		Exercises: "GET /api/v1/user/repos; under an owner scope GET /api/v1/users/{owner}/repos, which resolves a user and an organization alike and pages before it drops the rows the caller has no unit access to, so its walk continues while the pages read cover fewer rows than X-Total-Count",
		Requests:  Requests{Min: 1, Max: 1, Paged: true},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Items[].Affordances.MergeTrain",
				Kind:  CannotSupply,
				Says:  "SupportNo: no queue",
			},
		},
	},
	{
		Method:    "Repos.ListRepos",
		Product:   Forgejo,
		Exercises: "GET /api/v1/user/repos; under an owner scope GET /api/v1/users/{owner}/repos, which resolves a user and an organization alike and pages before it drops the rows the caller has no unit access to, so its walk continues while the pages read cover fewer rows than X-Total-Count",
		Requests:  Requests{Min: 1, Max: 1, Paged: true},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Items[].Affordances.MergeTrain",
				Kind:  CannotSupply,
				Says:  "SupportNo: no queue",
			},
		},
	},
	{
		Method:    "PullRequests.ListPRs",
		Product:   GitHub,
		Exercises: "the PRList document, asking for at most 50 rows a page whatever the page bound asks, since a larger page of its rows answered 502 on every attempt on a large repository",
		Requests:  Requests{Min: 1, Max: 1, Paged: true},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Items[].Action.Checks, under a fine-grained token",
				Kind:  CannotSupply,
				Says:  "CheckUnknown with no count, beside forbidden on the row's own Partial and no page marker: a fine-grained token holds no Checks permission, and on a private repository the document answered every field of the row and FORBIDDEN on its rollup's contexts nodes, measured 2026-10-11 on the PRMine search, which selects the same rollup",
			},
		},
	},
	{
		Method:    "PullRequests.ListPRs",
		Product:   GitLab,
		Exercises: "the PRList document, or GET /api/v4/projects/{id}/merge_requests where this connection's setup found the documents refused",
		Requests:  Requests{Min: 1, Max: 1, Paged: true},
		Needs:     "read_merge_state",
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Items[].Ref",
				Kind:  DiffersInSpelling,
				Says:  "iid, sigil \"!\"",
			},
			{
				Field: "Items[].Labels[].Name",
				Kind:  DiffersInSpelling,
				Says:  "labels.nodes[].title, not name",
			},
			{
				Field: "Items[].Labels[].Color",
				Kind:  DiffersInSpelling,
				Says:  "color string, leading #",
			},
			{
				Field: "Items[].Action.Checks",
				Kind:  DiffersInShape,
				Says:  "headPipeline.status scalar, no fold",
			},
			{
				Field: "Items[].Action.ChecksPassing .. ChecksTotal",
				Kind:  CannotSupply,
				Says:  "absent: one scalar status",
			},
			{
				Field: "Items[].Action.QueueState",
				Kind:  CannotSupply,
				Says:  "QueueNone always: every field that would name a train carries a deprecation reason, so no document can select one",
			},
			{
				Field: "Items[].Action.QueuePosition",
				Kind:  CannotSupply,
				Says:  "no position; train index null",
			},
			{
				Field: "Successor",
				Kind:  CannotSupply,
				Says:  "nil: GraphQL sends no successor",
			},
		},
	},
	{
		Method:    "PullRequests.ListPRs",
		Product:   Gitea,
		Exercises: "GET /api/v1/repos/{owner}/{repo}/pulls, then each row's folded commit status, and where the repository moved, the one 301 its old path answers, whose Location names the successor, every later request of the call addressing that successor",
		Requests:  Requests{Min: 1, Max: 2, PerItem: 1, Paged: true},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Items[].Action.AutoMergeArmed",
				Kind:  CannotSupply,
				Says:  "no auto_merge key: unknown",
			},
			{
				Field: "Items[].Action.QueueState",
				Kind:  CannotSupply,
				Says:  "QueueNone always",
			},
			{
				Field: "Items[].Action.QueuePosition",
				Kind:  CannotSupply,
				Says:  "QueuePositionUnknown: no queue",
			},
			{
				Field: "Items[].Action.MergeBlocked",
				Kind:  CannotSupply,
				Says:  "mergeable and draft, no reason",
			},
		},
	},
	{
		Method:    "PullRequests.ListPRs",
		Product:   Forgejo,
		Exercises: "GET /api/v1/repos/{owner}/{repo}/pulls, then each row's folded commit status, and where the repository moved, the one 301 its old path answers, whose Location names the successor, every later request of the call addressing that successor",
		Requests:  Requests{Min: 1, Max: 2, PerItem: 1, Paged: true},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Items[].AuthorKind",
				Kind:  CannotSupply,
				Says:  "AccountKindUnknown on every row: the user object carries no type key, measured on codeberg.org 2026-10-11",
			},
			{
				Field: "Items[].Action.AutoMergeArmed",
				Kind:  CannotSupply,
				Says:  "no auto_merge key: unknown",
			},
			{
				Field: "Items[].Action.QueueState",
				Kind:  CannotSupply,
				Says:  "QueueNone always",
			},
			{
				Field: "Items[].Action.QueuePosition",
				Kind:  CannotSupply,
				Says:  "QueuePositionUnknown: no queue",
			},
			{
				Field: "Items[].Action.MergeBlocked",
				Kind:  CannotSupply,
				Says:  "mergeable and draft, no reason",
			},
		},
	},
	{
		Method:    "PullRequests.ListMyPRs",
		Product:   GitHub,
		Exercises: "the PRMine search document, asking for at most 25 rows a page whatever the page bound asks, since a larger page of its rows answered 502 under both owners measured, the smaller of them holding fewer than a hundred open pull requests; under an owner scope the same document with user:{owner} in place of the author keyword, which resolves an organization as it resolves a user",
		Requests:  Requests{Min: 1, Max: 1, Paged: true},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Items[].Action.Checks, under a fine-grained token",
				Kind:  CannotSupply,
				Says:  "CheckUnknown with no count, beside forbidden on the row's own Partial and no page marker: a fine-grained token holds no Checks permission, and on a private repository the document answered every field of the row and FORBIDDEN on its rollup's contexts nodes, measured 2026-10-11 on this document",
			},
			{
				Field: "Next",
				Kind:  DiffersInShape,
				Says:  "a continuation while hasNextPage holds, as on a viewer's first page of 20 rows over issueCount 23; past the search connection's 1,000th result, where it answers hasNextPage false while issueCount states 2673, and a page asked for after it answers no rows and a null end cursor, so a walk ends there with no continuation and carries result_window",
			},
			{
				Field: "error, owner unresolved",
				Kind:  CannotSupply,
				Says:  "empty: an owner the search index holds no account for answers issueCount 0 and no error, so no such owner reads as nothing open",
			},
		},
	},
	{
		Method:    "PullRequests.ListMyPRs",
		Product:   GitLab,
		Exercises: "GET /api/v4/merge_requests?scope=created_by_me; under an owner scope GET /api/v4/groups/{owner}/merge_requests with the open state, which answers 404 for a user's own namespace; an owner named by digits alone, which every namespace route reads as a numeric id, is first resolved by name through the OwnerLookup document to the id the route takes, one request more on every page; on either route, GET /api/v4/projects/{id} once for each distinct fork a row's source_project_id names",
		Requests:  Requests{Min: 1, Max: 2, PerItemCeiling: 1, Paged: true},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Items[].Ref",
				Kind:  DiffersInSpelling,
				Says:  "iid, sigil \"!\"",
			},
			{
				Field: "Items[].SourceRepo",
				Kind:  DiffersInShape,
				Says:  "source_project_id beside target_project_id, a fork resolved by one GET /api/v4/projects/{id} per distinct fork on the page to its path_with_namespace, the zero value where the id is null on a deleted fork or the lookup answers 404 or 403",
			},
			{
				Field: "Items[].Labels[].Color",
				Kind:  DiffersInSpelling,
				Says:  "color string, leading #, on 2 of 2, upper-case hex on 0",
			},
			{
				Field: "Items[].Action.Mergeable",
				Kind:  CannotSupply,
				Says:  "no mergeable key on a list row, which states mergeability only through detailed_merge_status, mergeable 1, unchecked 2, and answers unchecked before the asynchronous check",
			},
			{
				Field: "Items[].Action.Checks",
				Kind:  CannotSupply,
				Says:  "CheckUnknown: no head_pipeline key on 3 of 3 list rows",
			},
			{
				Field: "Items[].Action.ChecksPassing .. ChecksTotal",
				Kind:  CannotSupply,
				Says:  "zero: no head_pipeline key on a list row, so no status to count",
			},
			{
				Field: "Items[].Action.QueueState",
				Kind:  CannotSupply,
				Says:  "QueueNone always: no merge-train key among the 51 keys a list row answers",
			},
			{
				Field: "Items[].Action.QueuePosition",
				Kind:  CannotSupply,
				Says:  "QueuePositionUnknown: no merge-train key on a list row",
			},
			{
				Field: "Items[].Action.MergeBlocked",
				Kind:  CannotSupply,
				Says:  "unknown on a list row, whose detailed_merge_status is the status the product stored when it last checked, which listing may leave unrefreshed: mergeable 1, unchecked 2 on the 3 open rows both scopes answered",
			},
			{
				Field: "Items[].Partial",
				Kind:  CannotSupply,
				Says:  "nil: no status read runs for a list row, which carries no head_pipeline",
			},
			{
				Field: "error, owner unresolved",
				Kind:  CannotSupply,
				Says:  "404 Group Not Found on a user's own namespace, an account the users route answers, exactly as on an owner no namespace holds",
			},
			{
				Field: "Items[], from an archived repository under an owner scope",
				Kind:  CannotSupply,
				Says:  "none: the group route's non_archived defaults to true",
			},
		},
	},
	{
		Method:    "PullRequests.ListMyPRs",
		Product:   Gitea,
		Exercises: "GET /api/v1/repos/issues/search filtered to pull requests and to the rows the authenticated credential created, whose rows carry no head commit and therefore no folded status; under an owner scope the same route filtered by owner={owner} in place of created=true",
		Requests:  Requests{Min: 1, Max: 1, Paged: true},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Items[].SourceBranch",
				Kind:  CannotSupply,
				Says:  "no head key on an issue-search row",
			},
			{
				Field: "Items[].SourceRepo",
				Kind:  CannotSupply,
				Says:  "no head key on an issue-search row",
			},
			{
				Field: "Items[].TargetBranch",
				Kind:  CannotSupply,
				Says:  "no base key on an issue-search row",
			},
			{
				Field: "Items[].HeadSHA",
				Kind:  CannotSupply,
				Says:  "no head key on an issue-search row",
			},
			{
				Field: "Items[].Action.Mergeable",
				Kind:  CannotSupply,
				Says:  "no mergeable key on an issue-search row",
			},
			{
				Field: "Items[].Action.Checks",
				Kind:  CannotSupply,
				Says:  "CheckUnknown: no head commit on an issue-search row, so no status read addresses it",
			},
			{
				Field: "Items[].Action.ChecksPassing .. ChecksTotal",
				Kind:  CannotSupply,
				Says:  "zero: no fold runs on a row that carries no head commit",
			},
			{
				Field: "Items[].Action.AutoMergeArmed",
				Kind:  CannotSupply,
				Says:  "no auto-merge key on an issue-search row",
			},
			{
				Field: "Items[].Action.QueueState",
				Kind:  CannotSupply,
				Says:  "QueueNone always",
			},
			{
				Field: "Items[].Action.QueuePosition",
				Kind:  CannotSupply,
				Says:  "QueuePositionUnknown: no queue",
			},
			{
				Field: "Items[].Action.MergeBlocked",
				Kind:  CannotSupply,
				Says:  "no merge-blocked key on an issue-search row",
			},
			{
				Field: "Items[].Partial",
				Kind:  CannotSupply,
				Says:  "nil: no fold runs on a row that carries no head commit, so none can stop short",
			},
			{
				Field: "Items[].Draft",
				Kind:  DiffersInShape,
				Says:  "draft on the pull_request object, not at the row's top level",
			},
		},
	},
	{
		Method:    "PullRequests.ListMyPRs",
		Product:   Forgejo,
		Exercises: "GET /api/v1/repos/issues/search filtered to pull requests and to the rows the authenticated credential created, whose rows carry no head commit and therefore no folded status; under an owner scope the same route filtered by owner={owner} in place of created=true",
		Requests:  Requests{Min: 1, Max: 1, Paged: true},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Items[].AuthorKind",
				Kind:  CannotSupply,
				Says:  "AccountKindUnknown on every row: the user object carries no type key, measured on codeberg.org 2026-10-11",
			},
			{
				Field: "Items[].SourceBranch",
				Kind:  CannotSupply,
				Says:  "no head key on an issue-search row",
			},
			{
				Field: "Items[].SourceRepo",
				Kind:  CannotSupply,
				Says:  "no head key on an issue-search row",
			},
			{
				Field: "Items[].TargetBranch",
				Kind:  CannotSupply,
				Says:  "no base key on an issue-search row",
			},
			{
				Field: "Items[].HeadSHA",
				Kind:  CannotSupply,
				Says:  "no head key on an issue-search row",
			},
			{
				Field: "Items[].Action.Mergeable",
				Kind:  CannotSupply,
				Says:  "no mergeable key on an issue-search row",
			},
			{
				Field: "Items[].Action.Checks",
				Kind:  CannotSupply,
				Says:  "CheckUnknown: no head commit on an issue-search row, so no status read addresses it",
			},
			{
				Field: "Items[].Action.ChecksPassing .. ChecksTotal",
				Kind:  CannotSupply,
				Says:  "zero: no fold runs on a row that carries no head commit",
			},
			{
				Field: "Items[].Action.AutoMergeArmed",
				Kind:  CannotSupply,
				Says:  "no auto-merge key on an issue-search row",
			},
			{
				Field: "Items[].Action.QueueState",
				Kind:  CannotSupply,
				Says:  "QueueNone always",
			},
			{
				Field: "Items[].Action.QueuePosition",
				Kind:  CannotSupply,
				Says:  "QueuePositionUnknown: no queue",
			},
			{
				Field: "Items[].Action.MergeBlocked",
				Kind:  CannotSupply,
				Says:  "no merge-blocked key on an issue-search row",
			},
			{
				Field: "Items[].Partial",
				Kind:  CannotSupply,
				Says:  "nil: no fold runs on a row that carries no head commit, so none can stop short",
			},
			{
				Field: "Items[].Draft",
				Kind:  DiffersInShape,
				Says:  "draft on the pull_request object, not at the row's top level",
			},
		},
	},
	{
		Method:    "PullRequests.ReadPR",
		Product:   GitHub,
		Exercises: "the PRRead document, then a further page of its contexts connection, or GET /repos/{owner}/{repo}/pulls/{number} where this connection has already discovered the documents refused, plus one read against the id-addressed location to resolve the successor where that record answered from another address",
		Requests:  Requests{Min: 0, Max: 3},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Action.Checks, under a fine-grained token",
				Kind:  CannotSupply,
				Says:  "CheckUnknown with no count, beside forbidden on Partial: a fine-grained token holds no Checks permission, and the document refuses the rollup's contexts nodes on a private repository with FORBIDDEN, measured on the PRMine search, which selects the same rollup",
			},
		},
	},
	{
		Method:    "PullRequests.ReadPR",
		Product:   GitLab,
		Exercises: "the PRRead document, or GET /api/v4/projects/{id}/merge_requests/{iid} where this connection's setup found the documents refused",
		Requests:  Requests{Min: 1, Max: 1},
		Needs:     "read_merge_state",
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Ref",
				Kind:  DiffersInSpelling,
				Says:  "iid, sigil \"!\"",
			},
			{
				Field: "Labels[].Name",
				Kind:  DiffersInSpelling,
				Says:  "labels.nodes[].title, not name",
			},
			{
				Field: "Labels[].Color",
				Kind:  DiffersInSpelling,
				Says:  "color string, leading #",
			},
			{
				Field: "Action.Checks",
				Kind:  DiffersInShape,
				Says:  "headPipeline.status scalar, no fold",
			},
			{
				Field: "Action.ChecksPassing .. ChecksTotal",
				Kind:  CannotSupply,
				Says:  "absent: one scalar status",
			},
			{
				Field: "Action.QueueState",
				Kind:  CannotSupply,
				Says:  "QueueNone always: every field that would name a train carries a deprecation reason, so no document can select one",
			},
			{
				Field: "Action.QueuePosition",
				Kind:  CannotSupply,
				Says:  "no position; train index null",
			},
		},
	},
	{
		Method:    "PullRequests.ReadPR",
		Product:   Gitea,
		Exercises: "GET /api/v1/repos/{owner}/{repo}/pulls/{number}, then the complete commit-status fold, and where the repository moved, the one 301 its first read answers, whose Location names the successor the stale refusal carries",
		Requests:  Requests{Min: 2, Max: 4},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Action.AutoMergeArmed",
				Kind:  CannotSupply,
				Says:  "no auto_merge key: unknown",
			},
			{
				Field: "Action.QueueState",
				Kind:  CannotSupply,
				Says:  "QueueNone always",
			},
			{
				Field: "Action.QueuePosition",
				Kind:  CannotSupply,
				Says:  "QueuePositionUnknown: no queue",
			},
			{
				Field: "Action.MergeBlocked",
				Kind:  CannotSupply,
				Says:  "mergeable and draft, no reason",
			},
		},
	},
	{
		Method:    "PullRequests.ReadPR",
		Product:   Forgejo,
		Exercises: "GET /api/v1/repos/{owner}/{repo}/pulls/{number}, then the complete commit-status fold, and where the repository moved, the one 301 its first read answers, whose Location names the successor the stale refusal carries",
		Requests:  Requests{Min: 2, Max: 4},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "AuthorKind",
				Kind:  CannotSupply,
				Says:  "AccountKindUnknown: the user object carries no type key, measured on codeberg.org 2026-10-11",
			},
			{
				Field: "Action.AutoMergeArmed",
				Kind:  CannotSupply,
				Says:  "no auto_merge key: unknown",
			},
			{
				Field: "Action.QueueState",
				Kind:  CannotSupply,
				Says:  "QueueNone always",
			},
			{
				Field: "Action.QueuePosition",
				Kind:  CannotSupply,
				Says:  "QueuePositionUnknown: no queue",
			},
			{
				Field: "Action.MergeBlocked",
				Kind:  CannotSupply,
				Says:  "mergeable and draft, no reason",
			},
		},
	},
	{
		Method:    "PullRequests.CreatePR",
		Product:   GitHub,
		Exercises: "POST /repos/{owner}/{repo}/pulls, then POST /repos/{owner}/{repo}/issues/{number}/labels where the caller names labels, which is the route this product declares for them because its creation takes none, plus one read against the id-addressed location to resolve the successor where either of them answered from another address",
		Requests:  Requests{Min: 1, Max: 3},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Labels[].Name",
				Kind:  CannotSupply,
				Says:  "no label row on a create",
			},
			{
				Field: "Labels[].Color",
				Kind:  CannotSupply,
				Says:  "no label row on a create",
			},
			{
				Field: "Labels[].Description",
				Kind:  CannotSupply,
				Says:  "no label row on a create",
			},
			{
				Field: "Action.Mergeable",
				Kind:  CannotSupply,
				Says:  "unknown on a mutation's own answer, whose mergeable is the asynchronous check's state as the creation lands, null on every creation measured",
			},
			{
				Field: "Action.QueueState",
				Kind:  CannotSupply,
				Says:  "no merge-queue key among 46",
			},
			{
				Field: "Action.QueuePosition",
				Kind:  CannotSupply,
				Says:  "no queue key, so unknown",
			},
			{
				Field: "Action.MergeBlocked",
				Kind:  CannotSupply,
				Says:  "unknown on a mutation's own answer, whose mergeable_state is the asynchronous check's state as the creation lands, unknown on every creation measured",
			},
		},
		Sends: []Sent{
			{
				Field: "labels",
				Where: InBody,
				Holds: HoldsLabelNames,
				Says:  "the labels the caller named, as the NAMES the labels route takes, on the second request this row prices: this product's pull-request creation declares no labels parameter at all, so a creation that sent them there would have them ignored and one that dropped them would answer a pull request the caller did not ask for",
			},
		},
	},
	{
		Method:    "PullRequests.CreatePR",
		Product:   GitLab,
		Exercises: "POST /api/v4/projects/{id}/merge_requests",
		Requests:  Requests{Min: 1, Max: 1},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Ref",
				Kind:  DiffersInSpelling,
				Says:  "iid, sigil \"!\"",
			},
			{
				Field: "Labels[].Color",
				Kind:  CannotSupply,
				Says:  "no colour on the answer: its labels arrive as name strings",
			},
			{
				Field: "Labels[].Description",
				Kind:  CannotSupply,
				Says:  "no description on the answer: its labels arrive as name strings",
			},
			{
				Field: "Action.Mergeable",
				Kind:  CannotSupply,
				Says:  "merge_status checking and detailed_merge_status preparing on the answer, unknown until the asynchronous check settles",
			},
			{
				Field: "Action.Checks",
				Kind:  CannotSupply,
				Says:  "CheckUnknown: head_pipeline null on the creation answer, which the product sets after it, null here although the source branch's pipeline had already started on its head",
			},
			{
				Field: "Action.ChecksPassing .. ChecksTotal",
				Kind:  CannotSupply,
				Says:  "zero: head_pipeline null on the answer, so no status to count",
			},
			{
				Field: "Action.QueueState",
				Kind:  CannotSupply,
				Says:  "QueueNone always: no merge-train key among the 61 keys the answer carries",
			},
			{
				Field: "Action.QueuePosition",
				Kind:  CannotSupply,
				Says:  "QueuePositionUnknown: no merge-train key on the answer",
			},
			{
				Field: "Action.MergeBlocked",
				Kind:  CannotSupply,
				Says:  "unknown on a mutation's own answer, whose detailed_merge_status is the asynchronous check's state as the creation lands, preparing on every creation measured",
			},
			{
				Field: "State",
				Kind:  DiffersInSpelling,
				Says:  "state string, opened",
			},
		},
		Sends: []Sent{
			{
				Field: "labels",
				Where: InBody,
				Holds: HoldsLabelNames,
				Says:  "the labels the caller named, as the NAMES this product's creation takes, so nothing has to resolve them first",
			},
		},
	},
	{
		Method:    "PullRequests.CreatePR",
		Product:   Gitea,
		Exercises: "GET /api/v1/repos/{owner}/{repo}/labels where the caller names labels, whose ids this product's option takes in place of their names, then POST /api/v1/repos/{owner}/{repo}/pulls",
		Requests:  Requests{Min: 1, Max: 2},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Action.Mergeable",
				Kind:  CannotSupply,
				Says:  "unknown on a mutation's own answer, whose mergeable bool is false while the instance's asynchronous check holds the pull request queued as the creation lands, true on every creation measured",
			},
			{
				Field: "Action.AutoMergeArmed",
				Kind:  CannotSupply,
				Says:  "no auto_merge key: unknown",
			},
			{
				Field: "Action.QueueState",
				Kind:  CannotSupply,
				Says:  "QueueNone always",
			},
			{
				Field: "Action.QueuePosition",
				Kind:  CannotSupply,
				Says:  "QueuePositionUnknown: no queue",
			},
			{
				Field: "Action.MergeBlocked",
				Kind:  CannotSupply,
				Says:  "mergeable and draft, no reason",
			},
		},
		Sends: []Sent{
			{
				Field: "labels",
				Where: InBody,
				Holds: HoldsLabelIDs,
				Says:  "the labels the caller named, as the numeric ids this product's creation declares in place of their names, which is what the label read this row prices resolves",
			},
		},
	},
	{
		Method:    "PullRequests.CreatePR",
		Product:   Forgejo,
		Exercises: "GET /api/v1/repos/{owner}/{repo}/labels where the caller names labels, whose ids this product's option takes in place of their names, then POST /api/v1/repos/{owner}/{repo}/pulls",
		Requests:  Requests{Min: 1, Max: 2},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "AuthorKind",
				Kind:  CannotSupply,
				Says:  "AccountKindUnknown: the user object carries no type key, measured on codeberg.org 2026-10-11",
			},
			{
				Field: "Action.Mergeable",
				Kind:  CannotSupply,
				Says:  "unknown on a mutation's own answer, whose mergeable bool is false while the instance's asynchronous check holds the pull request queued as the creation lands, true on every creation measured",
			},
			{
				Field: "Action.AutoMergeArmed",
				Kind:  CannotSupply,
				Says:  "no auto_merge key: unknown",
			},
			{
				Field: "Action.QueueState",
				Kind:  CannotSupply,
				Says:  "QueueNone always",
			},
			{
				Field: "Action.QueuePosition",
				Kind:  CannotSupply,
				Says:  "QueuePositionUnknown: no queue",
			},
			{
				Field: "Action.MergeBlocked",
				Kind:  CannotSupply,
				Says:  "mergeable and draft, no reason",
			},
		},
		Sends: []Sent{
			{
				Field: "labels",
				Where: InBody,
				Holds: HoldsLabelIDs,
				Says:  "the labels the caller named, as the numeric ids this product's creation declares in place of their names, which is what the label read this row prices resolves",
			},
		},
	},
	{
		Method:    "PullRequests.ClosePR",
		Product:   GitHub,
		Exercises: "PATCH /repos/{owner}/{repo}/pulls/{number}, then one read against the id-addressed location to resolve the successor where it answered from another address",
		Requests:  Requests{Min: 1, Max: 2},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Action.Mergeable",
				Kind:  CannotSupply,
				Says:  "unknown on a mutation's own answer, whose mergeable is the asynchronous check's state as the close lands: null before the check settles, true after it",
			},
			{
				Field: "Action.QueueState",
				Kind:  CannotSupply,
				Says:  "no merge-queue key among 46",
			},
			{
				Field: "Action.QueuePosition",
				Kind:  CannotSupply,
				Says:  "no queue key, so unknown",
			},
			{
				Field: "Action.MergeBlocked",
				Kind:  CannotSupply,
				Says:  "unknown on a mutation's own answer, whose mergeable_state is the asynchronous check's state as the close lands: unknown before the check settles, clean after it",
			},
		},
	},
	{
		Method:    "PullRequests.ClosePR",
		Product:   GitLab,
		Exercises: "PUT /api/v4/projects/{id}/merge_requests/{iid}",
		Requests:  Requests{Min: 1, Max: 1},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Ref",
				Kind:  DiffersInSpelling,
				Says:  "iid, sigil \"!\"",
			},
			{
				Field: "SourceRepo",
				Kind:  DiffersInShape,
				Says:  "source_project_id beside target_project_id, a fork named by id alone: the addressed project where the two are equal, the zero value otherwise",
			},
			{
				Field: "Labels[].Color",
				Kind:  CannotSupply,
				Says:  "no colour on the answer: its labels arrive as name strings",
			},
			{
				Field: "Labels[].Description",
				Kind:  CannotSupply,
				Says:  "no description on the answer: its labels arrive as name strings",
			},
			{
				Field: "Action.Mergeable",
				Kind:  CannotSupply,
				Says:  "merge_status checking and detailed_merge_status preparing on the answer before the asynchronous check settles, can_be_merged and not_open after it, unknown on both: the REST arm reads no mergeable flag",
			},
			{
				Field: "Action.Checks",
				Kind:  DiffersInShape,
				Says:  "head_pipeline.status scalar on the answer, no fold: null where the head ran no pipeline, so CheckUnknown, and the head pipeline's own status, running here, where one ran",
			},
			{
				Field: "Action.ChecksPassing .. ChecksTotal",
				Kind:  CannotSupply,
				Says:  "zero: the answer's head_pipeline is one scalar status, null or set, with nothing under it to count",
			},
			{
				Field: "Action.QueueState",
				Kind:  CannotSupply,
				Says:  "QueueNone always: no merge-train key among the 61 keys the answer carries",
			},
			{
				Field: "Action.QueuePosition",
				Kind:  CannotSupply,
				Says:  "QueuePositionUnknown: no merge-train key on the answer",
			},
			{
				Field: "Action.MergeBlocked",
				Kind:  CannotSupply,
				Says:  "unknown on a mutation's own answer, whose detailed_merge_status is the asynchronous check's state as the close lands: preparing before the check settles, not_open after it, checking after a reopen",
			},
		},
	},
	{
		Method:    "PullRequests.ClosePR",
		Product:   Gitea,
		Exercises: "PATCH /api/v1/repos/{owner}/{repo}/pulls/{number}",
		Requests:  Requests{Min: 1, Max: 1},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Action.Mergeable",
				Kind:  CannotSupply,
				Says:  "unknown on a mutation's own answer, whose mergeable bool is false while the instance's asynchronous check holds the pull request queued as the close lands, true on every close measured",
			},
			{
				Field: "Action.AutoMergeArmed",
				Kind:  CannotSupply,
				Says:  "no auto_merge key: unknown",
			},
			{
				Field: "Action.QueueState",
				Kind:  CannotSupply,
				Says:  "QueueNone always",
			},
			{
				Field: "Action.QueuePosition",
				Kind:  CannotSupply,
				Says:  "QueuePositionUnknown: no queue",
			},
			{
				Field: "Action.MergeBlocked",
				Kind:  CannotSupply,
				Says:  "mergeable and draft, no reason",
			},
		},
	},
	{
		Method:    "PullRequests.ClosePR",
		Product:   Forgejo,
		Exercises: "PATCH /api/v1/repos/{owner}/{repo}/pulls/{number}",
		Requests:  Requests{Min: 1, Max: 1},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "AuthorKind",
				Kind:  CannotSupply,
				Says:  "AccountKindUnknown: the user object carries no type key, measured on codeberg.org 2026-10-11",
			},
			{
				Field: "Action.Mergeable",
				Kind:  CannotSupply,
				Says:  "unknown on a mutation's own answer, whose mergeable bool is false while the instance's asynchronous check holds the pull request queued as the close lands, true on every close measured",
			},
			{
				Field: "Action.AutoMergeArmed",
				Kind:  CannotSupply,
				Says:  "no auto_merge key: unknown",
			},
			{
				Field: "Action.QueueState",
				Kind:  CannotSupply,
				Says:  "QueueNone always",
			},
			{
				Field: "Action.QueuePosition",
				Kind:  CannotSupply,
				Says:  "QueuePositionUnknown: no queue",
			},
			{
				Field: "Action.MergeBlocked",
				Kind:  CannotSupply,
				Says:  "mergeable and draft, no reason",
			},
		},
	},
	{
		Method:    "PullRequests.ReopenPR",
		Product:   GitHub,
		Exercises: "PATCH /repos/{owner}/{repo}/pulls/{number}, then one read against the id-addressed location to resolve the successor where it answered from another address",
		Requests:  Requests{Min: 1, Max: 2},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Action.Mergeable",
				Kind:  CannotSupply,
				Says:  "unknown on a mutation's own answer, whose mergeable is the asynchronous check's state as the reopen lands, null on every reopen measured",
			},
			{
				Field: "Action.QueueState",
				Kind:  CannotSupply,
				Says:  "no merge-queue key among 46",
			},
			{
				Field: "Action.QueuePosition",
				Kind:  CannotSupply,
				Says:  "no queue key, so unknown",
			},
			{
				Field: "Action.MergeBlocked",
				Kind:  CannotSupply,
				Says:  "unknown on a mutation's own answer, whose mergeable_state is the asynchronous check's state as the reopen lands, unknown on every reopen measured",
			},
		},
	},
	{
		Method:    "PullRequests.ReopenPR",
		Product:   GitLab,
		Exercises: "PUT /api/v4/projects/{id}/merge_requests/{iid}",
		Requests:  Requests{Min: 1, Max: 1},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Ref",
				Kind:  DiffersInSpelling,
				Says:  "iid, sigil \"!\"",
			},
			{
				Field: "SourceRepo",
				Kind:  DiffersInShape,
				Says:  "source_project_id beside target_project_id, a fork named by id alone: the addressed project where the two are equal, the zero value otherwise",
			},
			{
				Field: "Labels[].Color",
				Kind:  CannotSupply,
				Says:  "no colour on the answer: its labels arrive as name strings",
			},
			{
				Field: "Labels[].Description",
				Kind:  CannotSupply,
				Says:  "no description on the answer: its labels arrive as name strings",
			},
			{
				Field: "Action.Mergeable",
				Kind:  CannotSupply,
				Says:  "merge_status unchecked and detailed_merge_status unchecked on the answer, unknown until the asynchronous check settles",
			},
			{
				Field: "Action.Checks",
				Kind:  DiffersInShape,
				Says:  "head_pipeline.status scalar on the answer, no fold: null where the head ran no pipeline, so CheckUnknown, and the head pipeline's own status, running here, where one ran",
			},
			{
				Field: "Action.ChecksPassing .. ChecksTotal",
				Kind:  CannotSupply,
				Says:  "zero: the answer's head_pipeline is one scalar status, null or set, with nothing under it to count",
			},
			{
				Field: "Action.QueueState",
				Kind:  CannotSupply,
				Says:  "QueueNone always: no merge-train key among the 61 keys the answer carries",
			},
			{
				Field: "Action.QueuePosition",
				Kind:  CannotSupply,
				Says:  "QueuePositionUnknown: no merge-train key on the answer",
			},
			{
				Field: "Action.MergeBlocked",
				Kind:  CannotSupply,
				Says:  "unknown on a mutation's own answer, whose detailed_merge_status is the asynchronous check's state as the reopen lands, unchecked on every reopen measured, the status a reopen resets to",
			},
			{
				Field: "State",
				Kind:  DiffersInSpelling,
				Says:  "state string, opened, closed_at cleared",
			},
		},
	},
	{
		Method:    "PullRequests.ReopenPR",
		Product:   Gitea,
		Exercises: "PATCH /api/v1/repos/{owner}/{repo}/pulls/{number}",
		Requests:  Requests{Min: 1, Max: 1},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Action.Mergeable",
				Kind:  CannotSupply,
				Says:  "unknown on a mutation's own answer, whose mergeable bool is false while the instance's asynchronous check holds the pull request queued as the reopen lands, true on every reopen measured",
			},
			{
				Field: "Action.AutoMergeArmed",
				Kind:  CannotSupply,
				Says:  "no auto_merge key: unknown",
			},
			{
				Field: "Action.QueueState",
				Kind:  CannotSupply,
				Says:  "QueueNone always",
			},
			{
				Field: "Action.QueuePosition",
				Kind:  CannotSupply,
				Says:  "QueuePositionUnknown: no queue",
			},
			{
				Field: "Action.MergeBlocked",
				Kind:  CannotSupply,
				Says:  "mergeable and draft, no reason",
			},
		},
	},
	{
		Method:    "PullRequests.ReopenPR",
		Product:   Forgejo,
		Exercises: "PATCH /api/v1/repos/{owner}/{repo}/pulls/{number}",
		Requests:  Requests{Min: 1, Max: 1},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "AuthorKind",
				Kind:  CannotSupply,
				Says:  "AccountKindUnknown: the user object carries no type key, measured on codeberg.org 2026-10-11",
			},
			{
				Field: "Action.Mergeable",
				Kind:  CannotSupply,
				Says:  "unknown on a mutation's own answer, whose mergeable bool is false while the instance's asynchronous check holds the pull request queued as the reopen lands, false on one reopen measured and true on the others",
			},
			{
				Field: "Action.AutoMergeArmed",
				Kind:  CannotSupply,
				Says:  "no auto_merge key: unknown",
			},
			{
				Field: "Action.QueueState",
				Kind:  CannotSupply,
				Says:  "QueueNone always",
			},
			{
				Field: "Action.QueuePosition",
				Kind:  CannotSupply,
				Says:  "QueuePositionUnknown: no queue",
			},
			{
				Field: "Action.MergeBlocked",
				Kind:  CannotSupply,
				Says:  "mergeable and draft, no reason",
			},
		},
	},
	{
		Method:    "PullRequests.RerunFailedChecks",
		Product:   GitHub,
		Exercises: "GET /repos/{owner}/{repo}/actions/runs to resolve the head commit's run, then POST /repos/{owner}/{repo}/actions/runs/{run}/rerun-failed-jobs, plus one read against the id-addressed location to resolve the successor where either of them answered from another address",
		Requests:  Requests{Min: 2, Max: 3},
		Needs:     "rerun_checks",
		Support:   Supported,
		Sends: []Sent{
			{
				Field: "head_sha",
				Where: InQuery,
				Holds: HoldsHeadSHA,
				Says:  "the head the caller pinned, as the filter this product's runs route declares, so the retry addresses the workflow run of the commit the caller's row was rendered from rather than whichever the instance returns first",
			},
		},
	},
	{
		Method:    "PullRequests.RerunFailedChecks",
		Product:   GitLab,
		Exercises: "GET /api/v4/projects/{id}/pipelines, filtered by the head SHA, to resolve the head commit's pipeline, then POST /api/v4/projects/{id}/pipelines/{run}/retry",
		Requests:  Requests{Min: 2, Max: 2},
		Needs:     "rerun_checks",
		Support:   Supported,
		Sends: []Sent{
			{
				Field: "sha",
				Where: InQuery,
				Holds: HoldsHeadSHA,
				Says:  "the head the caller pinned, as the filter this product's pipelines route declares, so the retry addresses the pipeline of the commit the caller's row was rendered from rather than whichever the instance returns first",
			},
		},
	},
	{
		Method:    "PullRequests.RerunFailedChecks",
		Product:   Gitea,
		Exercises: "GET /api/v1/repos/{owner}/{repo}/actions/runs, filtered by the head SHA, to resolve the head commit's run, then POST /api/v1/repos/{owner}/{repo}/actions/runs/{run}/rerun-failed-jobs, both of which this product's swagger document names",
		Requests:  Requests{Min: 2, Max: 2},
		Needs:     "rerun_checks",
		Support:   Supported,
		Sends: []Sent{
			{
				Field: "head_sha",
				Where: InQuery,
				Holds: HoldsHeadSHA,
				Says:  "the head the caller pinned, as the filter this product's runs route declares, so the retry addresses the run of the commit the caller's row was rendered from rather than whichever the instance returns first",
			},
		},
	},
	{
		Method:    "PullRequests.RerunFailedChecks",
		Product:   Forgejo,
		Exercises: "",
		Requests:  Requests{},
		Needs:     "rerun_checks",
		Support:   Unsupported,
		Departures: []Departure{
			{
				Field: "error, verb absent",
				Kind:  CannotSupply,
				Says:  "no re-run verb in this product's API, which its swagger document settles",
			},
		},
	},
	{
		Method:    "Merges.MergePR",
		Product:   GitHub,
		Exercises: "PUT /repos/{owner}/{repo}/pulls/{number}/merge-async, this product's asynchronous merge, whose accept carries a handle and whose outcome Merges.MergeStatus reads from the pull request, then one read against the id-addressed location to resolve the successor where it answered from another address; a merge asked to wait first reads GET /repos/{owner}/{repo}/pulls/{number}, and a pull request that cannot merge now is armed by the EnableAutoMerge document in place of the merge",
		Requests:  Requests{Min: 1, Max: 3},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "QueueState",
				Kind:  CannotSupply,
				Says:  "no queue key on the 202; MergeStatus reads it",
			},
		},
		Sends: []Sent{
			{
				Field: "merge_method",
				Where: InBody,
				Holds: HoldsLiteral,
				Value: "\"merge\"",
				Says:  "the strategy this product's own route takes, in its own spelling, and merge where the caller named none",
			},
			{
				Field: "sha",
				Where: InBody,
				Holds: HoldsHeadSHA,
				Says:  "the head the caller pinned, under the key this product's asynchronous merge route reads, so a merge cannot land on a head that moved under the row the caller was looking at: a pin naming another commit is refused before anything merges, and a push between the request and the merge cancels it, as the route documents",
			},
			{
				Field: "merge_action",
				Where: InBody,
				Holds: HoldsNothing,
				Says:  "absent: a merge this product accepts completes in the background on its own, so an action that deferred it would answer a merge nobody asked to wait for",
			},
			{
				Field: "delete_branch_on_merge",
				Where: InBody,
				Holds: HoldsNothing,
				Says:  "absent: neither this product's merge route nor its auto-merge arm takes a branch deletion, so the repository's own delete_branch_on_merge setting decides whether the head branch goes, whatever the caller asked",
			},
		},
	},
	{
		Method:    "Merges.MergePR",
		Product:   GitLab,
		Exercises: "PUT /api/v4/projects/{id}/merge_requests/{iid}/merge, with the head commit required",
		Requests:  Requests{Min: 1, Max: 1},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "State",
				Kind:  CannotSupply,
				Says:  "Merged, Enqueued or Refused",
			},
			{
				Field: "Code",
				Kind:  CannotSupply,
				Says:  "empty: no such state",
			},
		},
		Sends: []Sent{
			{
				Field: "sha",
				Where: InBody,
				Holds: HoldsHeadSHA,
				Says:  "the head the caller pinned, which a group or instance setting on this product can make compulsory and which is what makes the merge refuse a branch that moved",
			},
			{
				Field: "squash",
				Where: InBody,
				Holds: HoldsLiteral,
				Value: "false",
				Says:  "false unless the caller asked to squash: this product exposes no strategy selector, so a squash flag is what a merge intent reaches here",
			},
			{
				Field: "auto_merge",
				Where: InBody,
				Holds: HoldsNothing,
				Says:  "absent unless the caller asked for it: sent by default it arms a merge that completes later and unattended on a pull request the user asked to merge now",
			},
		},
	},
	{
		Method:    "Merges.MergePR",
		Product:   Gitea,
		Exercises: "POST /api/v1/repos/{owner}/{repo}/pulls/{number}/merge, and for a merge asked to wait whose form the route answers 405, the same form again carrying merge_when_checks_succeed",
		Requests:  Requests{Min: 1, Max: 2},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "State",
				Kind:  CannotSupply,
				Says:  "Merged, Enqueued or Refused: a merge asked to wait sends the merge form first and, on its 405, the form with merge_when_checks_succeed, whose 201 is the scheduled merge",
			},
			{
				Field: "Code",
				Kind:  CannotSupply,
				Says:  "empty: no such state",
			},
			{
				Field: "QueueState",
				Kind:  CannotSupply,
				Says:  "QueueNone always",
			},
		},
		Sends: []Sent{
			{
				Field: "do",
				Where: InBody,
				Holds: HoldsLiteral,
				Value: "\"merge\"",
				Says:  "the strategy this product takes, in its own spelling, and merge where the caller named none",
			},
			{
				Field: "head_commit_id",
				Where: InBody,
				Holds: HoldsHeadSHA,
				Says:  "the head the caller pinned",
			},
			{
				Field: "merge_when_checks_succeed",
				Where: InBody,
				Holds: HoldsNothing,
				Says:  "absent unless the caller asked for it and the flagless form answered 405: sent, the instance schedules the merge whatever the checks, so sent by default it would answer a merge nobody asked to wait for",
			},
		},
	},
	{
		Method:    "Merges.MergePR",
		Product:   Forgejo,
		Exercises: "POST /api/v1/repos/{owner}/{repo}/pulls/{number}/merge, and for a merge asked to wait whose form the route answers 405, the same form again carrying merge_when_checks_succeed",
		Requests:  Requests{Min: 1, Max: 2},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "State",
				Kind:  CannotSupply,
				Says:  "Merged, Enqueued or Refused: a merge asked to wait sends the merge form first and, on its 405, the form with merge_when_checks_succeed, whose 201 is the scheduled merge",
			},
			{
				Field: "Code",
				Kind:  CannotSupply,
				Says:  "empty: no such state",
			},
			{
				Field: "QueueState",
				Kind:  CannotSupply,
				Says:  "QueueNone always",
			},
		},
		Sends: []Sent{
			{
				Field: "Do",
				Where: InBody,
				Holds: HoldsLiteral,
				Value: "\"merge\"",
				Says:  "the strategy this product takes, in its own spelling, and merge where the caller named none",
			},
			{
				Field: "head_commit_id",
				Where: InBody,
				Holds: HoldsHeadSHA,
				Says:  "the head the caller pinned",
			},
			{
				Field: "merge_when_checks_succeed",
				Where: InBody,
				Holds: HoldsNothing,
				Says:  "absent unless the caller asked for it and the flagless form answered 405: sent, the instance schedules the merge whatever the checks, so sent by default it would answer a merge nobody asked to wait for",
			},
		},
	},
	{
		Method:    "Merges.MergeStatus",
		Product:   GitHub,
		Exercises: "the PRRead document, or GET /repos/{owner}/{repo}/pulls/{number} where this connection has already discovered the documents refused, then one read against the id-addressed location to resolve the successor where that record answered from another address",
		Requests:  Requests{Min: 0, Max: 3},
		Support:   Supported,
	},
	{
		Method:    "Merges.MergeStatus",
		Product:   GitLab,
		Exercises: "the PRRead document, or GET /api/v4/projects/{id}/merge_requests/{iid} where this connection's setup found the documents refused",
		Requests:  Requests{Min: 1, Max: 1},
		Needs:     "read_merge_state",
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Merged",
				Kind:  DiffersInShape,
				Says:  "state and mergedAt, no merged field",
			},
			{
				Field: "Queue",
				Kind:  CannotSupply,
				Says:  "QueueNone always: every field that would name a train carries a deprecation reason, so no document can select one",
			},
			{
				Field: "Successor",
				Kind:  CannotSupply,
				Says:  "nil: GraphQL sends no successor",
			},
		},
	},
	{
		Method:    "Merges.MergeStatus",
		Product:   Gitea,
		Exercises: "GET /api/v1/repos/{owner}/{repo}/pulls/{number}, and where the repository moved, the one 301 its old path answers, whose Location names the successor, every later request of the call addressing that successor",
		Requests:  Requests{Min: 1, Max: 2},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Queue",
				Kind:  CannotSupply,
				Says:  "QueueNone always",
			},
		},
	},
	{
		Method:    "Merges.MergeStatus",
		Product:   Forgejo,
		Exercises: "GET /api/v1/repos/{owner}/{repo}/pulls/{number}, and where the repository moved, the one 301 its old path answers, whose Location names the successor, every later request of the call addressing that successor",
		Requests:  Requests{Min: 1, Max: 2},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Queue",
				Kind:  CannotSupply,
				Says:  "QueueNone always",
			},
		},
	},
	{
		Method:    "Checks.CommitStatus",
		Product:   GitHub,
		Exercises: "the CommitRollup document, the one source on this product that tells a commit with no CI from a commit whose checks all passed, or GET /repos/{owner}/{repo}/commits/{ref}/status beside GET /repos/{owner}/{repo}/commits/{ref}/check-runs where this connection has already discovered the documents refused, plus one read against the id-addressed location to resolve the successor where those reads answered from another address, which both of them meet and one read answers",
		Requests:  Requests{Min: 0, Max: 5},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "State, under a fine-grained token",
				Kind:  CannotSupply,
				Says:  "CheckUnknown with no count, beside forbidden on Partial: a fine-grained token holds no Checks permission, and the document refuses the rollup's contexts nodes on a private repository with FORBIDDEN, measured on the PRMine search, which selects the same rollup",
			},
		},
	},
	{
		Method:    "Checks.CommitStatus",
		Product:   GitLab,
		Exercises: "GET /api/v4/projects/{id}/repository/commits/{sha}/statuses",
		Requests:  Requests{Min: 1, Max: 3},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Successor",
				Kind:  CannotSupply,
				Says:  "nil: no successor key",
			},
		},
	},
	{
		Method:    "Checks.CommitStatus",
		Product:   Gitea,
		Exercises: "GET /api/v1/repos/{owner}/{repo}/commits/{ref}/status, and where the repository moved, the one 301 its old path answers, whose Location names the successor, every later request of the call addressing that successor",
		Requests:  Requests{Min: 1, Max: 4},
		Support:   Supported,
	},
	{
		Method:    "Checks.CommitStatus",
		Product:   Forgejo,
		Exercises: "GET /api/v1/repos/{owner}/{repo}/commits/{ref}/status, and where the repository moved, the one 301 its old path answers, whose Location names the successor, every later request of the call addressing that successor",
		Requests:  Requests{Min: 1, Max: 4},
		Support:   Supported,
	},
	{
		Method:    "Checks.ListRuns",
		Product:   GitHub,
		Exercises: "GET /repos/{owner}/{repo}/actions/runs, the route the re-run resolves its run on, with the page bound as its page size and no head filter, then one read against the id-addressed location to resolve the successor where it answered from another address; a run whose conclusion is action_required, a fork's run waiting for a maintainer's approval with no job run, reads pending, the verdict the Gitea family gives the same wait; under a since bound, a page that reaches the 1,000-run window the route serves a created-filtered listing through, or ends short of its total with no next relation, holds back the runs of the oldest second it served, and the next page asks created=<bound>..<that second> from page 1, the inclusive range the route's created filter takes from the search syntax (https://docs.github.com/en/search-github/getting-started-with-searching-on-github/understanding-the-search-syntax#query-for-dates); where an earlier page of the range served that second, the range already ends at that second, or a page's runs of its oldest second are more than a continuation names, the next page asks created=<that second>..<that second> instead (measured: it answers that second's runs alone), drops by id the runs of it the walk answered, and the walk then goes on with created=<bound>..<the second before>, so each range a walk crosses into reads the runs of one second twice and pays once more for the pages they fill",
		Requests:  Requests{Min: 0, Max: 3, Paged: true},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Items[].Ref.Number",
				Kind:  DiffersInShape,
				Says:  "run_number, numbered per workflow rather than per repository, so two runs of one repository can share it",
			},
			{
				Field: "Next, under a since bound",
				Kind:  DiffersInShape,
				Says:  "the route serves a filtered listing through the first whole page that reaches its 1,000th run (https://docs.github.com/en/rest/actions/workflow-runs#list-workflow-runs-for-a-repository) and answers the page after it with no rows and total_count 0 (measured: 1,000 of a repository's 1,045 runs in 72 hours), so a walk goes on below the oldest second that page served; where more runs than that window share one second, the walk goes on below that second too, and where runs of such seconds remain that neither the walk nor that second's own range served, its last page, which carries no continuation, carries result_window counting them; the call fails as a validation failure of kind upstream where no narrower range reaches the rest: a range that served no row while its total states more, or a second's own range that ends short of its total below the window",
			},
		},
		Sends: []Sent{
			{
				Field: "head_sha",
				Where: InQuery,
				Holds: HoldsNothing,
				Says:  "absent: the listing answers every run of the repository, and the head filter the re-run sends on the same route would narrow it to one commit's runs",
			},
			{
				Field: "created",
				Where: InQuery,
				Holds: HoldsSinceBound,
				Says:  "under a since bound, >= the bound at whole seconds, the precision the route stamps created_at at, and the rows stamped before the bound's second are dropped; past the window, the bound to the oldest second the window served, that second alone, or the bound to the second before it, both ends included; absent without one",
			},
		},
	},
	{
		Method:    "Checks.ListRuns",
		Product:   GitLab,
		Exercises: "GET /api/v4/projects/{id}/pipelines, the route the re-run resolves its pipeline on, with the page bound as its page size and no head filter",
		Requests:  Requests{Min: 1, Max: 1, Paged: true},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Items[].Branch",
				Kind:  DiffersInSpelling,
				Says:  "ref string: a branch on 2 of 5 and a refs/ path on 3, which merge-request and workload pipelines run for",
			},
			{
				Field: "Items[].StartedAt",
				Kind:  CannotSupply,
				Says:  "zero: the listing row carries no started_at, which only the single-pipeline route answers, and the listing adds no request for it",
			},
			{
				Field: "Items[].WorkflowPath",
				Kind:  CannotSupply,
				Says:  "empty: a pipeline row names no configuration file, and every pipeline of a project runs its one configuration",
			},
			{
				Field: "Successor",
				Kind:  CannotSupply,
				Says:  "nil: no successor key",
			},
		},
		Sends: []Sent{
			{
				Field: "sha",
				Where: InQuery,
				Holds: HoldsNothing,
				Says:  "absent: the listing answers every run of the repository, and the head filter the re-run sends on the same route would narrow it to one commit's runs",
			},
			{
				Field: "created_after",
				Where: InQuery,
				Holds: HoldsSinceBound,
				Says:  "under a since bound, one second before the bound at whole seconds, because the filter is strictly after, and the rows created before the bound's second are dropped; absent without one",
			},
		},
	},
	{
		Method:    "Checks.ListRuns",
		Product:   Gitea,
		Exercises: "GET /api/v1/repos/{owner}/{repo}/actions/runs, the route the re-run resolves its run on, with page from 1, the smaller of the page bound and the instance's stated maximum as its page size and no head filter, continued from the body's total_count, and where the repository moved, the one 301 its old path answers, whose Location names the successor, every later request of the call addressing that successor; under a since bound no time filter, the route declaring none, so each page's rows created before the bound's second are dropped; the route answers runs by id descending, or from v28 by repository index descending, co-monotonic with id (models/actions/run_list.go, FindRunOptions.ToOrders), and stamps a run's creation once, at insert (models/actions/run.go, its Created field), so a page whose every run was created more than a minute before the bound, the step a host clock can take back between two inserts, ends the walk with no continuation",
		Requests:  Requests{Min: 1, Max: 2, Paged: true},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Items[].Name",
				Kind:  DiffersInSpelling,
				Says:  "path string, the workflow file and its ref, on 6 of 6; no name key among the 26 the route answers",
			},
			{
				Field: "Items[].WorkflowPath",
				Kind:  DiffersInShape,
				Says:  "the workflow file's name, path string before its ref with no directory, the name Forgejo answers",
			},
			{
				Field: "Items[].CreatedAt, before v28",
				Kind:  CannotSupply,
				Says:  "zero: the route's run object carries no created_at before v28, so a page read under a since bound is refused as run_undated rather than answered as holding no run",
			},
			{
				Field: "Items[].UpdatedAt, before v28",
				Kind:  CannotSupply,
				Says:  "zero: the route's run object carries no updated_at before v28",
			},
		},
		Sends: []Sent{
			{
				Field: "head_sha",
				Where: InQuery,
				Holds: HoldsNothing,
				Says:  "absent: the listing answers every run of the repository, and the head filter the re-run sends on the same route would narrow it to one commit's runs",
			},
			{
				Field: "page",
				Where: InQuery,
				Holds: HoldsLiteral,
				Value: "1",
				Says:  "the first page of a walk, sent with every limit: a Forgejo instance answers a request with no page with every run of the repository",
			},
			{
				Field: "limit",
				Where: InQuery,
				Holds: HoldsLiteral,
				Value: "50",
				Says:  "the smaller of the page bound and the instance's stated maximum, here the maximum both public instances state, the same on every page of one walk, which is what lets the stated total decide the continuation where an instance serves fewer rows than asked",
			},
		},
	},
	{
		Method:    "Checks.ListRuns",
		Product:   Forgejo,
		Exercises: "GET /api/v1/repos/{owner}/{repo}/actions/runs, with page from 1, the smaller of the page bound and the instance's stated maximum as its page size and no head filter, continued from the body's total_count, since the route sends no paging header and an instance whose maximum was lowered after the connection read it still serves a page of its own maximum, and where the repository moved, the one 301 its old path answers, whose Location names the successor, every later request of the call addressing that successor; under a since bound no time filter, the route declaring none, so each page's rows created before the bound's second are dropped; the route answers runs by id descending (models/actions/run_list.go, FindRunOptions.ToOrders) and stamps a run's creation once, at insert (models/actions/run.go, its Created field), so a page whose every run was created more than a minute before the bound, the step a host clock can take back between two inserts, ends the walk with no continuation",
		Requests:  Requests{Min: 1, Max: 2, Paged: true},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Items[].Name",
				Kind:  DiffersInSpelling,
				Says:  "workflow_id string, the workflow file's name, on 16 of 16 rows kept; no name key among the 23 the route answers",
			},
			{
				Field: "Items[].WorkflowPath",
				Kind:  DiffersInShape,
				Says:  "workflow_id string, the workflow file's name with no directory",
			},
			{
				Field: "Items[].Branch",
				Kind:  DiffersInSpelling,
				Says:  "prettyref string, pull_request run \"#820\"; pull_request run \"#841\"; pull_request run \"#842\"; push run \"main\": the branch on a push run and a pull request's number on a pull-request run, no branch key",
			},
		},
		Sends: []Sent{
			{
				Field: "head_sha",
				Where: InQuery,
				Holds: HoldsNothing,
				Says:  "absent: the listing answers every run of the repository, and the head filter the re-run sends on the same route would narrow it to one commit's runs",
			},
			{
				Field: "page",
				Where: InQuery,
				Holds: HoldsLiteral,
				Value: "1",
				Says:  "the first page of a walk, sent with every limit: a Forgejo instance answers a request with no page with every run of the repository",
			},
			{
				Field: "limit",
				Where: InQuery,
				Holds: HoldsLiteral,
				Value: "50",
				Says:  "the smaller of the page bound and the instance's stated maximum, here the maximum both public instances state, the same on every page of one walk, which is what lets the stated total decide the continuation where an instance serves fewer rows than asked",
			},
		},
	},
	{
		Method:    "Issues.ListIssues",
		Product:   GitHub,
		Exercises: "GET /repos/{owner}/{repo}/issues, whose population is issues and pull requests both, so a row carrying a pull-request key is dropped, then one read against the id-addressed location to resolve the successor where it answered from another address",
		Requests:  Requests{Min: 0, Max: 3, Paged: true},
		Support:   Supported,
	},
	{
		Method:    "Issues.ListIssues",
		Product:   GitLab,
		Exercises: "GET /api/v4/projects/{id}/issues",
		Requests:  Requests{Min: 1, Max: 1, Paged: true},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Items[].Ref",
				Kind:  DiffersInSpelling,
				Says:  "iid int",
			},
			{
				Field: "Items[].AuthorKind",
				Kind:  CannotSupply,
				Says:  "AccountKindUnknown on every row: the issue's author carries no bot key, measured on gitlab.com 2026-10-11, while a merge request's author answers it",
			},
			{
				Field: "Items[].Labels[].Color",
				Kind:  DiffersInSpelling,
				Says:  "color string, leading #",
			},
			{
				Field: "Items[].State",
				Kind:  DiffersInSpelling,
				Says:  "state string, opened",
			},
			{
				Field: "Successor",
				Kind:  CannotSupply,
				Says:  "nil: no successor key",
			},
		},
	},
	{
		Method:    "Issues.ListIssues",
		Product:   Gitea,
		Exercises: "GET /api/v1/repos/{owner}/{repo}/issues, filtered to issues, because this route's population is issues and pull requests both, and where the repository moved, the one 301 its old path answers, whose Location names the successor, every later request of the call addressing that successor",
		Requests:  Requests{Min: 1, Max: 2, Paged: true},
		Support:   Supported,
	},
	{
		Method:    "Issues.ListIssues",
		Product:   Forgejo,
		Exercises: "GET /api/v1/repos/{owner}/{repo}/issues, filtered to issues, because this route's population is issues and pull requests both, and where the repository moved, the one 301 its old path answers, whose Location names the successor, every later request of the call addressing that successor",
		Requests:  Requests{Min: 1, Max: 2, Paged: true},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Items[].AuthorKind",
				Kind:  CannotSupply,
				Says:  "AccountKindUnknown on every row: the user object carries no type key, measured on codeberg.org 2026-10-11",
			},
		},
	},
	{
		Method:    "Issues.ListMyIssues",
		Product:   GitHub,
		Exercises: "the IssueMine search document, whose query names type:issue and the author keyword, or user:{owner} in its place under an owner scope",
		Requests:  Requests{Min: 1, Max: 1, Paged: true},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Next",
				Kind:  DiffersInShape,
				Says:  "a continuation while hasNextPage holds, as on a viewer's first page of 20 rows over issueCount 188; past the search connection's 1,000th result, where it answers hasNextPage false while issueCount states 4617, and a page asked for after it answers no rows and a null end cursor, so a walk ends there with no continuation and carries result_window",
			},
			{
				Field: "error, owner unresolved",
				Kind:  CannotSupply,
				Says:  "empty: an owner the search index holds no account for answers issueCount 0 and no error, so no such owner reads as nothing open",
			},
		},
	},
	{
		Method:    "Issues.ListMyIssues",
		Product:   GitLab,
		Exercises: "GET /api/v4/issues?scope=created_by_me, or GET /api/v4/groups/{owner}/issues under an owner scope, both with the open state and the label detail parameter; an owner named by digits alone, which every namespace route reads as a numeric id, is first resolved by name through the OwnerLookup document to the id the route takes, one request more on every page",
		Requests:  Requests{Min: 1, Max: 2, Paged: true},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Items[].Ref",
				Kind:  DiffersInSpelling,
				Says:  "iid int, on the group route's 2 of 2 rows",
			},
			{
				Field: "Items[].AuthorKind",
				Kind:  CannotSupply,
				Says:  "AccountKindUnknown on every row: the issue's author carries no bot key, measured on gitlab.com 2026-10-11, while a merge request's author answers it",
			},
			{
				Field: "Items[].Labels[].Color",
				Kind:  DiffersInSpelling,
				Says:  "color string, leading #, on 16 of 16, upper-case hex on 1",
			},
			{
				Field: "Items[].State",
				Kind:  DiffersInSpelling,
				Says:  "state string, opened on 2 of 2: the state parameter fixes it",
			},
			{
				Field: "error, owner unresolved",
				Kind:  CannotSupply,
				Says:  "404 Group Not Found on a user's own namespace, an account the users route answers, exactly as on an owner no namespace holds",
			},
			{
				Field: "Items[], from an archived repository",
				Kind:  CannotSupply,
				Says:  "none on either route: non_archived defaults to true",
			},
		},
	},
	{
		Method:    "Issues.ListMyIssues",
		Product:   Gitea,
		Exercises: "GET /api/v1/repos/issues/search filtered to issues and to the rows the authenticated credential created, or by owner={owner} in place of created=true under an owner scope",
		Requests:  Requests{Min: 1, Max: 1, Paged: true},
		Support:   Supported,
	},
	{
		Method:    "Issues.ListMyIssues",
		Product:   Forgejo,
		Exercises: "GET /api/v1/repos/issues/search filtered to issues and to the rows the authenticated credential created, or by owner={owner} in place of created=true under an owner scope",
		Requests:  Requests{Min: 1, Max: 1, Paged: true},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Items[].AuthorKind",
				Kind:  CannotSupply,
				Says:  "AccountKindUnknown on every row: the user object carries no type key, measured on codeberg.org 2026-10-11",
			},
		},
	},
	{
		Method:    "Issues.CreateIssue",
		Product:   GitHub,
		Exercises: "POST /repos/{owner}/{repo}/issues, whose own body carries the labels the caller named, then one read against the id-addressed location to resolve the successor where it answered from another address",
		Requests:  Requests{Min: 1, Max: 2},
		Support:   Supported,
		Sends: []Sent{
			{
				Field: "labels",
				Where: InBody,
				Holds: HoldsLabelNames,
				Says:  "the labels the caller named, as the NAMES this product's issue creation takes, which is the difference from its pull-request creation",
			},
		},
	},
	{
		Method:    "Issues.CreateIssue",
		Product:   GitLab,
		Exercises: "POST /api/v4/projects/{id}/issues",
		Requests:  Requests{Min: 1, Max: 1},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Ref",
				Kind:  DiffersInSpelling,
				Says:  "iid int, the project's own sequence",
			},
			{
				Field: "AuthorKind",
				Kind:  CannotSupply,
				Says:  "AccountKindUnknown: the issue's author carries no bot key, measured on gitlab.com 2026-10-11, while a merge request's author answers it",
			},
			{
				Field: "Labels[].Color",
				Kind:  CannotSupply,
				Says:  "no colour on the answer: its labels arrive as name strings",
			},
			{
				Field: "Labels[].Description",
				Kind:  CannotSupply,
				Says:  "no description on the answer: its labels arrive as name strings",
			},
			{
				Field: "State",
				Kind:  DiffersInSpelling,
				Says:  "state string, opened",
			},
		},
		Sends: []Sent{
			{
				Field: "labels",
				Where: InBody,
				Holds: HoldsLabelNames,
				Says:  "the labels the caller named, as the NAMES this product's creation takes, so nothing has to resolve them first",
			},
		},
	},
	{
		Method:    "Issues.CreateIssue",
		Product:   Gitea,
		Exercises: "GET /api/v1/repos/{owner}/{repo}/labels where the caller names labels, whose ids this product's option takes in place of their names, then POST /api/v1/repos/{owner}/{repo}/issues",
		Requests:  Requests{Min: 1, Max: 2},
		Support:   Supported,
		Sends: []Sent{
			{
				Field: "labels",
				Where: InBody,
				Holds: HoldsLabelIDs,
				Says:  "the labels the caller named, as the numeric ids this product's creation declares in place of their names, which is what the label read this row prices resolves",
			},
		},
	},
	{
		Method:    "Issues.CreateIssue",
		Product:   Forgejo,
		Exercises: "GET /api/v1/repos/{owner}/{repo}/labels where the caller names labels, whose ids this product's option takes in place of their names, then POST /api/v1/repos/{owner}/{repo}/issues",
		Requests:  Requests{Min: 1, Max: 2},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "AuthorKind",
				Kind:  CannotSupply,
				Says:  "AccountKindUnknown: the user object carries no type key, measured on codeberg.org 2026-10-11",
			},
		},
		Sends: []Sent{
			{
				Field: "labels",
				Where: InBody,
				Holds: HoldsLabelIDs,
				Says:  "the labels the caller named, as the numeric ids this product's creation declares in place of their names, which is what the label read this row prices resolves",
			},
		},
	},
	{
		Method:    "Issues.CloseIssue",
		Product:   GitHub,
		Exercises: "PATCH /repos/{owner}/{repo}/issues/{number}, then one read against the id-addressed location to resolve the successor where it answered from another address",
		Requests:  Requests{Min: 1, Max: 2},
		Support:   Supported,
	},
	{
		Method:    "Issues.CloseIssue",
		Product:   GitLab,
		Exercises: "PUT /api/v4/projects/{id}/issues/{iid}",
		Requests:  Requests{Min: 1, Max: 1},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Ref",
				Kind:  DiffersInSpelling,
				Says:  "iid int, the project's own sequence",
			},
			{
				Field: "AuthorKind",
				Kind:  CannotSupply,
				Says:  "AccountKindUnknown: the issue's author carries no bot key, measured on gitlab.com 2026-10-11, while a merge request's author answers it",
			},
			{
				Field: "Labels[].Color",
				Kind:  CannotSupply,
				Says:  "no colour on the answer: its labels arrive as name strings",
			},
			{
				Field: "Labels[].Description",
				Kind:  CannotSupply,
				Says:  "no description on the answer: its labels arrive as name strings",
			},
		},
	},
	{
		Method:    "Issues.CloseIssue",
		Product:   Gitea,
		Exercises: "PATCH /api/v1/repos/{owner}/{repo}/issues/{number}",
		Requests:  Requests{Min: 1, Max: 1},
		Support:   Supported,
	},
	{
		Method:    "Issues.CloseIssue",
		Product:   Forgejo,
		Exercises: "PATCH /api/v1/repos/{owner}/{repo}/issues/{number}",
		Requests:  Requests{Min: 1, Max: 1},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "AuthorKind",
				Kind:  CannotSupply,
				Says:  "AccountKindUnknown: the user object carries no type key, measured on codeberg.org 2026-10-11",
			},
		},
	},
	{
		Method:    "Capabilities.ConnectionCaps",
		Product:   GitHub,
		Exercises: "the X-GitHub-Request-Id header that names the product and the x-github-enterprise-version header that names an appliance's version, both riding traffic the connection already sent, then GET /meta where no header answered, beside GET /versions, which settles whether this instance serves the version this family pins, all cached with the connection",
		Requests:  Requests{Max: 2},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Caps[rerun_checks]",
				Kind:  CannotSupply,
				Says:  "no wire field: per repository",
			},
		},
	},
	{
		Method:    "Capabilities.ConnectionCaps",
		Product:   GitLab,
		Exercises: "the X-Gitlab-Meta header riding traffic the connection already sent, then GET /api/v4/metadata, beside the one bounded schema question that settles whether this instance accepts the documents this family ships, both cached with the connection",
		Requests:  Requests{Max: 2},
		Support:   Supported,
	},
	{
		Method:    "Capabilities.ConnectionCaps",
		Product:   Gitea,
		Exercises: "neither X-Gitea-Version nor X-Forgejo-Version, which neither product sends, so GET /api/v1/version, whose body separates the two, then GET /swagger.v1.json where no cheaper source answered, both cached with the connection, and GET /api/v1/settings/api, whose max_response_items every page-numbered list asks for at most, read once beside the version",
		Requests:  Requests{Max: 3},
		Support:   Supported,
	},
	{
		Method:    "Capabilities.ConnectionCaps",
		Product:   Forgejo,
		Exercises: "neither X-Gitea-Version nor X-Forgejo-Version, which neither product sends, so GET /api/v1/version, whose body separates the two, then GET /swagger.v1.json where no cheaper source answered, both cached with the connection, and GET /api/v1/settings/api, whose max_response_items every page-numbered list asks for at most, read once beside the version",
		Requests:  Requests{Max: 3},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Caps[rerun_checks]",
				Kind:  CannotSupply,
				Says:  "swagger has no rerun verb: no",
			},
		},
	},
	{
		Method:    "Capabilities.GrantCaps",
		Product:   GitHub,
		Exercises: "GET /user, whose X-OAuth-Scopes header carries the scope pair",
		Requests:  Requests{Max: 1},
		Support:   Supported,
	},
	{
		Method:    "Capabilities.GrantCaps",
		Product:   GitLab,
		Exercises: "the PRRead document's user permissions",
		Requests:  Requests{Max: 1},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Caps[read_merge_state], after a scope refusal",
				Kind:  CannotSupply,
				Says:  "no token-permission source a request reads: the token route refused the fine-grained token for lacking [Personal Access Token: Read] at the user boundary, so once a refusal names what the credential lacks, the merge-state grant answers unknown with that refusal as its evidence",
			},
		},
	},
	{
		Method:    "Capabilities.GrantCaps",
		Product:   Gitea,
		Exercises: "GET /api/v1/repos/{owner}/{repo}/pulls/{number}, whose body carries the mergeable flag",
		Requests:  Requests{Max: 1},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Ev",
				Kind:  CannotSupply,
				Says:  "no evidence object; body mergeable",
			},
		},
	},
	{
		Method:    "Capabilities.GrantCaps",
		Product:   Forgejo,
		Exercises: "GET /api/v1/repos/{owner}/{repo}/pulls/{number}, whose body carries the mergeable flag",
		Requests:  Requests{Max: 1},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Ev",
				Kind:  CannotSupply,
				Says:  "no evidence object; body mergeable",
			},
		},
	},
	{
		Method:    "Capabilities.RepoAffordances",
		Product:   GitHub,
		Exercises: "GET /repos/{owner}/{repo}, then one read against the id-addressed location to resolve the successor where it answered from another address",
		Requests:  Requests{Min: 0, Max: 3},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "MergeTrain",
				Kind:  CannotSupply,
				Says:  "not on the record: a rulesets read",
			},
		},
	},
	{
		Method:    "Capabilities.RepoAffordances",
		Product:   GitLab,
		Exercises: "GET /api/v4/projects/{id}",
		Requests:  Requests{Min: 1, Max: 1},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "HasIssues",
				Kind:  DiffersInShape,
				Says:  "varies by permission",
			},
			{
				Field: "CanPush",
				Kind:  DiffersInShape,
				Says:  "from a role integer",
			},
			{
				Field: "Ev",
				Kind:  CannotSupply,
				Says:  "no source on the row for its three keys, anonymous",
			},
			{
				Field:    "error, scope insufficient",
				Kind:     NotMeasured,
				Says:     "no refusal on a public project outside the token's group, which a fine-grained token reads without a permission and which answered 200 with its permission object; a private project outside it is unmeasured, needs a private project outside the token's group that the account can read",
				Evidence: "PARTLY-CREDENTIAL",
			},
		},
	},
	{
		Method:    "Capabilities.RepoAffordances",
		Product:   Gitea,
		Exercises: "GET /api/v1/repos/{owner}/{repo}, and where the repository moved, the one 301 its first read answers, whose Location names the successor the stale refusal carries",
		Requests:  Requests{Min: 1, Max: 2},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "MergeTrain",
				Kind:  CannotSupply,
				Says:  "SupportNo: no queue",
			},
		},
	},
	{
		Method:    "Capabilities.RepoAffordances",
		Product:   Forgejo,
		Exercises: "GET /api/v1/repos/{owner}/{repo}, and where the repository moved, the one 301 its first read answers, whose Location names the successor the stale refusal carries",
		Requests:  Requests{Min: 1, Max: 2},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "MergeTrain",
				Kind:  CannotSupply,
				Says:  "SupportNo: no queue",
			},
		},
	},
	{
		Method:    "Governor.BudgetState",
		Product:   GitHub,
		Exercises: "the signal riding every response the other operations already received",
		Requests:  Requests{},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Remaining",
				Kind:  DiffersInShape,
				Says:  "the pool the last response drew on, which its X-RateLimit-Resource header names: core after a REST call, graphql after a document",
			},
			{
				Field: "RotationCursor",
				Kind:  CannotSupply,
				Says:  "empty: every response carries its budget",
			},
		},
	},
	{
		Method:    "Governor.BudgetState",
		Product:   GitLab,
		Exercises: "the signal riding every response the other operations already received",
		Requests:  Requests{},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "LastCost",
				Kind:  DiffersInShape,
				Says:  "queryComplexity.score; REST 1 per request",
			},
			{
				Field: "RotationCursor",
				Kind:  CannotSupply,
				Says:  "empty: nothing rotates",
			},
		},
	},
	{
		Method:    "Governor.BudgetState",
		Product:   Gitea,
		Exercises: "nothing: this product sends no signal, so the neutral value is the answer",
		Requests:  Requests{},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Remaining",
				Kind:  CannotSupply,
				Says:  "BudgetRemainingUnknown: no signal",
			},
			{
				Field: "Reset",
				Kind:  CannotSupply,
				Says:  "zero time: no signal",
			},
			{
				Field: "LastCost",
				Kind:  CannotSupply,
				Says:  "own per-call price",
			},
		},
	},
	{
		Method:    "Governor.BudgetState",
		Product:   Forgejo,
		Exercises: "the signal riding every response the other operations already received",
		Requests:  Requests{},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "LastCost",
				Kind:  CannotSupply,
				Says:  "own per-call price",
			},
		},
	},
	{
		Method:    "Releases.ListReleases",
		Product:   GitHub,
		Exercises: "GET /repos/{owner}/{repo}/releases, then one read against the id-addressed location to resolve the successor where it answered from another address",
		Requests:  Requests{Min: 0, Max: 3, Paged: true},
		Support:   Supported,
	},
	{
		Method:    "Releases.ListReleases",
		Product:   GitLab,
		Exercises: "GET /api/v4/projects/{id}/releases",
		Requests:  Requests{Min: 1, Max: 1, Paged: true},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Items[].Body",
				Kind:  DiffersInSpelling,
				Says:  "description, no body key",
			},
			{
				Field: "Items[].Draft",
				Kind:  CannotSupply,
				Says:  "no draft key on the row",
			},
			{
				Field: "Items[].Prerelease",
				Kind:  CannotSupply,
				Says:  "no prerelease key; upcoming_release false here",
			},
			{
				Field: "Successor",
				Kind:  CannotSupply,
				Says:  "nil: no successor key",
			},
		},
	},
	{
		Method:    "Releases.ListReleases",
		Product:   Gitea,
		Exercises: "GET /api/v1/repos/{owner}/{repo}/releases, and where the repository moved, the one 301 its old path answers, whose Location names the successor, every later request of the call addressing that successor",
		Requests:  Requests{Min: 1, Max: 2, Paged: true},
		Support:   Supported,
	},
	{
		Method:    "Releases.ListReleases",
		Product:   Forgejo,
		Exercises: "GET /api/v1/repos/{owner}/{repo}/releases, and where the repository moved, the one 301 its old path answers, whose Location names the successor, every later request of the call addressing that successor",
		Requests:  Requests{Min: 1, Max: 2, Paged: true},
		Support:   Supported,
	},
	{
		Method:    "Releases.CreateRelease",
		Product:   GitHub,
		Exercises: "POST /repos/{owner}/{repo}/releases, then one read against the id-addressed location to resolve the successor where it answered from another address",
		Requests:  Requests{Min: 1, Max: 2},
		Support:   Supported,
	},
	{
		Method:    "Releases.CreateRelease",
		Product:   GitLab,
		Exercises: "POST /api/v4/projects/{id}/releases",
		Requests:  Requests{Min: 1, Max: 1},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Body",
				Kind:  DiffersInSpelling,
				Says:  "description, no body key",
			},
			{
				Field: "Draft",
				Kind:  CannotSupply,
				Says:  "no draft key on the answer",
			},
			{
				Field: "Prerelease",
				Kind:  CannotSupply,
				Says:  "no prerelease key; upcoming_release false here, though one was asked for",
			},
		},
	},
	{
		Method:    "Releases.CreateRelease",
		Product:   Gitea,
		Exercises: "POST /api/v1/repos/{owner}/{repo}/releases",
		Requests:  Requests{Min: 1, Max: 1},
		Support:   Supported,
	},
	{
		Method:    "Releases.CreateRelease",
		Product:   Forgejo,
		Exercises: "POST /api/v1/repos/{owner}/{repo}/releases",
		Requests:  Requests{Min: 1, Max: 1},
		Support:   Supported,
	},
	{
		Method:    "Labels.ListLabels",
		Product:   GitHub,
		Exercises: "GET /repos/{owner}/{repo}/labels, then one read against the id-addressed location to resolve the successor where it answered from another address",
		Requests:  Requests{Min: 0, Max: 3, Paged: true},
		Support:   Supported,
	},
	{
		Method:    "Labels.ListLabels",
		Product:   GitLab,
		Exercises: "GET /api/v4/projects/{id}/labels",
		Requests:  Requests{Min: 1, Max: 1, Paged: true},
		Support:   Supported,
		Departures: []Departure{
			{
				Field: "Items[].Color",
				Kind:  DiffersInSpelling,
				Says:  "color string, leading #, on 23 of 23, upper-case hex on 3",
			},
			{
				Field: "Successor",
				Kind:  CannotSupply,
				Says:  "nil: no successor key",
			},
		},
	},
	{
		Method:    "Labels.ListLabels",
		Product:   Gitea,
		Exercises: "GET /api/v1/repos/{owner}/{repo}/labels, and where the repository moved, the one 301 its old path answers, whose Location names the successor, every later request of the call addressing that successor",
		Requests:  Requests{Min: 1, Max: 2, Paged: true},
		Support:   Supported,
	},
	{
		Method:    "Labels.ListLabels",
		Product:   Forgejo,
		Exercises: "GET /api/v1/repos/{owner}/{repo}/labels, and where the repository moved, the one 301 its old path answers, whose Location names the successor, every later request of the call addressing that successor",
		Requests:  Requests{Min: 1, Max: 2, Paged: true},
		Support:   Supported,
	},
}

// Throttle is one answer a product sends for its rate limit, which every
// operation on that product reports as KindRateLimited, with RetryAfter from the
// Retry-After header where the answer carries one.
//
// Header is the response header that makes the status a throttle, empty where
// the status alone does, and Value the value it carries, empty where any does.
type Throttle struct {
	Product Product
	Header  string
	Value   string
	Says    string
	Status  int
}

// Throttles is every rate-limit answer the families map.
var Throttles = []Throttle{
	{
		Product: GitHub, Status: 403, Header: "X-RateLimit-Remaining", Value: "0",
		Says: "the primary or a secondary limit spent, with no budget left in the window",
	},
	{
		Product: GitHub, Status: 403, Header: "Retry-After",
		Says: "a secondary limit, whatever X-RateLimit-Remaining reads, " +
			"as [GitHub documents it](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api)",
	},
	{Product: GitHub, Status: 429, Says: "a primary or a secondary limit"},
	{Product: GitLab, Status: 429, Says: "a rate limit"},
	{
		Product: Gitea, Status: 403, Header: "RateLimit-Remaining", Value: "0",
		Says: "a rate limit with no budget left in the window",
	},
	{Product: Gitea, Status: 429, Says: "a rate limit"},
	{
		Product: Forgejo, Status: 403, Header: "RateLimit-Remaining", Value: "0",
		Says: "a rate limit with no budget left in the window",
	},
	{Product: Forgejo, Status: 429, Says: "a rate limit"},
}

// Revalidation is what one product does with a conditional read of a REST route:
// the validators it sends, what a not-modified answer bills, and whether a
// document read can be revalidated. NotModifiedPrice is in the product's own
// units. Says carries the citation.
type Revalidation struct {
	Product          Product
	Validators       string
	Documents        string
	Says             string
	NotModifiedPrice int
}

// Revalidations is every product's conditional-read facts, which the read cache
// and the published prices follow.
var Revalidations = []Revalidation{
	{
		Product:    GitHub,
		Validators: "`ETag` on most REST routes, weak on the lists measured, and `Last-Modified` on many ([best practices](https://docs.github.com/en/rest/using-the-rest-api/best-practices-for-using-the-rest-api#use-conditional-requests-if-appropriate))",
		Documents:  "not revalidated: a document is a POST ([forming calls](https://docs.github.com/en/graphql/guides/forming-calls-with-graphql#communicating-with-graphql)), and conditional requests for unsafe methods are unsupported (best practices)",
		Says: "a not-modified answer to a conditional request made with an `Authorization` header does not count against the primary rate limit " +
			"([best practices](https://docs.github.com/en/rest/using-the-rest-api/best-practices-for-using-the-rest-api#use-conditional-requests-if-appropriate)); " +
			"measured with an authorized request, the answer carries `ETag`, `Vary`, `Cache-Control` and the `x-ratelimit-*` headers but no `Link` and no `X-OAuth-Scopes`, and two answers with one body and different `Link` headers carried one `ETag`, which therefore covers the body alone: " +
			"a revalidated page of a list whose body states no total continues wherever it is full, and the identity read, whose scopes ride that header, is never revalidated",
		NotModifiedPrice: 0,
	},
	{
		Product:    GitLab,
		Validators: "a weak `ETag` on every REST read answered 200 with a body that sets no validator of its own, computed by Rack's ETag middleware over the body ([rack/etag.rb](https://github.com/rack/rack/blob/v2.2.23/lib/rack/etag.rb#L26-L63)), and no `Last-Modified` on the routes measured",
		Documents:  "not revalidated: a document is a POST; the ETag polling GitLab documents serves its own frontend routes ([polling](https://docs.gitlab.com/ee/development/polling.html))",
		Says: "a not-modified answer is billed like any request ([rate limits](https://docs.gitlab.com/administration/settings/user_and_ip_rate_limits/)); " +
			"measured with an authenticated request, `ratelimit-observed` rose across a 304 that carried `link`, `x-next-page`, `x-total` and `x-total-pages` as the 200 did, so revalidation saves the body's transfer, not the request",
		NotModifiedPrice: 1,
	},
	{
		Product:          Gitea,
		Validators:       "none on the JSON API: its writer sets only `Content-Type` ([services/context/base.go](https://github.com/go-gitea/gitea/blob/v28.1.0/services/context/base.go#L114-L120)); the raw, media and archive download routes send them ([routers/common/serve.go](https://github.com/go-gitea/gitea/blob/v28.1.0/routers/common/serve.go#L20-L21), [routers/api/v1/repo/file.go](https://github.com/go-gitea/gitea/blob/v28.1.0/routers/api/v1/repo/file.go#L141), [services/repository/archiver/archiver.go](https://github.com/go-gitea/gitea/blob/v28.1.0/services/repository/archiver/archiver.go#L355-L408)), and this library reads none of them; measured on gitea.com",
		Documents:        "no GraphQL API: the API is the REST surface its swagger describes ([API usage](https://docs.gitea.com/development/api-usage)), mounted at `/api/v1` with no GraphQL route beside it ([routers/init.go](https://github.com/go-gitea/gitea/blob/v28.1.0/routers/init.go#L183-L185))",
		Says:             "no validator rides a JSON answer, so no read is revalidated, no not-modified answer arises, and the cache stays inert; the price of 1 is this library's own for a product whose API documentation states no exemption ([API usage](https://docs.gitea.com/development/api-usage)), and the product sends no budget signal (measured)",
		NotModifiedPrice: 1,
	},
	{
		Product:          Forgejo,
		Validators:       "none on the JSON API: its writer sets only `Content-Type` ([services/context/base.go](https://codeberg.org/forgejo/forgejo/src/tag/v16.0.5/services/context/base.go#L137-L148)); the raw, media and archive download routes send them ([routers/common/serve.go](https://codeberg.org/forgejo/forgejo/src/tag/v16.0.5/routers/common/serve.go#L18-L19), [routers/api/v1/repo/file.go](https://codeberg.org/forgejo/forgejo/src/tag/v16.0.5/routers/api/v1/repo/file.go#L147) and [#L371-L374](https://codeberg.org/forgejo/forgejo/src/tag/v16.0.5/routers/api/v1/repo/file.go#L371-L374)), and this library reads none of them; measured on codeberg.org",
		Documents:        "no GraphQL API: the API is the REST surface its swagger describes ([API usage](https://forgejo.org/docs/latest/user/api-usage/)), mounted at `/api/v1` and `/api/forgejo/v1`, both REST ([routers/init.go](https://codeberg.org/forgejo/forgejo/src/tag/v16.0.5/routers/init.go#L180-L181))",
		Says:             "no validator rides a JSON answer, so no read is revalidated, no not-modified answer arises, and the cache stays inert; the price of 1 is this library's own for a product whose API documentation states no exemption ([API usage](https://forgejo.org/docs/latest/user/api-usage/)), and codeberg.org meters every request (`ratelimit: \"baseline\";r=1999;t=600`, measured)",
		NotModifiedPrice: 1,
	},
}
