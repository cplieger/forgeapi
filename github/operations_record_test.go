package github

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cplieger/forgeapi"
)

// refusingOnce answers the first request to route with a 502 from beneath the retry
// layer and sends every other request on to the instance.
type refusingOnce struct {
	next   http.RoundTripper
	route  string
	served atomic.Bool
}

func (r *refusingOnce) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method+" "+req.URL.EscapedPath() == r.route && r.served.CompareAndSwap(false, true) {
		return &http.Response{
			StatusCode: http.StatusBadGateway, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{}`)), Request: req,
		}, nil
	}
	return r.next.RoundTrip(req)
}

// recordedHarness is a harness whose first request to route is refused once with a
// 502, logging JSON lines into logs.
func recordedHarness(t *testing.T, routes map[string]string, route string, logs *bytes.Buffer) *harness {
	t.Helper()
	return newHarness(t, routes,
		forgeapi.WithWireTransport(&refusingOnce{next: &http.Transport{}, route: route}),
		forgeapi.WithLogger(slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))))
}

// lineOf is the one recorded line naming err's diagnostic id.
func lineOf(t *testing.T, logs *bytes.Buffer, err error) map[string]any {
	t.Helper()
	fe, ok := errors.AsType[*forgeapi.Error](err)
	if !ok || fe.DiagID == "" {
		t.Fatalf("the operation = %v, want a failure carrying a diagnostic id", err)
	}
	var found []map[string]any
	dec := json.NewDecoder(logs)
	for {
		var line map[string]any
		if decodeErr := dec.Decode(&line); errors.Is(decodeErr, io.EOF) {
			break
		} else if decodeErr != nil {
			t.Fatalf("Setup: decoding a recorded line: %v", decodeErr)
		}
		if line["diag_id"] == fe.DiagID {
			found = append(found, line)
		}
	}
	if len(found) != 1 {
		t.Fatalf("the log holds %d line(s) naming diagnostic id %s, want 1", len(found), fe.DiagID)
	}
	return found[0]
}

func TestAFamilyFailureAfterARetriedRequestReadsThatRetry(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "a_refused_document", body: `{"errors":[{"type":"FORBIDDEN","message":"refused"}]}`},
		{name: "a_null_search", body: `{"data":{"search":null}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var logs bytes.Buffer
			h := recordedHarness(t, map[string]string{documentRoute: test.body}, documentRoute, &logs)
			_, err := h.client.ListMyPRs(t.Context())
			line := lineOf(t, &logs, err)
			if line["attempt"] != float64(2) || line["retried_on"] != "502" {
				t.Errorf("the failure line after a retried 502 = attempt %v, retried_on %v, want 2, 502", line["attempt"], line["retried_on"])
			}
		})
	}
}

func TestAMergeAfterARetriedReadReadsTheMergeRequest(t *testing.T) {
	var logs bytes.Buffer
	h := recordedHarness(t, map[string]string{pullsRoute: autoMergePull("clean"), mergeRoute: `{}`}, pullsRoute, &logs)
	h.instance.status(mergeRoute, http.StatusConflict)
	_, err := h.client.MergePR(t.Context(), testRef(), testPR(), forgeapi.MergeRequest{HeadSHA: testHeadSHA, AutoMerge: true})
	line := lineOf(t, &logs, err)
	if got, ok := line["retried_on"]; line["attempt"] != float64(1) || ok {
		t.Errorf("the 409 failure after a read retried on a 502 = attempt %v, retried_on %v, want 1 and none: the merge PUT is the last request",
			line["attempt"], got)
	}
}
