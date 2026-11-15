package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// ticked reports which people are ticked for a show on the Shows page.
func ticked(page string, show string) string {
	var out []string
	for _, id := range []string{"1", "2", "3"} {
		if strings.Contains(page, `name="w-`+show+`" value="`+id+`" form="pagesave" checked`) {
			out = append(out, id)
		}
	}
	return strings.Join(out, ",")
}

func TestSaveAllWatchersAtOnce(t *testing.T) {
	app := newTestApp(t)
	alice := app.client("alice")
	app.post(alice, "/groups", url.Values{"name": {"Family"}})
	for _, n := range []string{"Ana", "Bo", "Cy"} {
		app.post(alice, "/g/1/people", url.Values{"name": {n}})
	}
	for _, title := range []string{"One", "Two", "Three"} { // shows 1, 2, 3
		app.post(alice, "/g/1/shows", url.Values{"title": {title}})
	}
	app.post(alice, "/g/1/shows/2/watchers", url.Values{"person": {"1", "2"}})

	page := app.get(alice, "/g/1/shows")
	mustContain(t, page, `id="pagesave"`, `action="/g/1/shows/watchers"`, "Save changes", "Discard",
		`name="was-1" value=""`, `name="was-2" value="1,2"`)
	mustNotContain(t, page, "Save watchers")

	// Someone else changes show 3 after the page was loaded.
	app.post(alice, "/g/1/shows/3/watchers", url.Values{"person": {"3"}})

	// The page is saved: show 1 gains two watchers, show 2 loses everyone,
	// show 3 is sent back as it was loaded (nobody).
	page = app.post(alice, "/g/1/shows/watchers", url.Values{
		"was-1": {""}, "w-1": {"3", "1"},
		"was-2": {"1,2"},
		"was-3": {""},
	})
	if a, b, c := ticked(page, "1"), ticked(page, "2"), ticked(page, "3"); a != "1,3" || b != "" || c != "3" {
		t.Errorf("after save: show 1 = %q, show 2 = %q, show 3 = %q; want \"1,3\", \"\", \"3\"", a, b, c)
	}

	// People and shows of another group can't be touched.
	bob := app.client("bob")
	app.post(bob, "/groups", url.Values{"name": {"Work"}})
	app.post(bob, "/g/2/people", url.Values{"name": {"Dee"}})  // person 4
	app.post(bob, "/g/2/shows", url.Values{"title": {"Four"}}) // show 4
	app.post(bob, "/g/2/shows/watchers", url.Values{"was-1": {"1,3"}, "w-1": {"4"}, "was-4": {""}, "w-4": {"1", "4"}})
	if got := ticked(app.get(alice, "/g/1/shows"), "1"); got != "1,3" {
		t.Errorf("another group's save changed show 1 to %q", got)
	}
	mustContain(t, app.get(bob, "/g/2/shows"), `name="was-4" value="4"`)
	if code, _ := app.do(bob, "POST", "/g/1/shows/watchers", url.Values{"was-1": {""}}); code != http.StatusForbidden {
		t.Errorf("saving someone else's group = %d", code)
	}
}

func TestSavePeopleAtOnce(t *testing.T) {
	app := newTestApp(t)
	alice := app.client("alice")
	app.post(alice, "/groups", url.Values{"name": {"Family"}})
	for _, n := range []string{"Ana", "Bo", "Cy"} { // people 1, 2, 3
		app.post(alice, "/g/1/people", url.Values{"name": {n}})
	}
	app.post(alice, "/g/1/shows", url.Values{"title": {"One"}})
	app.post(alice, "/g/1/shows/1/watchers", url.Values{"person": {"1", "2", "3"}})

	page := app.get(alice, "/g/1/people")
	mustContain(t, page, `action="/g/1/people/save"`, `type="text" name="name-1" value="Ana"`, `name="remove" value="2"`, "Save changes")
	mustNotContain(t, page, "Rename")

	// One save: Ana is renamed, Bo removed, Cy's emptied name is ignored.
	page = app.post(alice, "/g/1/people/save", url.Values{
		"name-1": {"  Anastasia "}, "name-2": {"Bobby"}, "name-3": {" "}, "remove": {"2"},
	})
	mustContain(t, page, `name="name-1" value="Anastasia"`, `name="name-3" value="Cy"`)
	mustNotContain(t, page, "Bo", `name="name-2"`)
	// Bo is gone from the show as well.
	shows := app.get(alice, "/g/1/shows")
	mustContain(t, shows, `name="was-1" value="1,3"`, "Anastasia")
	mustNotContain(t, shows, "Bo")

	// No two people with the same name, whatever the case.
	code, page := app.do(alice, "POST", "/g/1/people", url.Values{"name": {" anastasia"}})
	if code != http.StatusConflict || !strings.Contains(page, "already someone called Anastasia") {
		t.Errorf("adding a duplicate = %d\n%s", code, page)
	}
	code, page = app.do(alice, "POST", "/g/1/people/save", url.Values{"name-1": {"Ann"}, "name-3": {"ANN"}})
	if code != http.StatusConflict || !strings.Contains(page, "both be called") || !strings.Contains(page, `value="Anastasia"`) {
		t.Errorf("renaming into a duplicate = %d\n%s", code, page)
	}
	if n := strings.Count(app.get(alice, "/g/1/people"), `name="remove"`); n != 2 {
		t.Errorf("people after refused changes = %d, want 2", n)
	}
	// Changing only the case of a name, swapping two names, and taking the
	// name of someone removed in the same save are all fine.
	app.post(alice, "/g/1/people", url.Values{"name": {"Dee"}}) // person 4
	page = app.post(alice, "/g/1/people/save", url.Values{"name-1": {"cy"}, "name-3": {"ANASTASIA"}})
	mustContain(t, page, `name="name-1" value="cy"`, `name="name-3" value="ANASTASIA"`)
	page = app.post(alice, "/g/1/people/save", url.Values{"name-1": {"Dee"}, "remove": {"4"}})
	mustContain(t, page, `name="name-1" value="Dee"`)

	// The old one-at-a-time routes are gone, and other groups are off limits.
	if code, _ := app.do(alice, "POST", "/g/1/people/1/delete", url.Values{}); code != http.StatusNotFound && code != http.StatusMethodNotAllowed {
		t.Errorf("old delete route = %d", code)
	}
	bob := app.client("bob")
	app.post(bob, "/groups", url.Values{"name": {"Work"}})
	app.post(bob, "/g/2/people/save", url.Values{"name-1": {"Hacked"}, "remove": {"3"}})
	mustContain(t, app.get(alice, "/g/1/people"), `name="name-1" value="Dee"`, `name="name-3" value="ANASTASIA"`)
	if code, _ := app.do(bob, "POST", "/g/1/people/save", url.Values{"remove": {"1"}}); code != http.StatusForbidden {
		t.Errorf("saving someone else's people = %d", code)
	}
}
