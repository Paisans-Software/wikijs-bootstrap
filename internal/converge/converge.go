// Package converge is one bootstrap run: set Wiki.js up if this database
// never was, then bring its sign-in, site settings and groups to what the
// configuration says. Its writes are exactly those the spec names.
package converge

import (
	"context"
	"errors"
	"fmt"
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
	if err := w.UpdateSite(ctx, token, wiki.Site{
		Host: c.PublicURL, Title: c.Title, Description: c.Description, LogoURL: c.LogoURL,
		AutoLogin: true, HideLocal: true, TrustProxy: true, UploadMaxFileSize: c.UploadMaxBytes,
	}); err != nil {
		return err
	}
	guests, err := w.Group(ctx, token, guestsID)
	if err != nil {
		return err
	}
	if guests.ID == 0 {
		return fmt.Errorf("Wiki.js has no Guests group with id %d; nothing more was changed", guestsID)
	}
	guests.Permissions, guests.PageRules = []string{}, []wiki.PageRule{}
	if err := w.UpdateGroup(ctx, token, guests); err != nil {
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

// groups creates each synced group Wiki.js lacks and sets the admin group's
// permissions, keeping its page rules. No other group is edited.
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
	admin.Permissions = AdminPermissions
	return w.UpdateGroup(ctx, token, admin)
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
