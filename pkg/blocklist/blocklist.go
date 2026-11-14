// Package blocklist parses DNS blocklists (hosts files, domain lists, simple AdBlock rules) and
// computes the local zones unbound needs to block them.
package blocklist

import (
	"bufio"
	"io"
	"regexp"
	"slices"
	"strings"
)

var labelRe = regexp.MustCompile(`^[a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?$`)

// sinks are the addresses hosts-format lists point blocked names to
var sinks = map[string]bool{"0.0.0.0": true, "127.0.0.1": true, "::": true, "::1": true}

// local names hosts files list for themselves
var ignored = map[string]bool{
	"localhost": true, "localhost.localdomain": true, "local": true, "broadcasthost": true,
	"ip6-localhost": true, "ip6-loopback": true, "0.0.0.0": true,
}

// Parse returns the domains of a list, one per line in any of these forms (the format is guessed
// per line): "0.0.0.0 ads.example" (hosts), "ads.example" (domains), "||ads.example^" (AdBlock).
// Comments (#, !), AdBlock rules with paths, options or wildcards, and invalid names are skipped.
func Parse(r io.Reader) []string {
	var out []string

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())

		// a comment starts the line or follows a blank; "example.com##.ad" is an AdBlock cosmetic rule
		switch i := strings.IndexByte(line, '#'); {
		case i == 0:
			continue
		case i > 0 && (line[i-1] == ' ' || line[i-1] == '\t'):
			line = strings.TrimSpace(line[:i])
		case i > 0:
			continue
		}

		if line == "" || line[0] == '!' || line[0] == '[' {
			continue
		}

		var name string

		switch f := strings.Fields(line); {
		case strings.HasPrefix(line, "||"):
			name = strings.TrimPrefix(line, "||")
			if !strings.HasSuffix(name, "^") {
				continue
			}

			name = strings.TrimSuffix(name, "^")
		case len(f) >= 2 && sinks[f[0]]:
			name = f[1]
		case len(f) == 1:
			name = f[0]
		default:
			continue
		}

		if n, ok := Normalize(name); ok && !ignored[n] {
			out = append(out, n)
		}
	}

	return out
}

// Normalize lowercases a domain name and checks it (at least two labels, no wildcard).
func Normalize(name string) (string, bool) {
	name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
	labels := strings.Split(name, ".")

	if len(labels) < 2 || len(name) > 253 {
		return "", false
	}

	for _, l := range labels {
		if !labelRe.MatchString(l) {
			return "", false
		}
	}

	return name, true
}

// Zones returns the blocked domains (subdomains of a blocked domain are dropped: a local zone
// covers them) and the allowed ones that must stay resolvable, sorted.
func Zones(blocked, allowed []string) (block, allow []string) {
	allowSet := map[string]bool{}

	for _, a := range allowed {
		if n, ok := Normalize(a); ok {
			allowSet[n] = true
		}
	}

	set := map[string]bool{}

	for _, b := range blocked {
		if !allowSet[b] {
			set[b] = true
		}
	}

	for name := range set {
		if covered(name, set) {
			continue
		}

		block = append(block, name)
	}

	// an allowed name inside a blocked zone becomes a transparent zone (more specific wins)
	for a := range allowSet {
		if covered(a, set) {
			allow = append(allow, a)
		}
	}

	slices.Sort(block)
	slices.Sort(allow)

	return block, allow
}

// covered reports whether a parent domain of name is in set.
func covered(name string, set map[string]bool) bool {
	for i := strings.IndexByte(name, '.'); i >= 0; i = strings.IndexByte(name, '.') {
		name = name[i+1:]
		if set[name] {
			return true
		}
	}

	return false
}
