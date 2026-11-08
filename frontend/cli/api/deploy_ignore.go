package api

import "strings"

// A .gitignore's lines, as git reads them: the last line that matches a path says whether it stays, so that a folder's
// own .gitignore, read after those above it, has the last word; a line beginning ! takes a path back. A path in a folder
// that stays is never looked at, so nothing in it can be taken back, as in git. The Python library and the dashboard read
// them the same way, and the three share their test's cases.

type ignoreRule struct {
	base     string   // the folder it was read in, "" or ending in a slash: it speaks of what is in there
	segments []string // the pattern, split at its slashes
	anchored bool     // with a slash before its end, it is matched from base; without, against a name at any depth
	negate   bool
	dirOnly  bool
}

// parseIgnore is the lines of a .gitignore read in the folder base ("", or ending in a slash).
func parseIgnore(base, text string) []ignoreRule {
	var rules []ignoreRule
	for _, line := range strings.Split(text, "\n") {
		line = trimIgnoreLine(strings.TrimSuffix(line, "\r"))
		if line == "" || line[0] == '#' {
			continue
		}
		r := ignoreRule{base: base}
		switch {
		case line[0] == '!':
			r.negate, line = true, line[1:]
		case strings.HasPrefix(line, `\!`), strings.HasPrefix(line, `\#`):
			line = line[1:]
		}
		r.dirOnly = strings.HasSuffix(line, "/")
		line = strings.TrimRight(line, "/")
		if line == "" {
			continue
		}
		r.anchored = strings.Contains(line, "/")
		for _, part := range strings.Split(strings.TrimLeft(line, "/"), "/") {
			// (a run of ** is one)
			if part != "**" || len(r.segments) == 0 || r.segments[len(r.segments)-1] != "**" {
				r.segments = append(r.segments, part)
			}
		}
		rules = append(rules, r)
	}
	return rules
}

// A line without the spaces at its end, but for one written with a backslash before it.
func trimIgnoreLine(line string) string {
	for strings.HasSuffix(line, " ") && !strings.HasSuffix(line, `\ `) {
		line = line[:len(line)-1]
	}
	return line
}

// ignoredBy says whether the rules leave out a path in the folder (with slashes, and no slash at its end).
func ignoredBy(rules []ignoreRule, path string, isDir bool) bool {
	out := false
	for i := range rules {
		r := &rules[i]
		if (r.dirOnly && !isDir) || !strings.HasPrefix(path, r.base) {
			continue
		}
		rest := path[len(r.base):]
		var found bool
		if r.anchored {
			found = ignoreSegments(r.segments, strings.Split(rest, "/"))
		} else {
			found = ignoreName(r.segments[0], rest[strings.LastIndexByte(rest, '/')+1:])
		}
		if found {
			out = !r.negate
		}
	}
	return out
}

// Whether a pattern's segments match a path's: ** stands for any number of folders, and at the end for all that is inside
// (one thing at least). Worked a segment at a time, so that no .gitignore can make it take long.
func ignoreSegments(pattern, path []string) bool {
	can := make([]bool, len(path)+1)
	can[0] = true
	for i, p := range pattern {
		next := make([]bool, len(path)+1)
		switch {
		case p == "**":
			seen := false
			for j := range next {
				next[j] = seen || can[j] && i < len(pattern)-1
				seen = seen || can[j]
			}
		default:
			for j := 1; j < len(next); j++ {
				next[j] = can[j-1] && ignoreName(p, path[j-1])
			}
		}
		can = next
	}
	return can[len(path)]
}

// Whether one name matches a pattern's segment: * for any characters, ? for one, [a-z] for one of those, \ for the
// character after it as it is. Never goes back further than the last *, so that no pattern can make it take long.
func ignoreName(pattern, name string) bool {
	p, n := []rune(pattern), []rune(name)
	pi, ni, star, mark := 0, 0, -1, 0
	for ni < len(n) {
		if pi < len(p) && p[pi] == '*' {
			star, mark = pi, ni
			pi++
			continue
		}
		if pi < len(p) {
			if ok, next := ignoreChar(p, pi, n[ni]); ok {
				pi, ni = next, ni+1
				continue
			}
		}
		if star < 0 {
			return false
		}
		mark++
		pi, ni = star+1, mark
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

// Whether a character matches what the pattern holds at this place, and the place after that.
func ignoreChar(p []rune, at int, c rune) (bool, int) {
	switch p[at] {
	case '?':
		return true, at + 1
	case '\\':
		if at+1 < len(p) {
			return p[at+1] == c, at + 2
		}
	case '[':
		i := at + 1
		negate := i < len(p) && (p[i] == '!' || p[i] == '^')
		if negate {
			i++
		}
		found := false
		for first := true; i < len(p) && (first || p[i] != ']'); first = false {
			lo := p[i]
			if lo == '\\' && i+1 < len(p) {
				i++
				lo = p[i]
			}
			hi := lo
			if i+2 < len(p) && p[i+1] == '-' && p[i+2] != ']' {
				i += 2
				if hi = p[i]; hi == '\\' && i+1 < len(p) {
					i++
					hi = p[i]
				}
			}
			found = found || lo <= c && c <= hi
			i++
		}
		if i < len(p) {
			return found != negate, i + 1
		}
		// (no end to it: the [ is itself)
	}
	return p[at] == c, at + 1
}
