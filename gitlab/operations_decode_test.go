package gitlab

import (
	"bufio"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi/v2"
)

// failureLine is the one logged line naming err's diagnostic id, as its key=value
// pairs.
func failureLine(t *testing.T, h *harness, err error) string {
	t.Helper()
	fe, ok := errors.AsType[*forgeapi.Error](err)
	if !ok || fe.DiagID == "" {
		t.Fatalf("the operation = %v, want a failure carrying a diagnostic id", err)
	}
	var found []string
	scan := bufio.NewScanner(strings.NewReader(h.logs.String()))
	for scan.Scan() {
		if strings.Contains(scan.Text(), "diag_id="+fe.DiagID) {
			found = append(found, scan.Text())
		}
	}
	if len(found) != 1 {
		t.Fatalf("the log holds %d line(s) naming diagnostic id %s, want 1", len(found), fe.DiagID)
	}
	return found[0]
}

func TestADocumentThatDoesNotDecodeIsRecordedWithItsCause(t *testing.T) {
	for _, test := range []struct {
		name  string
		body  string
		cause string
	}{
		{name: "an_empty_200", body: "", cause: `error="empty response body"`},
		{name: "a_truncated_200", body: `{"data":{"project":`, cause: `error="decoding response: `},
		{name: "data_that_does_not_decode", body: `{"data":{"project":"a string"}}`, cause: `error="decoding response: `},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{documentRoute: test.body})
			_, err := h.client.ListPRs(t.Context(), testRef())
			fe, ok := errors.AsType[*forgeapi.Error](err)
			if !ok || fe.Code != forgeapi.CodeValidation || fe.Kind != forgeapi.KindUpstream || fe.Message != "response body did not decode" {
				t.Errorf("ListPRs over a document that does not decode = %v, want the validation failure of kind upstream", err)
			}
			line := failureLine(t, h, err)
			for _, want := range []string{test.cause, "content_type=application/json", "body_bytes=" + strconv.Itoa(len(test.body)), "attempt=1 "} {
				if !strings.Contains(line, want) {
					t.Errorf("the failure line does not carry %s: %s", want, line)
				}
			}
		})
	}
}
