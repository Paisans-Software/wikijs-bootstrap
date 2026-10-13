package config

import (
	"strings"
	"testing"
)

func env(over map[string]string) func(string) string {
	base := map[string]string{
		"WIKI_URL": "http://app:3000", "DATABASE_URL": "postgresql://u:p@db:5432/wiki?ssl=no-verify",
		"PUBLIC_URL": "https://wiki.example.org", "ADMIN_EMAIL": "wikijs-admin@example.org", "ADMIN_PASSWORD": "not-a-real-password",
		"OAUTH_CLIENT_ID": "cid", "OAUTH_CLIENT_SECRET": "not-a-real-secret",
		"OAUTH_AUTHORIZE_URL": "https://id.example.org/authorize", "OAUTH_TOKEN_URL": "https://id.example.org/api/oidc/token",
		"OAUTH_USERINFO_URL": "https://id.example.org/api/oidc/userinfo", "OAUTH_LOGOUT_URL": "https://id.example.org/api/oidc/end-session",
		"ADMIN_GROUP": "admins", "SYNCED_GROUPS": "admins,members", "SITE_TITLE": "Example Wiki",
		"SITE_DESCRIPTION": "", "LOGO_URL": "", "UPLOAD_MAX_MB": "5",
	}
	for k, v := range over {
		base[k] = v
	}
	return func(k string) string { return base[k] }
}

func TestFromEnvReadsEverything(t *testing.T) {
	c, err := FromEnv(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.SyncedGroups, ",") != "admins,members" || c.UploadMaxBytes != 5*1024*1024 || c.HealthAddr != ":8090" {
		t.Errorf("%+v", c)
	}
}

func TestMissingRequiredIsNamed(t *testing.T) {
	_, err := FromEnv(env(map[string]string{"ADMIN_PASSWORD": ""}))
	if err == nil || !strings.Contains(err.Error(), "ADMIN_PASSWORD") {
		t.Errorf("err = %v", err)
	}
}

func TestSyncedGroupsMustHoldTheAdminGroup(t *testing.T) {
	_, err := FromEnv(env(map[string]string{"SYNCED_GROUPS": "members"}))
	if err == nil || !strings.Contains(err.Error(), "admins") {
		t.Errorf("err = %v", err)
	}
}

func TestBuiltInWikiGroupsAreRefused(t *testing.T) {
	for _, name := range []string{"Administrators", "Guests"} {
		for _, over := range []map[string]string{
			{"ADMIN_GROUP": name, "SYNCED_GROUPS": name + ",members"},
			{"SYNCED_GROUPS": "admins," + name},
		} {
			_, err := FromEnv(env(over))
			if err == nil || !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), "built-in") {
				t.Errorf("%v: err = %v", over, err)
			}
		}
	}
	if _, err := FromEnv(env(map[string]string{"ADMIN_GROUP": "administrators", "SYNCED_GROUPS": "administrators"})); err != nil {
		t.Errorf("the refusal is exact and case-sensitive: %v", err)
	}
}

func TestNoVerifyBecomesRequireForPgx(t *testing.T) {
	c, _ := FromEnv(env(nil))
	if got := c.PgxDSN(); got != "postgresql://u:p@db:5432/wiki?sslmode=require" {
		t.Errorf("PgxDSN = %s", got)
	}
	c, _ = FromEnv(env(map[string]string{"DATABASE_URL": "postgresql://u:p@postgres:5432/wiki"}))
	if got := c.PgxDSN(); got != "postgresql://u:p@postgres:5432/wiki?sslmode=disable" {
		t.Errorf("pinned PgxDSN = %s", got)
	}
}
