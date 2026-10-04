package provider

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Pez-Solutions/terraform-provider-updawg/internal/client"
)

// listed is how many items a summary names before saying "and N more".
const listed = 5

// previewSummary renders what saving a policy would do to the fleet, for a
// plan's warning (DAWG-138). It reports whether there is anything to say:
// a preview that changes nothing is not worth a warning on every plan.
//
// The order is the order of consequence. Updates nothing would decide any
// more come first, since that is the change that leaves hosts unpatched
// and shows up in no proposal count; then anything that would go out with
// nobody looking.
func previewSummary(p *client.PreviewBody) (string, bool) {
	if p.ChangesNothing {
		return "", false
	}
	var b strings.Builder

	scope := fmt.Sprintf("%d hosts", p.HostsEvaluated)
	if !p.Complete {
		scope = fmt.Sprintf("the first %d of %d hosts (counts are lower bounds)", p.HostsEvaluated, p.HostsTotal)
	}
	fmt.Fprintf(&b, "Saving this policy would, across %s:\n", scope)

	if len(p.Dropped) > 0 {
		fmt.Fprintf(&b, "\n⚠️ Stop deciding %s nothing else covers: %s\n", plural(len(p.Dropped), "update"), coverage(p.Dropped))
	}

	// An opening and a closing that do the same work are one proposal that
	// moved to another rule, not one gone and one new.
	var opening, closing, moved []client.Proposed
	for _, o := range p.Opening {
		if o.SameWorkAs.IsSpecified() && !o.SameWorkAs.IsNull() {
			moved = append(moved, o)
		} else {
			opening = append(opening, o)
		}
	}
	for _, c := range p.Closing {
		if !c.SameWorkAs.IsSpecified() || c.SameWorkAs.IsNull() {
			closing = append(closing, c)
		}
	}

	if len(opening) > 0 {
		fmt.Fprintf(&b, "\nOpen %s%s: %s\n", plural(len(opening), "proposal"), autoMerging(opening), proposals(opening))
	}
	if len(p.Changing) > 0 {
		fmt.Fprintf(&b, "\nChange %s%s: %s\n", plural(len(p.Changing), "proposal"), autoMerging(p.Changing), proposals(p.Changing))
	}
	if len(closing) > 0 {
		fmt.Fprintf(&b, "\nClose %s: %s\n", plural(len(closing), "proposal"), proposals(closing))
	}
	if len(moved) > 0 {
		fmt.Fprintf(&b, "\nMove %s to another rule, the same work under a new key.\n", plural(len(moved), "proposal"))
	}
	if len(p.NewlyCovered) > 0 {
		fmt.Fprintf(&b, "\nStart deciding %s nothing decides now: %s\n", plural(len(p.NewlyCovered), "update"), coverage(p.NewlyCovered))
	}
	fmt.Fprintf(&b, "\nAfterwards, %s would go out with nobody looking.", plural(p.WouldAutoMerge, "proposal"))
	return b.String(), true
}

// autoMerging says how many of these would merge without review.
func autoMerging(ps []client.Proposed) string {
	n := 0
	for _, p := range ps {
		if p.Action == client.ActionAutoMerge {
			n++
		}
	}
	switch {
	case n == 0:
		return ""
	case n == len(ps) && n == 1:
		return ", which would auto-merge"
	case n == len(ps):
		return ", all of which would auto-merge"
	default:
		return fmt.Sprintf(", %d of which would auto-merge", n)
	}
}

// proposals names the biggest few: `security on 40 hosts (high)`.
func proposals(ps []client.Proposed) string {
	sorted := append([]client.Proposed(nil), ps...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Hosts > sorted[j].Hosts })
	var parts []string
	for _, p := range sorted[:min(len(sorted), listed)] {
		s := fmt.Sprintf("%s on %s", p.Kind, plural(p.Hosts, "host"))
		if p.MaxSeverity.IsSpecified() && !p.MaxSeverity.IsNull() {
			s += fmt.Sprintf(" (%s)", p.MaxSeverity.MustGet())
		}
		if p.Action == client.ActionAutoMerge {
			s += " [auto-merge]"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ", ") + more(len(sorted))
}

// coverage names the packages affecting most hosts: `openssl (12 hosts)`.
func coverage(cs []client.Coverage) string {
	sorted := append([]client.Coverage(nil), cs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Hosts > sorted[j].Hosts })
	var parts []string
	for _, c := range sorted[:min(len(sorted), listed)] {
		parts = append(parts, fmt.Sprintf("%s (%s)", c.Package, plural(c.Hosts, "host")))
	}
	return strings.Join(parts, ", ") + more(len(sorted))
}

func more(n int) string {
	if n <= listed {
		return ""
	}
	return fmt.Sprintf(" and %d more", n-listed)
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
