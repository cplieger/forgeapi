package github

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func FuzzARenamedRepositoryIsResolvedOnlyAtTheDigitIDItsLocationCarries(f *testing.F) {
	for _, seed := range []string{
		"api/v3/repositories/1246315859/labels?page=2",
		"repositories/7",
		"api/v3/repositories",
		"repositories/",
		"repositories/12a",
		"repositories/..%2F1",
		"repos/example/example",
		"repositories/repositories/3",
		"%zz",
		"",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, path string) {
		in := newInstance()
		in.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			in.mu.Lock()
			in.requests = append(in.requests, r.Method+" "+r.URL.EscapedPath())
			seen := len(in.requests)
			in.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			switch seen {
			case 1:
				// Pinned to this origin: a read follows a hop to another host, and would dial it.
				w.Header().Set("Location", "http://"+r.Host+"/"+path)
				w.WriteHeader(http.StatusMovedPermanently)
			case 2:
				_, _ = w.Write([]byte(`[]`))
			default:
				_, _ = w.Write([]byte(`{"full_name":"example/renamed"}`))
			}
		}))
		defer in.server.Close()
		h := newHarnessOver(t, in)
		_, _ = h.client.ListLabels(t.Context(), testRef())

		in.mu.Lock()
		requests := append([]string(nil), in.requests...)
		in.mu.Unlock()
		if len(requests) > 3 {
			t.Fatalf("ListLabels redirected to /%s sent %q, want the read, its hop and at most one resolving read", path, requests)
		}
		if len(requests) < 3 {
			return
		}
		hop := strings.TrimPrefix(requests[1], "GET ")
		id, ok := strings.CutPrefix(requests[2], "GET /api/v3/repositories/")
		if !ok || id == "" || strings.TrimLeft(id, "0123456789") != "" {
			t.Fatalf("ListLabels redirected to /%s resolved its successor at %q, want GET /api/v3/repositories/ and digits alone", path, requests[2])
		}
		if !strings.Contains(hop+"/", "/repositories/"+id+"/") {
			t.Fatalf("ListLabels resolved id %q from a hop to %q, want a segment the hop carries right after a repositories segment", id, hop)
		}
	})
}
