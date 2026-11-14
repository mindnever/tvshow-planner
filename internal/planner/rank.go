// Package planner answers "given who is present, what can we watch?".
package planner

import (
	"cmp"
	"slices"
	"strings"
)

type Tier int

const (
	// Exact: the present people are exactly the show's watchers.
	Exact Tier = iota + 1
	// AllPresent: every watcher is here, plus people who don't follow the show.
	AllPresent
	// Partial: some watchers are here, some are missing.
	Partial
)

type Show struct {
	ID       int64
	Title    string
	Watchers []int64 // person IDs
	// Finished means progress is known and nothing is left to watch.
	Finished     bool
	LastPlayedAt int64 // unix seconds, 0 if unknown
}

type Result struct {
	Show    Show
	Tier    Tier
	Missing []int64 // watchers who are not present
	Extra   []int64 // present people who don't watch this show
}

// Rank returns the shows at least one present person watches, best match
// first. Shows without watchers, or with no watcher present, are left out.
func Rank(present []int64, shows []Show) []Result {
	var out []Result
	for _, sh := range shows {
		r := Result{Show: sh}
		anyPresent := false
		for _, w := range sh.Watchers {
			if slices.Contains(present, w) {
				anyPresent = true
			} else {
				r.Missing = append(r.Missing, w)
			}
		}
		if !anyPresent {
			continue
		}
		for _, p := range present {
			if !slices.Contains(sh.Watchers, p) {
				r.Extra = append(r.Extra, p)
			}
		}
		switch {
		case len(r.Missing) > 0:
			r.Tier = Partial
		case len(r.Extra) > 0:
			r.Tier = AllPresent
		default:
			r.Tier = Exact
		}
		out = append(out, r)
	}
	slices.SortStableFunc(out, func(a, b Result) int {
		return cmp.Or(
			cmp.Compare(a.Tier, b.Tier),
			cmp.Compare(b2i(a.Show.Finished), b2i(b.Show.Finished)),
			cmp.Compare(len(a.Missing), len(b.Missing)),
			cmp.Compare(len(a.Extra), len(b.Extra)),
			cmp.Compare(b.Show.LastPlayedAt, a.Show.LastPlayedAt),
			cmp.Compare(strings.ToLower(a.Show.Title), strings.ToLower(b.Show.Title)),
		)
	})
	return out
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
