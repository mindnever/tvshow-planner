package planner

import (
	"slices"
	"testing"
)

const (
	ana int64 = iota + 1
	bo
	cy
	di
)

func titles(rs []Result) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Show.Title)
	}
	return out
}

func TestRankTiers(t *testing.T) {
	shows := []Show{
		{ID: 1, Title: "Partial", Watchers: []int64{ana, bo, di}},
		{ID: 2, Title: "Extras", Watchers: []int64{ana}},
		{ID: 3, Title: "Exact", Watchers: []int64{bo, ana}},
		{ID: 4, Title: "NobodyHere", Watchers: []int64{cy, di}},
		{ID: 5, Title: "NoWatchers"},
	}
	got := Rank([]int64{ana, bo}, shows)

	if want := []string{"Exact", "Extras", "Partial"}; !slices.Equal(titles(got), want) {
		t.Fatalf("order = %v, want %v", titles(got), want)
	}
	if got[0].Tier != Exact || len(got[0].Missing)+len(got[0].Extra) != 0 {
		t.Errorf("exact: %+v", got[0])
	}
	if got[1].Tier != AllPresent || !slices.Equal(got[1].Extra, []int64{bo}) {
		t.Errorf("extras: %+v", got[1])
	}
	if got[2].Tier != Partial || !slices.Equal(got[2].Missing, []int64{di}) || len(got[2].Extra) != 0 {
		t.Errorf("partial: %+v", got[2])
	}
}

func TestRankNobodyPresent(t *testing.T) {
	if got := Rank(nil, []Show{{ID: 1, Title: "A", Watchers: []int64{ana}}}); len(got) != 0 {
		t.Fatalf("got %v, want nothing", titles(got))
	}
}

func TestRankTieBreaks(t *testing.T) {
	all := []int64{ana, bo}
	shows := []Show{
		{ID: 1, Title: "Finished", Watchers: all, Finished: true, LastPlayedAt: 900},
		{ID: 2, Title: "Older", Watchers: all, LastPlayedAt: 100},
		{ID: 3, Title: "Recent", Watchers: all, LastPlayedAt: 500},
		{ID: 4, Title: "beta", Watchers: all},
		{ID: 5, Title: "Alpha", Watchers: all},
		// Tier wins over everything else: finished exact beats unfinished partial.
		{ID: 6, Title: "MissingTwo", Watchers: []int64{ana, cy, di}, LastPlayedAt: 999},
		{ID: 7, Title: "MissingOne", Watchers: []int64{ana, bo, cy}},
	}
	want := []string{"Recent", "Older", "Alpha", "beta", "Finished", "MissingOne", "MissingTwo"}
	if got := titles(Rank(all, shows)); !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestRankFewerExtrasFirst(t *testing.T) {
	shows := []Show{
		{ID: 1, Title: "TwoExtras", Watchers: []int64{ana}},
		{ID: 2, Title: "OneExtra", Watchers: []int64{ana, bo}},
	}
	want := []string{"OneExtra", "TwoExtras"}
	if got := titles(Rank([]int64{ana, bo, cy}, shows)); !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}
