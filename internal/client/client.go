package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultURL is the API a provider talks to unless told otherwise.
const DefaultURL = "https://api.updawg.net"

// maxRetries bounds how often one request is retried after a 429. The API
// answers 429 per organization (docs: rate-limits), and a `terraform apply`
// of a few dozen resources at the default parallelism of ten can reach it.
const maxRetries = 4

// maxWait caps a single Retry-After. Longer than this and the person running
// `apply` is better served by an error than by a silent stall.
const maxWait = 60 * time.Second

// New returns a client for the API at baseURL that authenticates with an API
// token (`upd_…`) and names itself with userAgent.
func New(baseURL, token, userAgent string) (*ClientWithResponses, error) {
	return NewWithDoer(baseURL, token, userAgent, &retrying{next: http.DefaultClient, sleep: sleepCtx})
}

// NewWithDoer is New with the HTTP transport supplied, for tests.
func NewWithDoer(baseURL, token, userAgent string, doer HttpRequestDoer) (*ClientWithResponses, error) {
	return NewClientWithResponses(
		strings.TrimRight(baseURL, "/"),
		WithHTTPClient(doer),
		WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("User-Agent", userAgent)
			return nil
		}),
	)
}

// retrying retries a request the API answered with 429, waiting as long as
// its Retry-After says.
type retrying struct {
	next  HttpRequestDoer
	sleep func(context.Context, time.Duration) error
}

func (r *retrying) Do(req *http.Request) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		// A body has to be read again on a retry. The generated client
		// builds every request from a byte slice, so GetBody is always set
		// when there is a body.
		if attempt > 0 && req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			req.Body = body
		}
		resp, err := r.next.Do(req)
		if err != nil || resp.StatusCode != http.StatusTooManyRequests || attempt == maxRetries {
			return resp, err
		}
		wait := retryAfter(resp.Header.Get("Retry-After"), attempt)
		_ = resp.Body.Close()
		if err := r.sleep(req.Context(), wait); err != nil {
			return nil, err
		}
	}
}

// retryAfter reads a Retry-After of seconds, falling back to exponential
// backoff from one second when it is missing or unreadable.
func retryAfter(header string, attempt int) time.Duration {
	wait := time.Duration(1<<attempt) * time.Second
	if secs, err := strconv.Atoi(strings.TrimSpace(header)); err == nil && secs >= 0 {
		wait = time.Duration(secs) * time.Second
	}
	return min(wait, maxWait)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Problem is an error response from the API: RFC 9457 problem details
// (docs: errors). Type is the stable part to branch on and Title and Detail
// are for people.
type Problem struct {
	Status int
	Type   string
	Title  string
	Detail string
}

func (p *Problem) Error() string {
	msg := fmt.Sprintf("%d %s", p.Status, p.Title)
	if p.Detail != "" {
		msg += ": " + p.Detail
	}
	if name := p.Name(); name != "" {
		msg += " (" + name + ")"
	}
	return msg
}

// problemTypes is where every problem type the API returns lives.
const problemTypes = "https://updawg.net/problems/"

// Name is the type without its namespace — `insufficient-scope` — or empty
// for a problem the API did not write.
func (p *Problem) Name() string {
	if name, ok := strings.CutPrefix(p.Type, problemTypes); ok {
		return name
	}
	return ""
}

// ProblemFrom builds the error for a response the caller did not expect.
// The body is a problem document on every error the API itself returns; a
// proxy in the way may answer with something else, which is kept as the
// detail so it is not lost.
func ProblemFrom(resp *http.Response, body []byte) *Problem {
	var doc ProblemBody
	if err := json.Unmarshal(body, &doc); err == nil && doc.Title != "" {
		p := &Problem{Status: resp.StatusCode, Type: doc.Type, Title: doc.Title}
		if doc.Detail.IsSpecified() && !doc.Detail.IsNull() {
			p.Detail = doc.Detail.MustGet()
		}
		return p
	}
	return &Problem{
		Status: resp.StatusCode,
		Title:  http.StatusText(resp.StatusCode),
		Detail: strings.TrimSpace(string(body)),
	}
}

// IsNotFound says whether err is the API's 404 — which it also gives for an
// organization the token cannot see, so a resource whose org was renamed
// out from under it reads as gone, as it should.
func IsNotFound(err error) bool {
	var p *Problem
	return errors.As(err, &p) && p.Status == http.StatusNotFound
}
