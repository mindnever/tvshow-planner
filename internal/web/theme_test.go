package web

import (
	"net/url"
	"strings"
	"testing"
)

func TestThemeIsPerAccount(t *testing.T) {
	app := newTestApp(t)
	alice := app.client("alice")
	app.post(alice, "/groups", url.Values{"name": {"Family"}})

	// By default the page follows the device, and the button offers light.
	page := app.get(alice, "/g/1/people")
	mustContain(t, page, `<html lang="en">`, `name="theme" value="light"`, `name="next" value="/g/1/people"`, "Auto")

	// Picking dark comes back to the same page.
	page = app.post(alice, "/theme", url.Values{"theme": {"dark"}, "next": {"/g/1/people"}})
	mustContain(t, page, `<html lang="en" data-theme="dark">`, `name="theme" value="auto"`, "Dark", "<h1>People</h1>")

	// It belongs to the account: another browser of alice's has it, bob doesn't.
	mustContain(t, app.get(app.client("alice"), "/"), `data-theme="dark"`)
	bob := app.client("bob")
	mustNotContain(t, app.get(bob, "/"), "data-theme")

	// Round the circle: dark -> auto -> light.
	mustNotContain(t, app.post(alice, "/theme", url.Values{"theme": {"auto"}, "next": {"/"}}), "data-theme")
	mustContain(t, app.post(alice, "/theme", url.Values{"theme": {"light"}, "next": {"/"}}), `data-theme="light"`, `name="theme" value="dark"`)

	// Anything else means auto, and "next" can't lead off the site.
	page = app.post(alice, "/theme", url.Values{"theme": {"neon"}, "next": {"//evil.example"}})
	mustNotContain(t, page, "data-theme")
	if !strings.Contains(page, "Your groups") && !strings.Contains(page, "Family") {
		t.Errorf("did not land on the home page:\n%s", page)
	}

	// The login page has no account to ask.
	mustNotContain(t, app.get(newClient(), "/login"), "data-theme", `action="/theme"`)
}
