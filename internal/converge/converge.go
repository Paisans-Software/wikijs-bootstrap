// Package converge is one bootstrap run: set Wiki.js up if this database
// never was, then bring its sign-in, site settings and groups to what the
// configuration says. Its writes are exactly those the spec names.
package converge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/paisans-software/wikijs-bootstrap/internal/config"
	"github.com/paisans-software/wikijs-bootstrap/internal/wiki"
)

type Wiki interface {
	Healthy(ctx context.Context) error
	SetupMode(ctx context.Context) (bool, error)
	Finalize(ctx context.Context, in wiki.Finalize) error
	Login(ctx context.Context, email, password string) (string, error)
	UpdateStrategies(ctx context.Context, token string, s []wiki.Strategy) error
	UpdateSite(ctx context.Context, token string, s wiki.Site) error
	Groups(ctx context.Context, token string) ([]wiki.GroupSummary, error)
	Group(ctx context.Context, token string, id int) (wiki.Group, error)
	CreateGroup(ctx context.Context, token, name string) (int, error)
	UpdateGroup(ctx context.Context, token string, g wiki.Group) error
}

// Held is a taken setup lock. Check reports whether it is still held.
type Held interface {
	Check(ctx context.Context) error
	Release()
}

type State interface {
	Lock(ctx context.Context) (Held, error)
	SettingsPopulated(ctx context.Context) (bool, error)
}

type Stopper interface{ StopApp() error }

var ErrStopped = errors.New("stopped the stale instance; it restarts and so does this container")

// guestsID is Wiki.js's built in Guests group (server/setup.js:246).
const guestsID = 2

// AdminPermissions is every Wiki.js 2.5 permission except manage:system and
// manage:api. manage:api lets its holder make a full access API key, which
// acts as Administrators (server/models/apiKeys.js), so it is manage:system
// by another route.
var AdminPermissions = []string{
	"read:pages", "write:pages", "manage:pages", "delete:pages",
	"write:styles", "write:scripts", "read:source", "read:history",
	"read:assets", "write:assets", "manage:assets",
	"read:comments", "write:comments", "manage:comments",
	"write:users", "manage:users", "write:groups", "manage:groups",
	"manage:navigation", "manage:theme",
}

// adminRuleID names the page rule the bootstrap owns on the admin group.
const adminRuleID = "paisans-admin"

// adminPageRoles is every role Wiki.js's page rule editor offers
// (client/components/admin/admin-groups-edit-rules.vue:215-229 at v2.5.315),
// all of which are in AdminPermissions.
var adminPageRoles = []string{
	"read:pages", "write:pages", "manage:pages", "delete:pages",
	"read:source", "read:history",
	"read:assets", "write:assets", "manage:assets",
	"write:scripts", "write:styles",
	"read:comments", "write:comments", "manage:comments",
}

// memberRoles is what a member group is seeded with, as permissions and as
// the roles of its one page rule: read, edit and comment on pages and
// upload assets. Moving, renaming and deleting pages stay with admins, since
// an edit is undone from a page's history and those are not; write:scripts
// and write:styles stay with admins, since a member's script would run in
// an admin's session.
var memberRoles = []string{
	"read:pages", "write:pages", "read:source", "read:history",
	"read:assets", "write:assets", "read:comments", "write:comments",
}

// memberRule grants memberRoles on every path but admins/ and what is under
// it. Wiki.js tests a REGEX rule against the page path without its leading
// slash or locale (server/core/auth.js:264-268 at v2.5.315). On admins/ no
// member rule matches, so nothing grants a member anything there; the
// admin group's START "" rule does grant an admin. Wiki.js refuses a rule
// its safe-regex check judges exponential (server/graph/resolvers/group.js:
// 167-172); this one has no repetition.
var memberRule = wiki.PageRule{ID: "paisans-members", Match: "REGEX", Path: "^(?!admins(/|$))", Roles: memberRoles, Locales: []string{}}

// guestRoles is what Guests gets on each of GUEST_PATHS, and nowhere else.
var guestRoles = []string{"read:pages", "read:assets", "read:comments"}

// guests is the Guests group's permissions and page rules: none without
// GUEST_PATHS, and with them one read-only START rule for each.
func guests(c config.Config) ([]string, []wiki.PageRule) {
	if len(c.GuestPaths) == 0 {
		return []string{}, []wiki.PageRule{}
	}
	rules := make([]wiki.PageRule, 0, len(c.GuestPaths))
	for i, p := range c.GuestPaths {
		rules = append(rules, wiki.PageRule{ID: fmt.Sprintf("paisans-guests-%d", i+1), Match: "START", Path: p, Roles: slices.Clone(guestRoles), Locales: []string{}})
	}
	return slices.Clone(guestRoles), rules
}

func Run(ctx context.Context, c config.Config, w Wiki, s State, p Stopper, wait time.Duration) error {
	if err := until(ctx, wait, func() error { return w.Healthy(ctx) }); err != nil {
		return fmt.Errorf("Wiki.js never answered /healthz: %w", err)
	}
	held, err := s.Lock(ctx)
	if err != nil {
		return err
	}
	defer held.Release()

	populated, err := s.SettingsPopulated(ctx)
	if err != nil {
		return fmt.Errorf("reading the settings table: %w", err)
	}
	setup, err := w.SetupMode(ctx)
	if err != nil {
		return err
	}
	switch {
	case !populated && setup:
		// Finalize deletes every user and group, so be sure the lock that
		// made this decision safe is still ours.
		if err := held.Check(ctx); err != nil {
			return fmt.Errorf("lost the setup lock before setup; nothing was changed: %w", err)
		}
		if err := w.Finalize(ctx, wiki.Finalize{AdminEmail: c.AdminEmail, AdminPassword: c.AdminPassword, SiteURL: c.PublicURL}); err != nil {
			return fmt.Errorf("setup: %w", err)
		}
		if err := until(ctx, wait, func() error { return stillInSetup(ctx, w) }); err != nil {
			return fmt.Errorf("after setup: %w", err)
		}
	case populated && setup:
		// Settings are present and the app still says setup. A finalize
		// that this or an earlier run started may be finishing; give it the
		// same budget before concluding the instance is stale.
		if err := until(ctx, wait, func() error { return stillInSetup(ctx, w) }); err == nil {
			break
		} else if !errors.Is(err, errStillInSetup) {
			return err
		}
		if err := p.StopApp(); err != nil {
			return err
		}
		return ErrStopped
	case !populated && !setup:
		return errors.New("the settings table is empty but Wiki.js is not in setup mode: the database was emptied under a running wiki. Nothing was changed")
	}

	token, err := w.Login(ctx, c.AdminEmail, c.AdminPassword)
	if err != nil {
		return fmt.Errorf("the break glass admin %s could not log in, so nothing can be configured: %w", c.AdminEmail, err)
	}
	if err := w.UpdateStrategies(ctx, token, strategies(c)); err != nil {
		return err
	}
	if err := w.UpdateSite(ctx, token, desiredSite(c)); err != nil {
		return err
	}
	g, err := w.Group(ctx, token, guestsID)
	if err != nil {
		return err
	}
	if g.ID == 0 {
		return fmt.Errorf("Wiki.js has no Guests group with id %d; nothing more was changed", guestsID)
	}
	g.Permissions, g.PageRules = guests(c)
	if err := w.UpdateGroup(ctx, token, g); err != nil {
		return err
	}
	return groups(ctx, c, w, token)
}

var errStillInSetup = errors.New("Wiki.js is still in setup mode")

// stillInSetup is nil once Wiki.js has left setup mode.
func stillInSetup(ctx context.Context, w Wiki) error {
	on, err := w.SetupMode(ctx)
	if err != nil {
		return err
	}
	if on {
		return errStillInSetup
	}
	return nil
}

func desiredSite(c config.Config) wiki.Site {
	return wiki.Site{
		Host: c.PublicURL, Title: c.Title, Description: c.Description, LogoURL: c.LogoURL,
		AutoLogin: true, HideLocal: true, TrustProxy: true, UploadMaxFileSize: c.UploadMaxBytes,
	}
}

func strategies(c config.Config) []wiki.Strategy {
	return []wiki.Strategy{
		{
			Key: "pocketid", StrategyKey: "oauth2", DisplayName: "Pocket ID", Order: 0, Enabled: true, SelfRegistration: true,
			Config: map[string]any{
				"clientId": c.ClientID, "clientSecret": c.ClientSecret,
				"authorizationURL": c.AuthorizeURL, "tokenURL": c.TokenURL, "userInfoURL": c.UserinfoURL,
				"userIdClaim": "sub", "displayNameClaim": "name", "emailClaim": "email", "pictureClaim": "picture",
				"mapGroups": true, "groupsClaim": "groups", "logoutURL": c.LogoutURL,
				"scope": "openid profile email groups", "useQueryStringForAccessToken": false, "enableCSRFProtection": true,
			},
		},
		{Key: "local", StrategyKey: "local", DisplayName: "Local", Order: 1, Enabled: true, Config: map[string]any{}},
	}
}

// groups creates each synced group Wiki.js lacks, seeds the member group if
// this run created it, and sets the admin group's permissions and its
// paisans-admin page rule. No other group is edited.
func groups(ctx context.Context, c config.Config, w Wiki, token string) error {
	list, err := w.Groups(ctx, token)
	if err != nil {
		return err
	}
	ids := map[string]int{}
	for _, g := range list {
		if _, dup := ids[g.Name]; dup && g.Name == c.AdminGroup {
			return fmt.Errorf("more than one Wiki.js group is named %q, the admin group; refusing to pick one", g.Name)
		}
		ids[g.Name] = g.ID
	}
	for _, name := range c.SyncedGroups {
		if _, ok := ids[name]; ok {
			continue
		}
		id, err := w.CreateGroup(ctx, token, name)
		if err != nil {
			return err
		}
		ids[name] = id
		if name == c.MemberGroup {
			if err := seedMembers(ctx, w, token, id); err != nil {
				return err
			}
		}
	}
	if id := ids[c.AdminGroup]; id == 1 || id == guestsID {
		return fmt.Errorf("the admin group %q is Wiki.js's built-in group %d; refusing to rewrite its permissions", c.AdminGroup, id)
	}
	admin, err := w.Group(ctx, token, ids[c.AdminGroup])
	if err != nil {
		return err
	}
	if admin.ID == 0 {
		return fmt.Errorf("the admin group %q does not exist in Wiki.js, so its permissions were not set", c.AdminGroup)
	}
	admin.Permissions = slices.Clone(AdminPermissions)
	admin.PageRules = withAdminRule(admin.PageRules)
	return w.UpdateGroup(ctx, token, admin)
}

// seedMembers replaces the defaults groups.create gave the member group
// (server/app/data.yml:143-158 at v2.5.315) with memberRoles and memberRule.
// It runs only in the run that created the group: from then on what members
// may do is the admins', in Wiki.js.
func seedMembers(ctx context.Context, w Wiki, token string, id int) error {
	g, err := w.Group(ctx, token, id)
	if err != nil {
		return err
	}
	if g.ID == 0 {
		return fmt.Errorf("the member group was created as %d but Wiki.js does not return it, so it was not seeded", id)
	}
	rule := memberRule
	rule.Roles = slices.Clone(memberRoles)
	g.Permissions, g.PageRules = slices.Clone(memberRoles), []wiki.PageRule{rule}
	return w.UpdateGroup(ctx, token, g)
}

// withAdminRule returns rules with the paisans-admin rule set to allow
// adminPageRoles on every path in every locale, rewritten in place if it is
// there and appended if not. Every other rule is kept as it is.
//
// Wiki.js grants a page action only when a group holds the permission and a
// page rule grants it too (server/core/auth.js:221-291 at v2.5.315), and
// groups.create gives a group one rule granting four read and comment roles
// (server/app/data.yml:148-158), so the permissions alone let no one write.
// Beside that default rule, an allow START "" rule wins: rules of the same
// match and path length take the later one unless the earlier is a deny
// (server/core/auth.js:416-436), and a rule without the permission asked
// for is skipped (auth.js:252). So a deny rule the admins add still wins
// over this one wherever it is at least as specific.
func withAdminRule(rules []wiki.PageRule) []wiki.PageRule {
	own := ownAdminRule()
	out := make([]wiki.PageRule, 0, len(rules)+1)
	found := false
	for _, r := range rules {
		if r.ID == adminRuleID {
			r, found = own, true
		}
		out = append(out, r)
	}
	if !found {
		out = append(out, own)
	}
	return out
}

// ownAdminRule is the paisans-admin rule: allow adminPageRoles on every path in
// every locale.
func ownAdminRule() wiki.PageRule {
	return wiki.PageRule{ID: adminRuleID, Match: "START", Path: "", Roles: slices.Clone(adminPageRoles), Locales: []string{}}
}

// until retries f every wait for two minutes' worth of tries, or until ctx ends.
func until(ctx context.Context, wait time.Duration, f func() error) error {
	var err error
	for i := 0; i < 60; i++ {
		if err = f(); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w (last: %v)", ctx.Err(), err)
		case <-time.After(wait):
		}
	}
	return err
}
