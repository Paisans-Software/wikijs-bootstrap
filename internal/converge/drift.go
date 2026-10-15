package converge

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/paisans-software/wikijs-bootstrap/internal/config"
	"github.com/paisans-software/wikijs-bootstrap/internal/wiki"
)

// Reader is what a drift poll needs of Wiki.js: a login and reads, nothing
// that writes a setting.
type Reader interface {
	Login(ctx context.Context, email, password string) (string, error)
	ActiveStrategies(ctx context.Context, token string) ([]wiki.Strategy, error)
	SiteConfig(ctx context.Context, token string) (wiki.Site, error)
	Groups(ctx context.Context, token string) ([]wiki.GroupSummary, error)
	Group(ctx context.Context, token string, id int) (wiki.Group, error)
}

// Drift reads every setting Run writes on every run and returns each one
// Wiki.js no longer holds as the configuration says, one line per
// difference naming the setting and both values. It writes nothing and takes
// no lock. It logs in each time, since a Wiki.js session expires.
//
// The member group and every other synced group, any rule on the admin group
// but paisans-admin, navigation, theme and users are the admins' and are not
// compared. Permissions and roles compare as sets, rules by id, strategies by
// key and URLs without a trailing slash: Wiki.js reorders and fills in
// defaults when it saves.
func Drift(ctx context.Context, c config.Config, r Reader) ([]string, error) {
	token, err := r.Login(ctx, c.AdminEmail, c.AdminPassword)
	if err != nil {
		return nil, fmt.Errorf("the break glass admin %s could not log in, so nothing could be compared: %w", c.AdminEmail, err)
	}
	var d []string
	have, err := r.ActiveStrategies(ctx, token)
	if err != nil {
		return nil, err
	}
	d = append(d, strategyDrift(have, strategies(c))...)

	site, err := r.SiteConfig(ctx, token)
	if err != nil {
		return nil, err
	}
	d = append(d, siteDrift(site, desiredSite(c))...)

	g, err := r.Group(ctx, token, guestsID)
	if err != nil {
		return nil, err
	}
	perms, rules := guests(c)
	if g.ID == 0 {
		d = append(d, fmt.Sprintf("Guests group %d: Wiki.js has none, configuration says it", guestsID))
	} else {
		d = append(d, setDrift("Guests permissions", g.Permissions, perms)...)
		d = append(d, rulesDrift("Guests page rule", g.PageRules, rules, true)...)
	}

	list, err := r.Groups(ctx, token)
	if err != nil {
		return nil, err
	}
	id := 0
	for _, s := range list {
		if s.Name != c.AdminGroup {
			continue
		}
		if id != 0 {
			return nil, fmt.Errorf("more than one Wiki.js group is named %q, the admin group", c.AdminGroup)
		}
		id = s.ID
	}
	what := "admin group " + c.AdminGroup
	if id == 0 {
		return append(d, what+": Wiki.js has none, configuration says it"), nil
	}
	admin, err := r.Group(ctx, token, id)
	if err != nil {
		return nil, err
	}
	d = append(d, setDrift(what+" permissions", admin.Permissions, AdminPermissions)...)
	d = append(d, rulesDrift(what+" page rule", admin.PageRules, []wiki.PageRule{ownAdminRule()}, false)...)
	return d, nil
}

func strategyDrift(have, want []wiki.Strategy) []string {
	var d []string
	for _, w := range want {
		i := slices.IndexFunc(have, func(h wiki.Strategy) bool { return h.Key == w.Key })
		if i < 0 {
			d = append(d, fmt.Sprintf("strategy %s: Wiki.js has none, configuration says it", w.Key))
			continue
		}
		h, name := have[i], "strategy "+w.Key
		d = append(d, differs(name+" type", h.StrategyKey, w.StrategyKey)...)
		d = append(d, differs(name+" display name", h.DisplayName, w.DisplayName)...)
		d = append(d, differs(name+" enabled", h.Enabled, w.Enabled)...)
		d = append(d, differs(name+" order", h.Order, w.Order)...)
		d = append(d, differs(name+" self registration", h.SelfRegistration, w.SelfRegistration)...)
		keys := make([]string, 0, len(w.Config))
		for k := range w.Config {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			hv, ok := h.Config[k]
			if k == "clientSecret" {
				if !ok || hv != w.Config[k] {
					d = append(d, name+": client secret differs")
				}
				continue
			}
			if !ok {
				d = append(d, fmt.Sprintf("%s %s: Wiki.js has none, configuration says %s", name, k, show(w.Config[k])))
				continue
			}
			want := w.Config[k]
			if hs, ws, ok := strings2(hv, want); ok && strings.HasSuffix(k, "URL") {
				hv, want = strings.TrimRight(hs, "/"), strings.TrimRight(ws, "/")
			}
			d = append(d, differs(name+" "+k, hv, want)...)
		}
	}
	for _, h := range have {
		if !slices.ContainsFunc(want, func(w wiki.Strategy) bool { return w.Key == h.Key }) {
			d = append(d, fmt.Sprintf("strategy %s: Wiki.js has it, configuration says none", h.Key))
		}
	}
	return d
}

// strings2 is a and b as strings, if both are.
func strings2(a, b any) (string, string, bool) {
	as, aok := a.(string)
	bs, bok := b.(string)
	return as, bs, aok && bok
}

func siteDrift(have, want wiki.Site) []string {
	url := func(s string) string { return strings.TrimRight(strings.TrimSpace(s), "/") }
	var d []string
	d = append(d, differs("site host", url(have.Host), url(want.Host))...)
	d = append(d, differs("site title", strings.TrimSpace(have.Title), strings.TrimSpace(want.Title))...)
	d = append(d, differs("site description", strings.TrimSpace(have.Description), strings.TrimSpace(want.Description))...)
	d = append(d, differs("site logo", url(have.LogoURL), url(want.LogoURL))...)
	d = append(d, differs("site auto login", have.AutoLogin, want.AutoLogin)...)
	d = append(d, differs("site hide local sign in", have.HideLocal, want.HideLocal)...)
	d = append(d, differs("site trust proxy", have.TrustProxy, want.TrustProxy)...)
	d = append(d, differs("site upload limit", have.UploadMaxFileSize, want.UploadMaxFileSize)...)
	return d
}

// rulesDrift compares rules by id: each wanted rule against the one Wiki.js
// has with its id. With all, a rule Wiki.js has that is not wanted is a
// difference too; without, it is someone else's and is left alone.
func rulesDrift(what string, have, want []wiki.PageRule, all bool) []string {
	var d []string
	for _, w := range want {
		var found []wiki.PageRule
		for _, h := range have {
			if h.ID == w.ID {
				found = append(found, h)
			}
		}
		switch {
		case len(found) == 0:
			d = append(d, fmt.Sprintf("%s %s: Wiki.js has none, configuration says %s", what, w.ID, rule(w)))
		case len(found) > 1:
			d = append(d, fmt.Sprintf("%s %s: Wiki.js has %d, configuration says one: %s", what, w.ID, len(found), rule(w)))
		case !sameRule(found[0], w):
			d = append(d, fmt.Sprintf("%s %s: Wiki.js has %s, configuration says %s", what, w.ID, rule(found[0]), rule(w)))
		}
	}
	if all {
		for _, h := range have {
			if !slices.ContainsFunc(want, func(w wiki.PageRule) bool { return w.ID == h.ID }) {
				d = append(d, fmt.Sprintf("%s %s: Wiki.js has %s, configuration says none", what, h.ID, rule(h)))
			}
		}
	}
	return d
}

func sameRule(a, b wiki.PageRule) bool {
	return a.Deny == b.Deny && a.Match == b.Match && a.Path == b.Path &&
		slices.Equal(set(a.Roles), set(b.Roles)) && slices.Equal(set(a.Locales), set(b.Locales))
}

func rule(r wiki.PageRule) string {
	kind := "allow"
	if r.Deny {
		kind = "deny"
	}
	return fmt.Sprintf("%s %s %q roles %v locales %v", kind, r.Match, r.Path, set(r.Roles), set(r.Locales))
}

func setDrift(what string, have, want []string) []string {
	if h, w := set(have), set(want); !slices.Equal(h, w) {
		return []string{fmt.Sprintf("%s: Wiki.js has %v, configuration says %v", what, h, w)}
	}
	return nil
}

// set is s sorted, without repeats and never nil.
func set(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return append([]string{}, slices.Compact(out)...)
}

func differs(what string, have, want any) []string {
	if have == want {
		return nil
	}
	return []string{fmt.Sprintf("%s: Wiki.js has %s, configuration says %s", what, show(have), show(want))}
}

func show(v any) string {
	if s, ok := v.(string); ok {
		return fmt.Sprintf("%q", s)
	}
	return fmt.Sprint(v)
}
