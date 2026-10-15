# wikijs-bootstrap

Wiki.js 2.x reads only its database settings from the environment; its first
admin, sign-in providers, site URL and groups live in its database and change
through its GraphQL API. This program is what sets them. It runs next to
Wiki.js, finishes setup once per database (taking a Postgres advisory lock so
two instances cannot both do it), then converges the sign-in provider, the site
settings and the groups on every start. It then stays up and reports on
`GET /healthz` (port 8090 by default): 200 `converged`, or 503 with the last
error or `converging`. The image's Docker healthcheck reads it with
`/wikijs-bootstrap healthcheck`.

Built for the `wikijs` blueprint in the paisans core catalog.

## Environment

Required:

| Variable | Meaning |
|---|---|
| `WIKI_URL` | where the bootstrap reaches Wiki.js, e.g. `http://app:3000` |
| `DATABASE_URL` | Wiki.js's Postgres connection string (`ssl=no-verify` is read as `sslmode=require`) |
| `PUBLIC_URL` | the wiki's public address |
| `ADMIN_EMAIL`, `ADMIN_PASSWORD` | the local break glass admin |
| `OAUTH_CLIENT_ID`, `OAUTH_CLIENT_SECRET` | the OAuth2 client |
| `OAUTH_AUTHORIZE_URL`, `OAUTH_TOKEN_URL`, `OAUTH_USERINFO_URL`, `OAUTH_LOGOUT_URL` | the identity provider's endpoints |
| `ADMIN_GROUP` | the group whose members administer the wiki |
| `SYNCED_GROUPS` | comma separated groups kept in step with the identity provider; must include `ADMIN_GROUP` |
| `SITE_TITLE` | the site's title |
| `UPLOAD_MAX_MB` | upload limit, in whole megabytes |

`ADMIN_GROUP` and `SYNCED_GROUPS` refuse Wiki.js's built-in `Administrators`
and `Guests` groups.

Optional:

| Variable | Meaning |
|---|---|
| `HEALTH_ADDR` | health listen address, default `:8090` |
| `SITE_DESCRIPTION` | the site's description |
| `LOGO_URL` | the site's logo |

## What it writes, and only that

| Write | Through |
|---|---|
| setup, once, on an empty database | `POST /finalize` |
| the strategy list | `authentication.updateStrategies` |
| the site settings | `site.updateConfig` |
| the `Guests` group's permissions and page rules | `groups.update`, ID 2 |
| the admin group's permissions and its `paisans-admin` page rule | `groups.update` |
| a missing `synced_groups` group | `groups.create` |
| a stale instance's restart | `SIGTERM` to the app's process |

It reads the `settings` table and takes the advisory lock, and writes nothing
to the database directly. It never deletes a group or a user, never changes a
group's members, never edits a group other than `Guests` and the admin group,
and never edits a page. Each write is one log line: what, the target, the
result. `internal/audit` fails the tests if any other mutation or any direct
SQL write appears in the code.

## Releases

A tag `vX.Y.Z` publishes `X.Y.Z` to `ghcr.io/paisans-software/wikijs-bootstrap`;
tagging is a human's action.
