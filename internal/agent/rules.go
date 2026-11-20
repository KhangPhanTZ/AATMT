package agent

import (
	"regexp"
	"strings"

	"github.com/khangphantz/aatmt/internal/store"
)

// chained reports whether a command uses shell composition that could hide a
// second command behind an allowed prefix.
func chained(cmd string) bool { return strings.ContainsAny(cmd, ";&|`$><\n") }

// segments splits a command line on shell separators (for deny rules, which
// must catch a forbidden command anywhere in a chain).
func segments(cmd string) []string {
	f := func(r rune) bool {
		return r == ';' || r == '&' || r == '|' || r == '\n' || r == '`' || r == '(' || r == ')'
	}
	var out []string
	for _, s := range strings.FieldsFunc(cmd, f) {
		if s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "$")); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func prefixMatch(cmd, p string) bool {
	p = strings.TrimSpace(p)
	return p != "" && (cmd == p || strings.HasPrefix(cmd, p+" "))
}

func argvMatch(cmd, p string) bool {
	want := strings.Fields(p)
	got := strings.Fields(cmd)
	if len(want) == 0 || len(got) < len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func ruleMatches(r *store.Rule, cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	switch r.Kind {
	case "regex":
		re, err := regexp.Compile(r.Pattern)
		return err == nil && re.MatchString(cmd)
	case "contains":
		return r.Pattern != "" && strings.Contains(cmd, r.Pattern)
	}
	// prefix / argv: an allow rule only matches a single simple command; deny
	// and ask rules match any segment of a chain.
	match := prefixMatch
	if r.Kind == "argv" {
		match = argvMatch
	}
	if r.Action == "allow" {
		return !chained(cmd) && match(cmd, r.Pattern)
	}
	for _, seg := range segments(cmd) {
		if match(seg, r.Pattern) {
			return true
		}
	}
	return false
}

// decide returns "allow" | "deny" | "ask" for a command, plus who decided.
func decide(cfg store.Config, cmd string) (action, by, rule string) {
	for _, r := range cfg.Rules {
		if r.Enabled && ruleMatches(r, cmd) {
			return r.Action, "rule", r.Kind + ":" + r.Pattern
		}
	}
	st := cfg.Settings
	switch st.CommandPolicy {
	case "off":
		return "deny", "policy", "off"
	case "auto":
		return "allow", "policy", "auto"
	}
	if allowed(cmd, st.CommandAllowlist) {
		return "allow", "policy", "allowlist"
	}
	if st.CommandPolicy == "allowlist" {
		return "deny", "policy", "allowlist"
	}
	return "ask", "policy", "ask"
}

func allowed(cmd string, list []string) bool {
	c := strings.TrimSpace(cmd)
	if chained(c) {
		return false
	}
	for _, p := range list {
		if prefixMatch(c, p) {
			return true
		}
	}
	return false
}
