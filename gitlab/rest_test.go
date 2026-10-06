package gitlab

import (
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
)

const labelRows = `[{"name": "example-label", "color": "#ededed", "description": "Example label."}]`

func TestAListResumesFromItsOwnContinuationAtThePageItNames(t *testing.T) {
	for _, test := range []struct {
		want string
		page int
	}{
		{page: 1, want: "1"},
		{page: 2, want: "2"},
	} {
		t.Run("page_"+test.want, func(t *testing.T) {
			h := newHarness(t, map[string]string{labelsRoute: labelRows})
			after := restCursor(t, h.client, "ListLabels", testRef(), test.page)
			if _, err := h.client.ListLabels(t.Context(), testRef(), forgeapi.WithAfter(after)); err != nil {
				t.Fatalf("ListLabels(WithAfter(page %d)) = %v, want the page", test.page, err)
			}
			if got := h.instance.query(labelsRoute, keyPage); got != test.want {
				t.Errorf("ListLabels(WithAfter(page %d)) sent page=%q, want %q", test.page, got, test.want)
			}
			if !h.logged(" page=" + test.want + " ") {
				t.Errorf("ListLabels(WithAfter(page %d)): no request record names page=%s: %s", test.page, test.want, h.logs)
			}
		})
	}
}

func TestAContinuationThisListDidNotMintIsRefusedBeforeAnyRequest(t *testing.T) {
	h := newHarness(t, nil)
	for name, after := range map[string]forgeapi.Cursor{
		"page_zero":         restCursor(t, h.client, "ListLabels", testRef(), 0),
		"another_lists_own": restCursor(t, h.client, "ListReleases", testRef(), 2),
	} {
		t.Run(name, func(t *testing.T) {
			before := h.instance.count()
			_, err := h.client.ListLabels(t.Context(), testRef(), forgeapi.WithAfter(after))
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeCursorInvalid {
				t.Errorf("ListLabels(WithAfter(%s)) = %v, want code %q", name, err, forgeapi.CodeCursorInvalid)
			}
			if sent := h.instance.count() - before; sent != 0 {
				t.Errorf("ListLabels(WithAfter(%s)) sent %d request(s), want none", name, sent)
			}
		})
	}
}

func TestTheNextPageHeaderNamesTheContinuationsPage(t *testing.T) {
	for _, page := range []int{3, 1} {
		t.Run(strconv.Itoa(page), func(t *testing.T) {
			h := newHarness(t, map[string]string{labelsRoute: labelRows})
			h.instance.answerHeader(headerNextPage, strconv.Itoa(page))
			got, err := h.client.ListLabels(t.Context(), testRef())
			if err != nil {
				t.Fatalf("ListLabels = %v, want the page", err)
			}
			if want := restCursor(t, h.client, "ListLabels", testRef(), page); got.Next != want {
				t.Errorf("ListLabels under %s: %d = next %q, want %q", headerNextPage, page, got.Next, want)
			}
		})
	}
}

func TestANextPageHeaderNamingNoPageEndsTheList(t *testing.T) {
	h := newHarness(t, map[string]string{labelsRoute: labelRows})
	h.instance.answerHeader(headerNextPage, "0")
	got, err := h.client.ListLabels(t.Context(), testRef())
	if err != nil {
		t.Fatalf("ListLabels = %v, want the page", err)
	}
	if got.Next != "" {
		t.Errorf("ListLabels under %s: 0 = next %q, want none", headerNextPage, got.Next)
	}
}

func TestAListAtItsPageCapMarksTheAnswerPartial(t *testing.T) {
	capped := &forgeapi.Partial{Reason: forgeapi.PartialPaginationCap, Fetched: 1, OmittedAtLeast: 1}
	for _, test := range []struct {
		want    *forgeapi.Partial
		name    string
		next    string
		pages   int
		counted int
	}{
		{name: "at_the_cap_with_a_next_page", pages: 1, next: "2", want: capped, counted: 1},
		{name: "at_the_cap_on_the_last_page", pages: 1},
		{name: "under_the_cap_with_a_next_page", pages: 2, next: "2"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{labelsRoute: labelRows}, forgeapi.WithListPages(test.pages))
			h.instance.answerHeader(headerNextPage, test.next)
			page, err := h.client.ListLabels(t.Context(), testRef())
			if err != nil {
				t.Fatalf("ListLabels = %v, want the page", err)
			}
			if !reflect.DeepEqual(page.Partial, test.want) {
				t.Errorf("ListLabels = partial %+v, want %+v", page.Partial, test.want)
			}
			if counted := h.spy.times("PartialResult:" + forgeapi.PartialPaginationCap.String()); counted != test.counted {
				t.Errorf("ListLabels counted the page cap %d time(s), want %d", counted, test.counted)
			}
		})
	}
}

func TestOnlyAScopeRefusalIsWhatTheGrantReads(t *testing.T) {
	for _, test := range []struct {
		name   string
		body   string
		detail string
		want   forgeapi.Support
		source forgeapi.EvidenceSource
	}{
		{
			name:   "a_missing_scope",
			body:   `{"error": "` + errorScope + `", "error_description": "The request requires higher privileges."}`,
			want:   forgeapi.SupportUnknown,
			source: forgeapi.EvidenceResponseBody,
			detail: "ListLabels was refused",
		},
		{
			name:   "a_role_refusal",
			body:   `{"message": "403 Forbidden"}`,
			want:   forgeapi.SupportYes,
			source: forgeapi.EvidenceDefault,
			detail: "no document has run",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{labelsRoute: test.body})
			h.instance.status(labelsRoute, http.StatusForbidden)
			if _, err := h.client.ListLabels(t.Context(), testRef()); err == nil {
				t.Fatal("ListLabels against a 403 = nil, want the refusal")
			}
			caps, err := h.client.GrantCaps(t.Context())
			if err != nil {
				t.Fatalf("GrantCaps = %v, want the capabilities", err)
			}
			if got := caps.Caps[forgeapi.CapReadMergeState]; got != test.want {
				t.Errorf("GrantCaps after %s = merge state %v, want %v", test.name, got, test.want)
			}
			ev := caps.Ev[forgeapi.CapReadMergeState]
			if ev.Source != test.source {
				t.Errorf("GrantCaps after %s = evidence source %q, want %q", test.name, ev.Source, test.source)
			}
			if !strings.HasPrefix(ev.Detail, test.detail) {
				t.Errorf("GrantCaps after %s = evidence detail %q, want it to open %q", test.name, ev.Detail, test.detail)
			}
		})
	}
}
