package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fake is an in-memory Updawg API for one organization, `acme`, speaking the
// wire format of openapi.json. Its responses are written as JSON maps rather
// than the generated types, so a test also catches the provider reading a
// field the spec does not have.
type fake struct {
	mu       sync.Mutex
	next     int
	groups   map[string]*fakeGroup
	policies map[string]*fakePolicy
	tokens   map[string]*fakeToken
	channels map[string]*fakeChannel
	rules    map[string]*fakeRule
	// hosts are the fleet: id to labels.
	hosts map[string]map[string]string
	// requests records "METHOD path body" for assertions about what was sent.
	requests []string
}

type fakeGroup struct {
	name        string
	description *string
	selector    map[string]string
	static      []string
}

type fakePolicy struct {
	yaml, name string
	priority   int
	enabled    bool
	version    int
	deleted    bool
}

type fakeChannel struct {
	name, kind string
	// config is what was last sent, which no response ever repeats.
	config  map[string]any
	secrets int
}

type fakeRule struct {
	name     string
	events   []string
	channels []string
	enabled  bool
	filter   map[string]any
}

type fakeToken struct {
	name      string
	labels    map[string]string
	maxUses   *int
	expiresAt *string
	revoked   bool
}

func newFake(t *testing.T) (*fake, *httptest.Server) {
	t.Helper()
	f := &fake{
		groups:   map[string]*fakeGroup{},
		policies: map[string]*fakePolicy{},
		tokens:   map[string]*fakeToken{},
		channels: map[string]*fakeChannel{},
		rules:    map[string]*fakeRule{},
		hosts: map[string]map[string]string{
			"hst_1": {"tier": "web"},
			"hst_2": {"tier": "web"},
			"hst_3": {"tier": "db"},
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/orgs/acme", func(w http.ResponseWriter, _ *http.Request) { reply(w, 200, org("acme", "Acme")) })

	mux.HandleFunc("POST /v1/orgs/acme/groups", f.createGroup)
	mux.HandleFunc("GET /v1/orgs/acme/groups/{id}", f.showGroup)
	mux.HandleFunc("PATCH /v1/orgs/acme/groups/{id}", f.updateGroup)
	mux.HandleFunc("PUT /v1/orgs/acme/groups/{id}/hosts", f.setHosts)
	mux.HandleFunc("DELETE /v1/orgs/acme/groups/{id}", f.deleteGroup)

	mux.HandleFunc("POST /v1/orgs/acme/policies/validate", f.validatePolicy)
	mux.HandleFunc("POST /v1/orgs/acme/policies/preview", f.previewPolicy)
	mux.HandleFunc("POST /v1/orgs/acme/policies", f.savePolicy)
	mux.HandleFunc("PUT /v1/orgs/acme/policies/{id}", f.savePolicy)
	mux.HandleFunc("GET /v1/orgs/acme/policies/{id}", f.showPolicy)
	mux.HandleFunc("DELETE /v1/orgs/acme/policies/{id}", f.deletePolicy)

	mux.HandleFunc("POST /v1/orgs/acme/enrollment-tokens", f.createToken)
	mux.HandleFunc("GET /v1/orgs/acme/enrollment-tokens", f.listTokens)
	mux.HandleFunc("DELETE /v1/orgs/acme/enrollment-tokens/{id}", f.revokeToken)

	mux.HandleFunc("POST /v1/orgs/acme/notification-channels", f.saveChannel)
	mux.HandleFunc("PATCH /v1/orgs/acme/notification-channels/{id}", f.saveChannel)
	mux.HandleFunc("GET /v1/orgs/acme/notification-channels", f.listChannels)
	mux.HandleFunc("DELETE /v1/orgs/acme/notification-channels/{id}", f.deleteChannel)

	mux.HandleFunc("POST /v1/orgs/acme/notification-rules", f.saveRule)
	mux.HandleFunc("PATCH /v1/orgs/acme/notification-rules/{id}", f.saveRule)
	mux.HandleFunc("GET /v1/orgs/acme/notification-rules", f.listRules)
	mux.HandleFunc("DELETE /v1/orgs/acme/notification-rules/{id}", f.deleteRule)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			problem(w, 401, "unauthenticated", "Not signed in")
			return
		}
		var body json.RawMessage
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests = append(f.requests, strings.TrimSpace(r.Method+" "+r.URL.Path+" "+string(body)))
		r.Body = readCloser{strings.NewReader(string(body))}
		// mux.Handler only finds the route; ServeHTTP is what fills in
		// PathValue.
		if _, pattern := mux.Handler(r); pattern == "" {
			problem(w, 404, "not-found", "Not found")
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

type readCloser struct{ *strings.Reader }

func (readCloser) Close() error { return nil }

func (f *fake) id(prefix string) string {
	f.next++
	return fmt.Sprintf("%s_%04d", prefix, f.next)
}

func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// --- groups ---

func (f *fake) groupBody(id string, g *fakeGroup) map[string]any {
	members := map[string]bool{}
	for _, h := range g.static {
		members[h] = true
	}
	hosts := []map[string]any{}
	for hid, labels := range f.hosts {
		byLabel := g.selector != nil
		for k, v := range g.selector {
			if labels[k] != v {
				byLabel = false
			}
		}
		if members[hid] || byLabel {
			hosts = append(hosts, map[string]any{"id": hid, "hostname": hid, "static": members[hid], "by_label": byLabel})
		}
	}
	return map[string]any{
		"id": id, "name": g.name, "description": g.description, "label_selector": g.selector,
		"static_hosts": len(g.static), "resolved_hosts": len(hosts), "created_at": "2026-10-04T08:00:00Z", "hosts": hosts,
	}
}

func (f *fake) createGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name          string            `json:"name"`
		Description   *string           `json:"description"`
		LabelSelector map[string]string `json:"label_selector"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	for _, g := range f.groups {
		if g.name == req.Name {
			problem(w, 409, "name-taken", "A group with that name exists")
			return
		}
	}
	id := f.id("grp")
	f.groups[id] = &fakeGroup{name: req.Name, description: req.Description, selector: req.LabelSelector, static: []string{}}
	reply(w, 201, f.groupBody(id, f.groups[id]))
}

func (f *fake) showGroup(w http.ResponseWriter, r *http.Request) {
	g, ok := f.groups[r.PathValue("id")]
	if !ok {
		problem(w, 404, "not-found", "No such group here")
		return
	}
	reply(w, 200, f.groupBody(r.PathValue("id"), g))
}

func (f *fake) updateGroup(w http.ResponseWriter, r *http.Request) {
	g, ok := f.groups[r.PathValue("id")]
	if !ok {
		problem(w, 404, "not-found", "No such group here")
		return
	}
	// Absent leaves alone, null clears: decode to raw to tell them apart.
	var req map[string]json.RawMessage
	_ = json.NewDecoder(r.Body).Decode(&req)
	renamed := []string{}
	if raw, ok := req["name"]; ok {
		var name string
		_ = json.Unmarshal(raw, &name)
		if name != g.name {
			for _, p := range f.policies {
				if !p.deleted && strings.Contains(p.yaml, "group: "+g.name) {
					renamed = append(renamed, p.name)
				}
			}
		}
		g.name = name
	}
	if raw, ok := req["description"]; ok {
		g.description = nil
		_ = json.Unmarshal(raw, &g.description)
	}
	if raw, ok := req["label_selector"]; ok {
		g.selector = nil
		_ = json.Unmarshal(raw, &g.selector)
	}
	reply(w, 200, map[string]any{"group": f.groupBody(r.PathValue("id"), g), "policies_naming_old_name": renamed})
}

func (f *fake) setHosts(w http.ResponseWriter, r *http.Request) {
	g, ok := f.groups[r.PathValue("id")]
	if !ok {
		problem(w, 404, "not-found", "No such group here")
		return
	}
	var req struct {
		Hosts []string `json:"hosts"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	for _, h := range req.Hosts {
		if _, ok := f.hosts[h]; !ok {
			problem(w, 400, "invalid-request", "Not a host of this organization: "+h)
			return
		}
	}
	g.static = req.Hosts
	reply(w, 200, map[string]any{"static_hosts": len(g.static)})
}

func (f *fake) deleteGroup(w http.ResponseWriter, r *http.Request) {
	if _, ok := f.groups[r.PathValue("id")]; !ok {
		problem(w, 404, "not-found", "No such group here")
		return
	}
	delete(f.groups, r.PathValue("id"))
	reply(w, 200, map[string]any{"proposals_unlinked": 0, "tokens_unlinked": 0, "policies_naming_it": []string{}})
}

// --- policies ---

var (
	policyName     = regexp.MustCompile(`(?m)^name:\s*(.+)$`)
	policyPriority = regexp.MustCompile(`(?m)^priority:\s*(\d+)$`)
	policyEnabled  = regexp.MustCompile(`(?m)^enabled:\s*(true|false)$`)
)

// compile is the fake's policy compiler: a document needs a name, and a
// line reading `bogus` is the error every test uses.
func compile(yaml string) (*fakePolicy, map[string]any) {
	for i, line := range strings.Split(yaml, "\n") {
		if strings.TrimSpace(line) == "bogus" {
			return nil, map[string]any{"line": i + 1, "column": 1, "detail": "unknown field `bogus`"}
		}
	}
	m := policyName.FindStringSubmatch(yaml)
	if m == nil {
		return nil, map[string]any{"detail": "missing field `name`"}
	}
	p := &fakePolicy{yaml: yaml, name: strings.TrimSpace(m[1]), priority: 100, enabled: true}
	if m := policyPriority.FindStringSubmatch(yaml); m != nil {
		p.priority, _ = strconv.Atoi(m[1])
	}
	if m := policyEnabled.FindStringSubmatch(yaml); m != nil {
		p.enabled = m[1] == "true"
	}
	return p, nil
}

func policyBody(id string, p *fakePolicy) map[string]any {
	return map[string]any{
		"id": id, "name": p.name, "priority": p.priority, "enabled": p.enabled, "version": p.version, "showing": p.version,
		"yaml": p.yaml, "created_at": "2026-10-04T08:00:00Z", "updated_at": "2026-10-04T08:00:00Z", "written_at": "2026-10-04T08:00:00Z",
	}
}

func (f *fake) validatePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		YAML string `json:"yaml"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	p, problem := compile(req.YAML)
	if problem != nil {
		reply(w, 200, map[string]any{"valid": false, "errors": []any{problem}})
		return
	}
	reply(w, 200, map[string]any{"valid": true, "errors": []any{}, "name": p.name, "priority": p.priority, "enabled": p.enabled, "rules": 1})
}

// previewPolicy answers as the API does for a document whose name another
// live policy holds; otherwise a preview of nothing changing, since what a
// preview says is the server's business and previewSummary has its own
// tests.
func (f *fake) previewPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		YAML     string `json:"yaml"`
		Replaces string `json:"replaces"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	p, bad := compile(req.YAML)
	if bad != nil {
		problem(w, 400, "invalid-request", bad["detail"].(string))
		return
	}
	for id, o := range f.policies {
		if id != req.Replaces && !o.deleted && o.name == p.name {
			problem(w, 409, "name-taken", "A live policy already has that name")
			return
		}
	}
	reply(w, 200, map[string]any{
		"opening": []any{}, "closing": []any{}, "changing": []any{}, "unchanged": []any{}, "dropped": []any{}, "newly_covered": []any{},
		"hosts_evaluated": len(f.hosts), "hosts_total": len(f.hosts), "complete": true, "would_auto_merge": 0, "changes_nothing": true,
	})
}

func (f *fake) savePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		YAML string `json:"yaml"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	p, bad := compile(req.YAML)
	if bad != nil {
		problem(w, 400, "invalid-request", bad["detail"].(string))
		return
	}
	id := r.PathValue("id")
	status := 200
	if id == "" {
		id, status = f.id("pol"), 201
	} else if old, ok := f.policies[id]; !ok || old.deleted {
		problem(w, 404, "not-found", "No such policy here")
		return
	} else {
		p.version = old.version
	}
	for oid, o := range f.policies {
		if oid != id && !o.deleted && o.name == p.name {
			problem(w, 409, "name-taken", "A live policy already has that name")
			return
		}
	}
	p.version++
	f.policies[id] = p
	reply(w, status, policyBody(id, p))
}

func (f *fake) showPolicy(w http.ResponseWriter, r *http.Request) {
	p, ok := f.policies[r.PathValue("id")]
	if !ok || p.deleted {
		problem(w, 404, "not-found", "No such policy here")
		return
	}
	reply(w, 200, policyBody(r.PathValue("id"), p))
}

func (f *fake) deletePolicy(w http.ResponseWriter, r *http.Request) {
	p, ok := f.policies[r.PathValue("id")]
	if !ok || p.deleted {
		problem(w, 404, "not-found", "No such policy here")
		return
	}
	p.deleted = true
	w.WriteHeader(204)
}

// --- enrollment tokens ---

func tokenBody(id string, t *fakeToken) map[string]any {
	labels := t.labels
	if labels == nil {
		labels = map[string]string{}
	}
	var revoked any
	if t.revoked {
		revoked = "2026-10-04T09:00:00Z"
	}
	return map[string]any{
		"id": id, "name": t.name, "labels": labels, "max_uses": t.maxUses, "expires_at": t.expiresAt,
		"revoked_at": revoked, "uses": 0, "usable": !t.revoked, "created_at": "2026-10-04T08:00:00Z",
	}
}

func (f *fake) createToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string            `json:"name"`
		Labels    map[string]string `json:"labels"`
		MaxUses   *int              `json:"max_uses"`
		ExpiresAt *time.Time        `json:"expires_at"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	t := &fakeToken{name: req.Name, labels: req.Labels, maxUses: req.MaxUses}
	if req.ExpiresAt != nil {
		// The API's own spelling of the instant, not the request's: a
		// provider that compares strings would see a change every plan.
		s := req.ExpiresAt.UTC().Format("2006-01-02T15:04:05.000000+00:00")
		t.expiresAt = &s
	}
	id := f.id("etk")
	f.tokens[id] = t
	body := tokenBody(id, t)
	body["token"] = "enr_secret_" + id
	reply(w, 201, body)
}

func (f *fake) listTokens(w http.ResponseWriter, _ *http.Request) {
	out := []any{}
	for id, t := range f.tokens {
		out = append(out, tokenBody(id, t))
	}
	reply(w, 200, out)
}

func (f *fake) revokeToken(w http.ResponseWriter, r *http.Request) {
	t, ok := f.tokens[r.PathValue("id")]
	if !ok || t.revoked {
		problem(w, 404, "not-found", "No such token here, or already revoked")
		return
	}
	t.revoked = true
	w.WriteHeader(204)
}

// --- notification channels ---

func channelBody(id string, c *fakeChannel) map[string]any {
	display := c.kind
	for _, v := range c.config {
		if s, ok := v.(string); ok && len(s) >= 4 {
			display = c.kind + " …" + s[len(s)-4:]
		}
	}
	return map[string]any{
		"id": id, "name": c.name, "kind": c.kind, "display": display,
		"created_at": "2026-10-04T08:00:00Z", "updated_at": "2026-10-04T08:00:00Z",
	}
}

func (f *fake) saveChannel(w http.ResponseWriter, r *http.Request) {
	var req map[string]json.RawMessage
	_ = json.NewDecoder(r.Body).Decode(&req)
	id := r.PathValue("id")
	status := 200
	c := f.channels[id]
	if id == "" {
		id, status, c = f.id("nch"), 201, &fakeChannel{}
		_ = json.Unmarshal(req["kind"], &c.kind)
	} else if c == nil {
		problem(w, 404, "not-found", "No such channel here")
		return
	}
	if raw, ok := req["name"]; ok {
		_ = json.Unmarshal(raw, &c.name)
	}
	configSet := false
	if raw, ok := req["config"]; ok && string(raw) != "null" {
		c.config = nil
		_ = json.Unmarshal(raw, &c.config)
		configSet = true
	}
	f.channels[id] = c
	body := channelBody(id, c)
	if configSet && c.kind == "webhook" {
		c.secrets++
		body["signing_secret"] = fmt.Sprintf("whsec_%s_%d", id, c.secrets)
	}
	reply(w, status, body)
}

func (f *fake) listChannels(w http.ResponseWriter, _ *http.Request) {
	out := []any{}
	for id, c := range f.channels {
		out = append(out, channelBody(id, c))
	}
	reply(w, 200, out)
}

func (f *fake) deleteChannel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := f.channels[id]; !ok {
		problem(w, 404, "not-found", "No such channel here")
		return
	}
	delete(f.channels, id)
	// As the API does: the rules that sent to it go with it.
	for rid, rule := range f.rules {
		for _, c := range rule.channels {
			if c == id {
				delete(f.rules, rid)
			}
		}
	}
	w.WriteHeader(204)
}

// --- notification rules ---

func ruleBody(id string, r *fakeRule) map[string]any {
	filter := map[string]any{"min_severity": nil, "known_exploited": nil, "proposal_kinds": nil}
	for k, v := range r.filter {
		filter[k] = v
	}
	return map[string]any{
		"id": id, "name": r.name, "event_types": r.events, "channel_ids": r.channels, "enabled": r.enabled,
		"filter": filter, "created_at": "2026-10-04T08:00:00Z", "updated_at": "2026-10-04T08:00:00Z",
	}
}

func (f *fake) saveRule(w http.ResponseWriter, r *http.Request) {
	var req map[string]json.RawMessage
	_ = json.NewDecoder(r.Body).Decode(&req)
	id := r.PathValue("id")
	status := 200
	rule := f.rules[id]
	if id == "" {
		id, status, rule = f.id("nru"), 201, &fakeRule{enabled: true}
	} else if rule == nil {
		problem(w, 404, "not-found", "No such rule here")
		return
	}
	set := func(key string, into any) {
		if raw, ok := req[key]; ok && string(raw) != "null" {
			_ = json.Unmarshal(raw, into)
		}
	}
	set("name", &rule.name)
	set("event_types", &rule.events)
	set("channel_ids", &rule.channels)
	set("enabled", &rule.enabled)
	if raw, ok := req["filter"]; ok && string(raw) != "null" {
		rule.filter = map[string]any{}
		_ = json.Unmarshal(raw, &rule.filter)
	}
	for _, c := range rule.channels {
		if _, ok := f.channels[c]; !ok {
			problem(w, 400, "invalid-request", "Not a channel of this organization: "+c)
			return
		}
	}
	f.rules[id] = rule
	reply(w, status, ruleBody(id, rule))
}

func (f *fake) listRules(w http.ResponseWriter, _ *http.Request) {
	out := []any{}
	for id, r := range f.rules {
		out = append(out, ruleBody(id, r))
	}
	reply(w, 200, out)
}

func (f *fake) deleteRule(w http.ResponseWriter, r *http.Request) {
	if _, ok := f.rules[r.PathValue("id")]; !ok {
		problem(w, 404, "not-found", "No such rule here")
		return
	}
	delete(f.rules, r.PathValue("id"))
	w.WriteHeader(204)
}

// sent says whether a request matching pattern was made.
func (f *fake) sent(pattern string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	re := regexp.MustCompile(pattern)
	for _, r := range f.requests {
		if re.MatchString(r) {
			return true
		}
	}
	return false
}
