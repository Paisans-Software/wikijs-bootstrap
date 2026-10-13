// Package config reads the bootstrap's configuration from its environment,
// which the wikijs blueprint renders into bootstrap.env.
package config

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

type Config struct {
	WikiURL, DatabaseURL, PublicURL                string
	AdminEmail, AdminPassword                      string
	ClientID, ClientSecret                         string
	AuthorizeURL, TokenURL, UserinfoURL, LogoutURL string
	AdminGroup                                     string
	SyncedGroups                                   []string
	Title, Description, LogoURL                    string
	UploadMaxBytes                                 int
	HealthAddr                                     string
}

var required = []string{
	"WIKI_URL", "DATABASE_URL", "PUBLIC_URL", "ADMIN_EMAIL", "ADMIN_PASSWORD",
	"OAUTH_CLIENT_ID", "OAUTH_CLIENT_SECRET", "OAUTH_AUTHORIZE_URL", "OAUTH_TOKEN_URL",
	"OAUTH_USERINFO_URL", "OAUTH_LOGOUT_URL", "ADMIN_GROUP", "SYNCED_GROUPS", "SITE_TITLE", "UPLOAD_MAX_MB",
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
		AdminGroup: get("ADMIN_GROUP"), SyncedGroups: groups,
		Title: get("SITE_TITLE"), Description: get("SITE_DESCRIPTION"), LogoURL: get("LOGO_URL"),
		UploadMaxBytes: mb * 1024 * 1024, HealthAddr: get("HEALTH_ADDR"),
	}
	if c.HealthAddr == "" {
		c.HealthAddr = ":8090"
	}
	// Without the admin group nobody but the break glass admin could
	// administer the wiki, so the blueprint's validation refuses this too.
	if !slices.Contains(c.SyncedGroups, c.AdminGroup) {
		return Config{}, fmt.Errorf("SYNCED_GROUPS (%s) does not include ADMIN_GROUP, %s", strings.Join(c.SyncedGroups, ","), c.AdminGroup)
	}
	return c, nil
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
