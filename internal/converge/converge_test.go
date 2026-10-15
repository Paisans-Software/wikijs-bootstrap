package converge

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/wikijs-bootstrap/internal/config"
	"github.com/paisans-software/wikijs-bootstrap/internal/wiki"
)

type fakeWiki struct {
	setup      bool
	loginErr   error
	groups     map[int]wiki.Group
	next       int
	writes     []string
	strategies []wiki.Strategy
	site       wiki.Site
	onFinalize func()
	// setupClearsAt, when above zero, makes SetupMode answer false from its
	// nth call on, as a finalize that was still running does when it ends.
	setupClearsAt int
	setupCalls    int
}

func newWiki(setup bool) *fakeWiki {
	return &fakeWiki{setup: setup, next: 3, groups: map[int]wiki.Group{
		1: {ID: 1, Name: "Administrators", Permissions: []string{"manage:system"}},
		2: {ID: 2, Name: "Guests", RedirectOnLogin: "/", Permissions: []string{"read:pages"}, PageRules: []wiki.PageRule{{ID: "g", Match: "START", Roles: []string{"read:pages"}}}},
	}}
}

func (f *fakeWiki) Healthy(context.Context) error { return nil }
func (f *fakeWiki) SetupMode(context.Context) (bool, error) {
	f.setupCalls++
	if f.setupClearsAt > 0 && f.setupCalls >= f.setupClearsAt {
		return false, nil
	}
	return f.setup, nil
}
func (f *fakeWiki) Finalize(context.Context, wiki.Finalize) error {
	f.writes = append(f.writes, "finalize")
	f.setup = false
	if f.onFinalize != nil {
		f.onFinalize()
	}
	return nil
}
func (f *fakeWiki) Login(context.Context, string, string) (string, error) {
	return "tok", f.loginErr
}
func (f *fakeWiki) UpdateStrategies(_ context.Context, _ string, s []wiki.Strategy) error {
	f.writes = append(f.writes, "updateStrategies")
	f.strategies = s
	return nil
}
func (f *fakeWiki) UpdateSite(_ context.Context, _ string, s wiki.Site) error {
	f.writes = append(f.writes, "updateConfig")
	f.site = s
	return nil
}
func (f *fakeWiki) Groups(context.Context, string) ([]wiki.GroupSummary, error) {
	var out []wiki.GroupSummary
	for _, g := range f.groups {
		out = append(out, wiki.GroupSummary{ID: g.ID, Name: g.Name})
	}
	return out, nil
}

// Group answers like the real client: an id Wiki.js does not know is a zero
// Group and a nil error.
func (f *fakeWiki) Group(_ context.Context, _ string, id int) (wiki.Group, error) {
	return f.groups[id], nil
}
func (f *fakeWiki) CreateGroup(_ context.Context, _ string, name string) (int, error) {
	f.writes = append(f.writes, "create "+name)
	id := f.next
	f.next++
	f.groups[id] = wiki.Group{ID: id, Name: name, RedirectOnLogin: "/", Permissions: slices.Clone(wikiDefaultRoles), PageRules: []wiki.PageRule{wikiDefaultRule()}}
	return id, nil
}

// wikiDefaultRoles and wikiDefaultRule are what groups.create gives a new
// group: WIKI.data.groups.defaultPermissions and defaultPageRules
// (server/app/data.yml:143-158 at v2.5.315).
var wikiDefaultRoles = []string{"read:pages", "read:assets", "read:comments", "write:comments"}

func wikiDefaultRule() wiki.PageRule {
	return wiki.PageRule{ID: "default", Match: "START", Path: "", Roles: slices.Clone(wikiDefaultRoles), Locales: []string{}}
}
func (f *fakeWiki) UpdateGroup(_ context.Context, _ string, g wiki.Group) error {
	f.writes = append(f.writes, fmt.Sprintf("update %d", g.ID))
	f.groups[g.ID] = g
	return nil
}

type fakeHeld struct{ checkErr error }

func (h fakeHeld) Check(context.Context) error { return h.checkErr }
func (h fakeHeld) Release()                    {}

type fakeState struct {
	populated bool
	checkErr  error
}

func (s *fakeState) Lock(context.Context) (Held, error) { return fakeHeld{s.checkErr}, nil }
func (s *fakeState) SettingsPopulated(context.Context) (bool, error) {
	return s.populated, nil
}

type fakeStopper struct{ stopped int }

func (p *fakeStopper) StopApp() error { p.stopped++; return nil }

func cfg() config.Config {
	return config.Config{WikiURL: "http://app:3000", PublicURL: "https://wiki.example.org", AdminEmail: "wikijs-admin@example.org", AdminPassword: "not-real", ClientID: "cid", ClientSecret: "not-real", AuthorizeURL: "https://id.example.org/authorize", TokenURL: "https://id.example.org/api/oidc/token", UserinfoURL: "https://id.example.org/api/oidc/userinfo", LogoutURL: "https://id.example.org/api/oidc/end-session", AdminGroup: "admins", MemberGroup: "members", SyncedGroups: []string{"admins", "members"}, Title: "Example Wiki", UploadMaxBytes: 5 << 20}
}

func TestAFreshDatabaseIsSetUpThenConverged(t *testing.T) {
	w, s := newWiki(true), &fakeState{}
	w.onFinalize = func() { s.populated = true }
	if err := Run(context.Background(), cfg(), w, s, &fakeStopper{}, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	want := []string{"finalize", "updateStrategies", "updateConfig", "update 2", "create admins", "create members", "update 4", "update 3"}
	if !slices.Equal(w.writes, want) {
		t.Errorf("writes %v, want %v", w.writes, want)
	}
	if w.strategies[0].Key != "pocketid" || w.strategies[0].Order != 0 || w.strategies[1].Key != "local" {
		t.Errorf("strategies %+v", w.strategies)
	}
	if w.strategies[0].Config["scope"] != "openid profile email groups" || w.strategies[0].Config["mapGroups"] != true {
		t.Errorf("pocketid config %+v", w.strategies[0].Config)
	}
	if len(w.groups[2].Permissions) != 0 || len(w.groups[2].PageRules) != 0 {
		t.Errorf("guests %+v", w.groups[2])
	}
	if !w.site.AutoLogin || !w.site.HideLocal || !w.site.TrustProxy || w.site.Host != "https://wiki.example.org" {
		t.Errorf("site %+v", w.site)
	}
}

func TestLostLockStopsBeforeFinalize(t *testing.T) {
	w, s := newWiki(true), &fakeState{checkErr: errors.New("connection reset")}
	err := Run(context.Background(), cfg(), w, s, &fakeStopper{}, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "lost the setup lock") {
		t.Fatalf("err = %v", err)
	}
	if slices.Contains(w.writes, "finalize") || len(w.writes) != 0 {
		t.Errorf("wrote %v", w.writes)
	}
}

func TestSecondInstanceStopsItsStaleAppInsteadOfFinalizing(t *testing.T) {
	w, s, p := newWiki(true), &fakeState{populated: true}, &fakeStopper{}
	err := Run(context.Background(), cfg(), w, s, p, time.Millisecond)
	if !errors.Is(err, ErrStopped) || p.stopped != 1 {
		t.Fatalf("err %v, stopped %d", err, p.stopped)
	}
	if slices.Contains(w.writes, "finalize") {
		t.Error("finalized a database that had settings")
	}
}

func TestASetupStillRunningIsWaitedForNotStopped(t *testing.T) {
	w, s, p := newWiki(true), &fakeState{populated: true}, &fakeStopper{}
	w.setupClearsAt = 5
	if err := Run(context.Background(), cfg(), w, s, p, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if p.stopped != 0 {
		t.Errorf("stopped an install that was finishing, %d times", p.stopped)
	}
	if !slices.Contains(w.writes, "updateStrategies") {
		t.Errorf("did not go on to converge: %v", w.writes)
	}
}

func TestBuiltInGroupsAreNeverTheAdminGroup(t *testing.T) {
	for _, name := range []string{"Administrators", "Guests"} {
		w, s := newWiki(false), &fakeState{populated: true}
		c := cfg()
		c.AdminGroup, c.SyncedGroups = name, []string{name}
		err := Run(context.Background(), c, w, s, &fakeStopper{}, time.Millisecond)
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: err = %v", name, err)
		}
		if slices.Contains(w.writes, "update 1") || w.groups[1].Permissions[0] != "manage:system" {
			t.Errorf("%s: touched a built-in group: %v", name, w.writes)
		}
	}
}

func TestTwoGroupsWithTheAdminNameAreAnError(t *testing.T) {
	w, s := newWiki(false), &fakeState{populated: true}
	w.groups[3] = wiki.Group{ID: 3, Name: "admins"}
	w.groups[4] = wiki.Group{ID: 4, Name: "admins"}
	err := Run(context.Background(), cfg(), w, s, &fakeStopper{}, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "admins") {
		t.Fatalf("err = %v", err)
	}
	if slices.Contains(w.writes, "update 3") || slices.Contains(w.writes, "update 4") {
		t.Errorf("wrote %v", w.writes)
	}
}

func TestAnEmptiedDatabaseUnderARunningWikiIsRefused(t *testing.T) {
	w, s := newWiki(false), &fakeState{populated: false}
	if err := Run(context.Background(), cfg(), w, s, &fakeStopper{}, time.Millisecond); err == nil {
		t.Fatal("went on against a database with no settings and a wiki not in setup mode")
	}
	if len(w.writes) != 0 {
		t.Errorf("wrote %v", w.writes)
	}
}

func TestSyncedGroupsAreCreatedOnceAndAdminsGetPermissions(t *testing.T) {
	w, s := newWiki(false), &fakeState{populated: true}
	for i := 0; i < 2; i++ {
		if err := Run(context.Background(), cfg(), w, s, &fakeStopper{}, time.Millisecond); err != nil {
			t.Fatal(err)
		}
	}
	creates := 0
	for _, x := range w.writes {
		if strings.HasPrefix(x, "create ") {
			creates++
		}
	}
	if creates != 2 {
		t.Errorf("created %d groups over two runs: %v", creates, w.writes)
	}
	var admins wiki.Group
	for _, g := range w.groups {
		if g.Name == "admins" {
			admins = g
		}
	}
	if slices.Contains(admins.Permissions, "manage:system") || slices.Contains(admins.Permissions, "manage:api") || !slices.Contains(admins.Permissions, "manage:groups") {
		t.Errorf("admins permissions %v", admins.Permissions)
	}
}

func adminRule(t *testing.T, g wiki.Group) wiki.PageRule {
	t.Helper()
	var found []wiki.PageRule
	for _, r := range g.PageRules {
		if r.ID == "paisans-admin" {
			found = append(found, r)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want one paisans-admin rule, got %+v", g.PageRules)
	}
	return found[0]
}

func checkAdminRule(t *testing.T, r wiki.PageRule) {
	t.Helper()
	want := []string{
		"read:pages", "write:pages", "manage:pages", "delete:pages",
		"read:source", "read:history",
		"read:assets", "write:assets", "manage:assets",
		"write:scripts", "write:styles",
		"read:comments", "write:comments", "manage:comments",
	}
	if r.Deny || r.Match != "START" || r.Path != "" || r.Locales == nil || len(r.Locales) != 0 {
		t.Errorf("admin rule %+v", r)
	}
	for _, role := range r.Roles {
		if !slices.Contains(AdminPermissions, role) {
			t.Errorf("admin rule grants %s, which the admin group's permissions lack", role)
		}
	}
	got, exp := slices.Clone(r.Roles), slices.Clone(want)
	slices.Sort(got)
	slices.Sort(exp)
	if !slices.Equal(got, exp) {
		t.Errorf("admin rule roles %v, want %v", r.Roles, want)
	}
}

func TestANewAdminGroupCanWritePagesEverywhere(t *testing.T) {
	w, s := newWiki(false), &fakeState{populated: true}
	for i := 0; i < 2; i++ {
		if err := Run(context.Background(), cfg(), w, s, &fakeStopper{}, time.Millisecond); err != nil {
			t.Fatal(err)
		}
		admins := w.groups[3]
		if admins.Name != "admins" {
			t.Fatalf("group 3 is %q", admins.Name)
		}
		if len(admins.PageRules) != 2 || !reflect.DeepEqual(admins.PageRules[0], wikiDefaultRule()) {
			t.Errorf("run %d: page rules %+v", i+1, admins.PageRules)
		}
		checkAdminRule(t, adminRule(t, admins))
	}
	if len(w.groups[4].PageRules) != 1 || w.groups[4].PageRules[0].ID != "paisans-members" {
		t.Errorf("members page rules %+v", w.groups[4].PageRules)
	}
}

func sorted(s []string) []string {
	s = slices.Clone(s)
	slices.Sort(s)
	return s
}

func TestTheMemberGroupIsSeededInTheRunThatCreatesIt(t *testing.T) {
	w, s := newWiki(false), &fakeState{populated: true}
	for i := 0; i < 2; i++ {
		if err := Run(context.Background(), cfg(), w, s, &fakeStopper{}, time.Millisecond); err != nil {
			t.Fatal(err)
		}
	}
	roles := []string{"read:pages", "write:pages", "read:source", "read:history", "read:assets", "write:assets", "read:comments", "write:comments"}
	members := w.groups[4]
	if members.Name != "members" || !slices.Equal(sorted(members.Permissions), sorted(roles)) {
		t.Errorf("members %+v", members)
	}
	want := wiki.PageRule{ID: "paisans-members", Match: "REGEX", Path: "^(?!admins(/|$))", Locales: []string{}}
	if len(members.PageRules) != 1 {
		t.Fatalf("members page rules %+v", members.PageRules)
	}
	got := members.PageRules[0]
	if !slices.Equal(sorted(got.Roles), sorted(roles)) {
		t.Errorf("member rule roles %v", got.Roles)
	}
	got.Roles = nil
	if !reflect.DeepEqual(got, want) {
		t.Errorf("member rule %+v, want %+v", got, want)
	}
	updates := 0
	for _, x := range w.writes {
		if x == "update 4" {
			updates++
		}
	}
	if updates != 1 {
		t.Errorf("updated the member group %d times over two runs: %v", updates, w.writes)
	}
}

func TestGuestsReadOnlyTheirPaths(t *testing.T) {
	w, s := newWiki(false), &fakeState{populated: true}
	c := cfg()
	c.GuestPaths = []string{"public", "docs/howto"}
	if err := Run(context.Background(), c, w, s, &fakeStopper{}, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	g := w.groups[2]
	three := []string{"read:pages", "read:assets", "read:comments"}
	if !slices.Equal(sorted(g.Permissions), sorted(three)) {
		t.Errorf("guests permissions %v", g.Permissions)
	}
	want := []wiki.PageRule{
		{ID: "paisans-guests-1", Match: "START", Path: "public", Roles: three, Locales: []string{}},
		{ID: "paisans-guests-2", Match: "START", Path: "docs/howto", Roles: three, Locales: []string{}},
	}
	if !reflect.DeepEqual(g.PageRules, want) {
		t.Errorf("guests rules %+v", g.PageRules)
	}
}

func TestAdminGroupKeepsOtherPageRulesAndRewritesItsOwn(t *testing.T) {
	w, s := newWiki(false), &fakeState{populated: true}
	human := wiki.PageRule{ID: "a", Deny: true, Match: "EXACT", Path: "secret", Roles: []string{"read:pages"}, Locales: []string{"en"}}
	stale := wiki.PageRule{ID: "paisans-admin", Deny: true, Match: "EXACT", Path: "x", Roles: []string{"read:pages"}, Locales: []string{"fr"}}
	w.groups[3] = wiki.Group{ID: 3, Name: "admins", RedirectOnLogin: "/", PageRules: []wiki.PageRule{human, stale, wikiDefaultRule()}}
	w.next = 4
	if err := Run(context.Background(), cfg(), w, s, &fakeStopper{}, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	rules := w.groups[3].PageRules
	if len(rules) != 3 || !reflect.DeepEqual(rules[0], human) || rules[1].ID != "paisans-admin" || !reflect.DeepEqual(rules[2], wikiDefaultRule()) {
		t.Fatalf("page rules %+v", rules)
	}
	checkAdminRule(t, rules[1])
}

func TestOtherGroupsAreNeverEdited(t *testing.T) {
	w, s := newWiki(false), &fakeState{populated: true}
	w.groups[3] = wiki.Group{ID: 3, Name: "members", Permissions: []string{"read:pages", "write:pages"}}
	w.next = 4
	if err := Run(context.Background(), cfg(), w, s, &fakeStopper{}, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(w.writes, "update 3") {
		t.Errorf("edited members: %v", w.writes)
	}
}

func TestAnAdminGroupNotNamedInSyncedGroupsIsAnError(t *testing.T) {
	w, s := newWiki(false), &fakeState{populated: true}
	c := cfg()
	c.AdminGroup = "ops" // never created: not in SyncedGroups
	err := Run(context.Background(), c, w, s, &fakeStopper{}, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "ops") {
		t.Fatalf("err = %v", err)
	}
	if slices.Contains(w.writes, "update 0") {
		t.Errorf("updated group 0: %v", w.writes)
	}
}

func TestLoginFailureIsReportedNotRetriedSilently(t *testing.T) {
	w, s := newWiki(false), &fakeState{populated: true}
	w.loginErr = fmt.Errorf("%w: bad password", wiki.ErrLogin)
	err := Run(context.Background(), cfg(), w, s, &fakeStopper{}, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "wikijs-admin@example.org") || !strings.Contains(err.Error(), "break glass") {
		t.Errorf("err = %v", err)
	}
}
