package converge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/wikijs-bootstrap/internal/wiki"
)

// The fake reads back what the run wrote, as Wiki.js does.
func (f *fakeWiki) ActiveStrategies(context.Context, string) ([]wiki.Strategy, error) {
	out := make([]wiki.Strategy, 0, len(f.strategies))
	for _, s := range f.strategies {
		cfg := map[string]any{}
		for k, v := range s.Config {
			cfg[k] = v
		}
		s.Config = cfg
		out = append(out, s)
	}
	return out, nil
}
func (f *fakeWiki) SiteConfig(context.Context, string) (wiki.Site, error) { return f.site, nil }

// converged is a wiki one run has converged, with GUEST_PATHS set.
func converged(t *testing.T) (*fakeWiki, func() []string) {
	t.Helper()
	w, s := newWiki(false), &fakeState{populated: true}
	c := cfg()
	c.GuestPaths = []string{"public"}
	if err := Run(context.Background(), c, w, s, &fakeStopper{}, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	return w, func() []string {
		t.Helper()
		before := len(w.writes)
		d, err := Drift(context.Background(), c, w)
		if err != nil {
			t.Fatal(err)
		}
		if len(w.writes) != before {
			t.Errorf("a poll wrote %v", w.writes[before:])
		}
		return d
	}
}

func one(t *testing.T, d []string, want ...string) {
	t.Helper()
	if len(d) != 1 {
		t.Fatalf("differences %q, want one", d)
	}
	for _, w := range want {
		if !strings.Contains(d[0], w) {
			t.Errorf("difference %q does not say %q", d[0], w)
		}
	}
}

func TestAConvergedWikiHasNoDrift(t *testing.T) {
	_, drift := converged(t)
	if d := drift(); len(d) != 0 {
		t.Errorf("differences %q", d)
	}
}

func TestDriftComparesMeaningNotBytes(t *testing.T) {
	w, drift := converged(t)
	g := w.groups[2]
	slices.Reverse(g.Permissions)
	g.PageRules[0].Roles = []string{"read:comments", "read:pages", "read:assets"}
	g.PageRules[0].Locales = nil
	w.groups[2] = g
	admins := w.groups[3]
	admins.Permissions = slices.Clone(admins.Permissions)
	slices.Reverse(admins.Permissions)
	w.groups[3] = admins
	w.site.Host += "/"
	w.strategies[0].Config["authorizationURL"] = w.strategies[0].Config["authorizationURL"].(string) + "/"
	if d := drift(); len(d) != 0 {
		t.Errorf("differences %q", d)
	}
}

func TestGuestsDrift(t *testing.T) {
	w, drift := converged(t)
	g := w.groups[2]
	g.Permissions = append(slices.Clone(g.Permissions), "write:pages")
	w.groups[2] = g
	one(t, drift(), "Guests permissions", "write:pages", "configuration says")

	w, drift = converged(t)
	g = w.groups[2]
	g.PageRules = append(g.PageRules, wiki.PageRule{ID: "x1", Match: "START", Path: "", Roles: []string{"read:pages"}})
	w.groups[2] = g
	one(t, drift(), "Guests page rule x1", "configuration says none")

	w, drift = converged(t)
	g = w.groups[2]
	g.PageRules = nil
	w.groups[2] = g
	one(t, drift(), "Guests page rule paisans-guests-1", "Wiki.js has none", `"public"`)

	w, drift = converged(t)
	g = w.groups[2]
	g.PageRules[0].Path = "pub"
	w.groups[2] = g
	one(t, drift(), "Guests page rule paisans-guests-1", `"pub"`, `"public"`)
}

func TestAdminGroupDrift(t *testing.T) {
	w, drift := converged(t)
	a := w.groups[3]
	a.Permissions = slices.DeleteFunc(slices.Clone(a.Permissions), func(p string) bool { return p == "manage:pages" })
	w.groups[3] = a
	one(t, drift(), "admin group admins permissions", "manage:pages")

	w, drift = converged(t)
	a = w.groups[3]
	a.PageRules = append(slices.Clone(a.PageRules), wiki.PageRule{ID: "theirs", Deny: true, Match: "START", Path: "x", Roles: []string{"read:pages"}})
	w.groups[3] = a
	if d := drift(); len(d) != 0 {
		t.Errorf("another rule on the admin group is the admins': %q", d)
	}

	w, drift = converged(t)
	a = w.groups[3]
	a.PageRules = slices.Clone(a.PageRules)
	for i := range a.PageRules {
		if a.PageRules[i].ID == "paisans-admin" {
			a.PageRules[i].Deny = true
		}
	}
	w.groups[3] = a
	one(t, drift(), "admin group admins page rule paisans-admin", "deny")

	w, drift = converged(t)
	delete(w.groups, 3)
	one(t, drift(), "admin group admins", "Wiki.js has none")
}

func TestTheMemberGroupAndOtherGroupsAreNotCompared(t *testing.T) {
	w, drift := converged(t)
	w.groups[4] = wiki.Group{ID: 4, Name: "members", Permissions: []string{"manage:pages"}}
	w.groups[9] = wiki.Group{ID: 9, Name: "editors", Permissions: []string{"write:styles"}}
	if d := drift(); len(d) != 0 {
		t.Errorf("differences %q", d)
	}
}

func TestStrategyDrift(t *testing.T) {
	w, drift := converged(t)
	w.strategies[1].Enabled = false
	one(t, drift(), "strategy local enabled", "false", "true")

	w, drift = converged(t)
	w.strategies[0].Order, w.strategies[1].Order = 1, 0
	if d := drift(); len(d) != 2 || !strings.Contains(d[0], "order") {
		t.Errorf("differences %q", d)
	}

	w, drift = converged(t)
	w.strategies[0].Config["tokenURL"] = "https://elsewhere.example.org/token"
	one(t, drift(), "strategy pocketid tokenURL", "elsewhere.example.org", "https://id.example.org/api/oidc/token")

	w, drift = converged(t)
	delete(w.strategies[0].Config, "scope")
	one(t, drift(), "strategy pocketid scope", "Wiki.js has none")

	w, drift = converged(t)
	w.strategies = append(w.strategies, wiki.Strategy{Key: "github", StrategyKey: "github", Enabled: true, Order: 2})
	one(t, drift(), "strategy github", "configuration says none")

	w, drift = converged(t)
	w.strategies = w.strategies[1:]
	one(t, drift(), "strategy pocketid", "Wiki.js has none")
}

func TestTheClientSecretIsNeverPrinted(t *testing.T) {
	w, drift := converged(t)
	const theirs = "a-secret-someone-typed-in"
	w.strategies[0].Config["clientSecret"] = theirs
	d := drift()
	one(t, d, "strategy pocketid", "client secret differs")
	if strings.Contains(d[0], theirs) || strings.Contains(d[0], cfg().ClientSecret) {
		t.Errorf("the secret was printed: %q", d[0])
	}
	delete(w.strategies[0].Config, "clientSecret")
	one(t, drift(), "client secret differs")
}

func TestSiteDrift(t *testing.T) {
	w, drift := converged(t)
	w.site.Title = "Someone Else's Wiki"
	one(t, drift(), "site title", `"Someone Else's Wiki"`, `"Example Wiki"`)

	w, drift = converged(t)
	w.site.UploadMaxFileSize = 1
	w.site.HideLocal = false
	if d := drift(); len(d) != 2 {
		t.Errorf("differences %q", d)
	}
}

func TestAFailedPollIsAnErrorNotDrift(t *testing.T) {
	w, _ := converged(t)
	w.loginErr = fmt.Errorf("%w: bad password", wiki.ErrLogin)
	d, err := Drift(context.Background(), cfg(), w)
	if err == nil || !strings.Contains(err.Error(), "break glass") || d != nil {
		t.Errorf("%q, %v", d, err)
	}
	w.loginErr = errors.New(`Post "http://app:3000/graphql": dial tcp: lookup app: no such host`)
	d, err = Drift(context.Background(), cfg(), w)
	if err == nil || strings.Contains(err.Error(), "break glass") || !strings.Contains(err.Error(), "no such host") || d != nil {
		t.Errorf("an unreachable Wiki.js reads as a refused login: %q, %v", d, err)
	}
}
