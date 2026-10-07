package tui

import (
	"context"
	"errors"
	"regexp"
	"strings"
)

// rowFilterHint tells the AI how a Data filter reads; the screen adds its columns and rows.
const rowFilterHint = "a filter of the rows shown, its terms ANDed: column=value, column!=value, column~text (a column's name in any case, or a unique part of it; * in a value globs), or plain words, each matching a cell fuzzily (letters in order, any case)"

var (
	rowTerm  = regexp.MustCompile(`^([A-Za-z_][\w.\-]*)(!=|=|~)(.*)$`)
	opSpaces = regexp.MustCompile(`\s*(!=|=|~)\s*`)
)

// rowMatcher reads a Data filter over rows of columns: `Destination=Pos status!=ok`, `*word*`, or
// fuzzy words. "Dest = Pos" reads as Dest=Pos; a term naming no column is a plain word.
func rowMatcher(filter string, columns []string) func(cells []string) bool {
	var conds []func([]string) bool
	for _, term := range splitTerms(opSpaces.ReplaceAllString(strings.TrimSpace(filter), "$1")) {
		if m := rowTerm.FindStringSubmatch(term); m != nil {
			if col := findColumn(columns, m[1]); col >= 0 {
				op, want := m[2], strings.Trim(m[3], `"`)
				is := valueMatcher(want)
				conds = append(conds, func(cells []string) bool {
					got := ""
					if col < len(cells) {
						got = stripMark(cells[col])
					}
					switch op {
					case "~":
						return strings.Contains(strings.ToLower(got), strings.ToLower(want))
					case "!=":
						return !is(got)
					}
					return is(got)
				})
				continue
			}
		}
		word := strings.Trim(term, `"`)
		hit := func(c string) bool { return fuzzy(c, word) }
		if strings.Contains(word, "*") {
			hit = valueMatcher(word)
		}
		conds = append(conds, func(cells []string) bool {
			for _, c := range cells {
				if hit(stripMark(c)) {
					return true
				}
			}
			return false
		})
	}
	return func(cells []string) bool {
		for _, c := range conds {
			if !c(cells) {
				return false
			}
		}
		return true
	}
}

// findColumn is the column named key in any case, else the only one holding it; -1 for none.
func findColumn(columns []string, key string) int {
	key = strings.ToLower(key)
	at := -1
	for i, c := range columns {
		c = strings.ToLower(c)
		if c == key {
			return i
		}
		if strings.Contains(c, key) {
			if at >= 0 {
				return -1
			}
			at = i
		}
	}
	return at
}

// valueMatcher compares a cell to want in any case; * in want matches any run of text.
func valueMatcher(want string) func(string) bool {
	if !strings.Contains(want, "*") {
		return func(got string) bool { return strings.EqualFold(strings.TrimSpace(got), want) }
	}
	re := regexp.MustCompile("(?is)^" + strings.ReplaceAll(regexp.QuoteMeta(want), `\*`, ".*") + "$")
	return re.MatchString
}

func stripMark(cell string) string { return strings.TrimPrefix(cell, "● ") }

// keepsSome accepts a filter that keeps at least one of rows: the check on what the AI writes.
func keepsSome(columns []string, rows [][]string) func(context.Context, string) error {
	return func(_ context.Context, v string) error {
		match := rowMatcher(v, columns)
		for _, r := range rows {
			if match(r) {
				return nil
			}
		}
		return errors.New("it keeps none of the rows")
	}
}
