package client

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// scripted answers each request with the next status, recording the bodies
// it was sent.
type scripted struct {
	statuses []int
	headers  []http.Header
	bodies   []string
}

func (s *scripted) Do(req *http.Request) (*http.Response, error) {
	var body string
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		body = string(b)
	}
	s.bodies = append(s.bodies, body)
	i := len(s.bodies) - 1
	h := http.Header{}
	if i < len(s.headers) && s.headers[i] != nil {
		h = s.headers[i]
	}
	return &http.Response{StatusCode: s.statuses[i], Header: h, Body: io.NopCloser(strings.NewReader("{}"))}, nil
}

func TestRetriesA429WithTheSameBody(t *testing.T) {
	next := &scripted{
		statuses: []int{429, 429, 201},
		headers:  []http.Header{{"Retry-After": {"3"}}, nil},
	}
	var waits []time.Duration
	r := &retrying{next: next, sleep: func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}}

	req, _ := http.NewRequest(http.MethodPost, "https://api.example/v1/orgs/acme/groups", strings.NewReader(`{"name":"web"}`))
	resp, err := r.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 201 {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	for i, b := range next.bodies {
		if b != `{"name":"web"}` {
			t.Errorf("attempt %d sent %q", i, b)
		}
	}
	// Retry-After first, then backoff from 2 s for the second attempt.
	if want := []time.Duration{3 * time.Second, 2 * time.Second}; !equal(waits, want) {
		t.Errorf("waits = %v, want %v", waits, want)
	}
}

func TestGivesUpAfterMaxRetries(t *testing.T) {
	next := &scripted{statuses: []int{429, 429, 429, 429, 429, 429}}
	r := &retrying{next: next, sleep: func(context.Context, time.Duration) error { return nil }}

	req, _ := http.NewRequest(http.MethodGet, "https://api.example/v1/orgs/acme", nil)
	resp, err := r.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 429 || len(next.bodies) != maxRetries+1 {
		t.Fatalf("status %d after %d attempts", resp.StatusCode, len(next.bodies))
	}
}

func TestRetryAfterIsCapped(t *testing.T) {
	if got := retryAfter("3600", 0); got != maxWait {
		t.Errorf("retryAfter(3600) = %v, want %v", got, maxWait)
	}
	if got := retryAfter("soon", 2); got != 4*time.Second {
		t.Errorf("unreadable header on attempt 2 = %v, want 4s", got)
	}
}

func TestProblemFromAProblemDocument(t *testing.T) {
	body := `{"type":"https://updawg.net/problems/insufficient-scope","title":"Not permitted for this token","status":403,"detail":"this API token does not carry the ` + "`groups`" + ` scope"}`
	p := ProblemFrom(&http.Response{StatusCode: 403}, []byte(body))
	want := "403 Not permitted for this token: this API token does not carry the `groups` scope (insufficient-scope)"
	if p.Error() != want {
		t.Errorf("got  %q\nwant %q", p.Error(), want)
	}
	if p.Name() != "insufficient-scope" {
		t.Errorf("Name() = %q", p.Name())
	}
}

func TestProblemFromSomethingElse(t *testing.T) {
	p := ProblemFrom(&http.Response{StatusCode: 502}, []byte("<html>bad gateway</html>\n"))
	if p.Error() != "502 Bad Gateway: <html>bad gateway</html>" {
		t.Errorf("got %q", p.Error())
	}
	if IsNotFound(p) {
		t.Error("a 502 is not a 404")
	}
	if !IsNotFound(&Problem{Status: 404}) {
		t.Error("a 404 is")
	}
}

func equal(a, b []time.Duration) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
