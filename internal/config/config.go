// Package config reads the bootstrap's configuration from its environment,
// which the wikijs blueprint renders into bootstrap.env.
package config

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type Config struct {
	WikiURL, DatabaseURL, PublicURL                string
	AdminEmail, AdminPassword                      string
	ClientID, ClientSecret                         string
	AuthorizeURL, TokenURL, UserinfoURL, LogoutURL string
	AdminGroup, MemberGroup                        string
	SyncedGroups                                   []string
	GuestPaths                                     []string
	Title, Description, LogoURL                    string
	UploadMaxBytes                                 int
	HealthAddr                                     string
	DriftInterval                                  time.Duration
	HeartbeatURLs                                  []string
}

var required = []string{
	"WIKI_URL", "DATABASE_URL", "PUBLIC_URL", "ADMIN_EMAIL", "ADMIN_PASSWORD",
	"OAUTH_CLIENT_ID", "OAUTH_CLIENT_SECRET", "OAUTH_AUTHORIZE_URL", "OAUTH_TOKEN_URL",
	"OAUTH_USERINFO_URL", "OAUTH_LOGOUT_URL", "ADMIN_GROUP", "MEMBER_GROUP", "SYNCED_GROUPS", "SITE_TITLE", "UPLOAD_MAX_MB",
}

// FromEnv reads every variable, refusing a missing one by name.
func FromEnv(get func(string) string) (Config, error) {
	var missing []string
	for _, k := range required {
		if strings.TrimSpace(get(k)) == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("not set: %s", strings.Join(missing, ", "))
	}
	mb, err := strconv.Atoi(get("UPLOAD_MAX_MB"))
	if err != nil || mb < 1 {
		return Config{}, fmt.Errorf("UPLOAD_MAX_MB is %q, not a whole number of megabytes", get("UPLOAD_MAX_MB"))
	}
	var groups []string
	for _, g := range strings.Split(get("SYNCED_GROUPS"), ",") {
		if g = strings.TrimSpace(g); g != "" && !slices.Contains(groups, g) {
			groups = append(groups, g)
		}
	}
	c := Config{
		WikiURL: strings.TrimRight(get("WIKI_URL"), "/"), DatabaseURL: get("DATABASE_URL"),
		PublicURL:  strings.TrimRight(get("PUBLIC_URL"), "/"),
		AdminEmail: get("ADMIN_EMAIL"), AdminPassword: get("ADMIN_PASSWORD"),
		ClientID: get("OAUTH_CLIENT_ID"), ClientSecret: get("OAUTH_CLIENT_SECRET"),
		AuthorizeURL: get("OAUTH_AUTHORIZE_URL"), TokenURL: get("OAUTH_TOKEN_URL"),
		UserinfoURL: get("OAUTH_USERINFO_URL"), LogoutURL: get("OAUTH_LOGOUT_URL"),
		AdminGroup: get("ADMIN_GROUP"), MemberGroup: get("MEMBER_GROUP"), SyncedGroups: groups,
		Title: get("SITE_TITLE"), Description: get("SITE_DESCRIPTION"), LogoURL: get("LOGO_URL"),
		UploadMaxBytes: mb * 1024 * 1024, HealthAddr: get("HEALTH_ADDR"),
	}
	if c.HealthAddr == "" {
		c.HealthAddr = ":8090"
	}
	c.DriftInterval = 15 * time.Minute
	if v := strings.TrimSpace(get("DRIFT_INTERVAL")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("DRIFT_INTERVAL is %q, not a positive duration such as 15m", v)
		}
		c.DriftInterval = d
	}
	if c.GuestPaths, err = guestPaths(get("GUEST_PATHS")); err != nil {
		return Config{}, err
	}
	if c.HeartbeatURLs, err = heartbeatURLs(get("HEARTBEAT_URLS")); err != nil {
		return Config{}, err
	}
	// Wiki.js's own groups are matched by name on the other side, and the
	// admin and member groups get written permissions: naming a built-in one
	// would strip Administrators or open up Guests.
	for _, g := range append([]string{c.AdminGroup, c.MemberGroup}, c.SyncedGroups...) {
		if g == "Administrators" || g == "Guests" {
			return Config{}, fmt.Errorf("%q cannot be ADMIN_GROUP, MEMBER_GROUP or in SYNCED_GROUPS: it is one of Wiki.js's built-in groups", g)
		}
	}
	// Without the admin group nobody but the break glass admin could
	// administer the wiki, so the blueprint's validation refuses this too.
	if !slices.Contains(c.SyncedGroups, c.AdminGroup) {
		return Config{}, fmt.Errorf("SYNCED_GROUPS (%s) does not include ADMIN_GROUP, %s", strings.Join(c.SyncedGroups, ","), c.AdminGroup)
	}
	// The member group is seeded when the bootstrap creates it, which it
	// does only for a synced group.
	if !slices.Contains(c.SyncedGroups, c.MemberGroup) {
		return Config{}, fmt.Errorf("SYNCED_GROUPS (%s) does not include MEMBER_GROUP, %s", strings.Join(c.SyncedGroups, ","), c.MemberGroup)
	}
	return c, nil
}

// adminsPrefix is the path under which only admins read pages.
const adminsPrefix = "admins/"

// guestPaths reads GUEST_PATHS: comma separated page path prefixes, each
// without a leading slash. Wiki.js matches a START rule as a plain string
// prefix of "/" + the page path (server/core/auth.js:254-257 at v2.5.315),
// so an entry that is a prefix of "admins/", such as "adm", reaches the
// pages only admins read just as "admins" itself does; every such entry is
// refused.
func guestPaths(v string) ([]string, error) {
	var out []string
	for _, p := range strings.Split(v, ",") {
		p = strings.TrimSpace(p)
		if p == "" || slices.Contains(out, p) {
			continue
		}
		if strings.HasPrefix(p, "/") {
			return nil, fmt.Errorf("GUEST_PATHS entry %q starts with a slash; a path is written without one", p)
		}
		if strings.IndexFunc(p, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
			return nil, fmt.Errorf("GUEST_PATHS entry %q holds a space or a control character", p)
		}
		if strings.HasPrefix(adminsPrefix, p) || strings.HasPrefix(p, adminsPrefix) {
			return nil, fmt.Errorf("GUEST_PATHS entry %q would let guests read admins/, which only admins read", p)
		}
		out = append(out, p)
	}
	return out, nil
}

// heartbeatURLs reads HEARTBEAT_URLS, space separated. Each carries a
// monitor's push token, so an error names an entry by its position only.
func heartbeatURLs(v string) ([]string, error) {
	var out []string
	for i, s := range strings.Fields(v) {
		u, err := url.Parse(s)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("HEARTBEAT_URLS entry %d is not an http or https URL", i+1)
		}
		out = append(out, strings.TrimRight(s, "/"))
	}
	return out, nil
}

// PgxDSN is DatabaseURL as pgx takes it. Wiki.js's connection string parser
// reads ssl=no-verify (encrypt, do not check the certificate) and ignores
// sslmode; pgx knows no ssl parameter, and its sslmode=require means the
// same. A DSN without either is a pinned wiki's own Postgres, which serves no
// TLS, so it gets disable rather than pgx's default of prefer.
func (c Config) PgxDSN() string {
	u, err := url.Parse(c.DatabaseURL)
	if err != nil {
		return c.DatabaseURL
	}
	q := u.Query()
	if q.Get("ssl") == "no-verify" {
		q.Del("ssl")
		q.Set("sslmode", "require")
	} else if q.Get("sslmode") == "" {
		q.Set("sslmode", "disable")
	}
	u.RawQuery = q.Encode()
	return u.String()
}
