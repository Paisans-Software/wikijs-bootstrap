package config

import (
	"strings"
	"testing"
	"time"
)

func env(over map[string]string) func(string) string {
	base := map[string]string{
		"WIKI_URL": "http://app:3000", "DATABASE_URL": "postgresql://u:p@db:5432/wiki?ssl=no-verify",
		"PUBLIC_URL": "https://wiki.example.org", "ADMIN_EMAIL": "wikijs-admin@example.org", "ADMIN_PASSWORD": "not-a-real-password",
		"OAUTH_CLIENT_ID": "cid", "OAUTH_CLIENT_SECRET": "not-a-real-secret",
		"OAUTH_AUTHORIZE_URL": "https://id.example.org/authorize", "OAUTH_TOKEN_URL": "https://id.example.org/api/oidc/token",
		"OAUTH_USERINFO_URL": "https://id.example.org/api/oidc/userinfo", "OAUTH_LOGOUT_URL": "https://id.example.org/api/oidc/end-session",
		"ADMIN_GROUP": "admins", "MEMBER_GROUP": "members", "SYNCED_GROUPS": "admins,members", "SITE_TITLE": "Example Wiki",
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
	if c.MemberGroup != "members" || len(c.GuestPaths) != 0 || c.DriftInterval != 15*time.Minute || len(c.HeartbeatURLs) != 0 {
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

func TestSyncedGroupsMustHoldTheMemberGroup(t *testing.T) {
	_, err := FromEnv(env(map[string]string{"MEMBER_GROUP": "people"}))
	if err == nil || !strings.Contains(err.Error(), "MEMBER_GROUP") || !strings.Contains(err.Error(), "people") {
		t.Errorf("err = %v", err)
	}
	if _, err := FromEnv(env(map[string]string{"MEMBER_GROUP": ""})); err == nil || !strings.Contains(err.Error(), "MEMBER_GROUP") {
		t.Errorf("an unset MEMBER_GROUP: err = %v", err)
	}
}

func TestBuiltInWikiGroupsAreRefused(t *testing.T) {
	for _, name := range []string{"Administrators", "Guests"} {
		for _, over := range []map[string]string{
			{"ADMIN_GROUP": name, "SYNCED_GROUPS": name + ",members"},
			{"MEMBER_GROUP": name, "SYNCED_GROUPS": "admins," + name},
			{"SYNCED_GROUPS": "admins,members," + name},
		} {
			_, err := FromEnv(env(over))
			if err == nil || !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), "built-in") {
				t.Errorf("%v: err = %v", over, err)
			}
		}
	}
	if _, err := FromEnv(env(map[string]string{"ADMIN_GROUP": "administrators", "SYNCED_GROUPS": "administrators,members"})); err != nil {
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

func TestGuestPathsAreTrimmedPrefixes(t *testing.T) {
	c, err := FromEnv(env(map[string]string{"GUEST_PATHS": " public , docs/howto,,public"}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.GuestPaths, "|") != "public|docs/howto" {
		t.Errorf("GuestPaths %q", c.GuestPaths)
	}
}

func TestGuestPathsRefuseWhatWouldExposeAdminsOrCannotBeAPrefix(t *testing.T) {
	for _, p := range []string{"/public", "admins", "admins/notes", "admin", "a", "adm", "has space", "tab\there", "ctl\x01x"} {
		if _, err := FromEnv(env(map[string]string{"GUEST_PATHS": "public," + p})); err == nil || !strings.Contains(err.Error(), "GUEST_PATHS") {
			t.Errorf("%q: err = %v", p, err)
		}
	}
	for _, p := range []string{"administration", "adminsx", "docs/admins", "b"} {
		if _, err := FromEnv(env(map[string]string{"GUEST_PATHS": p})); err != nil {
			t.Errorf("%q: %v", p, err)
		}
	}
}

func TestDriftInterval(t *testing.T) {
	c, err := FromEnv(env(map[string]string{"DRIFT_INTERVAL": "90s"}))
	if err != nil || c.DriftInterval != 90*time.Second {
		t.Errorf("%v, %v", c.DriftInterval, err)
	}
	for _, v := range []string{"soon", "0s", "-1m", "15"} {
		if _, err := FromEnv(env(map[string]string{"DRIFT_INTERVAL": v})); err == nil || !strings.Contains(err.Error(), "DRIFT_INTERVAL") {
			t.Errorf("%q: err = %v", v, err)
		}
	}
}

func TestHeartbeatURLsAreSpaceSeparated(t *testing.T) {
	c, err := FromEnv(env(map[string]string{"HEARTBEAT_URLS": " https://up.example.org/ping/abc  https://up2.example.org/ping/def/ "}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.HeartbeatURLs, "|") != "https://up.example.org/ping/abc|https://up2.example.org/ping/def" {
		t.Errorf("HeartbeatURLs %q", c.HeartbeatURLs)
	}
	for _, v := range []string{"up.example.org/ping/abc", "ftp://up.example.org/x", "https://"} {
		_, err := FromEnv(env(map[string]string{"HEARTBEAT_URLS": v}))
		if err == nil || !strings.Contains(err.Error(), "HEARTBEAT_URLS") {
			t.Errorf("%q: err = %v", v, err)
		}
		if err != nil && strings.Contains(err.Error(), "abc") {
			t.Errorf("%q: the error carries the URL, and with it the token: %v", v, err)
		}
	}
}
