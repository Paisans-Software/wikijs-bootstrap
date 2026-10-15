# wikijs-bootstrap

Wiki.js 2.x reads only its database settings from the environment; its first
admin, sign-in providers, site URL and groups live in its database and change
through its GraphQL API. This program is what sets them. It runs next to
Wiki.js, finishes setup once per database (taking a Postgres advisory lock so
two instances cannot both do it), then converges the sign-in provider, the site
settings and the groups on every start. It then stays up, compares Wiki.js with
its configuration every `DRIFT_INTERVAL`, and reports on port 8090 by default:

| Endpoint | 200 | 503 |
|---|---|---|
| `GET /livez` | `converged`: a run has converged, whatever a later poll finds | `converging`, or the run's error |
| `GET /healthz` | `converged`, and the last poll found no drift | `converging`, the run's error, `drift poll failed: <error>`, or `drifted: <differences>` |

The image's Docker healthcheck, `/wikijs-bootstrap healthcheck`, reads
`/livez`, so a wiki someone has changed in Wiki.js is still a healthy
container. Drift is reported on `/healthz`, in the log and to the monitors in
`HEARTBEAT_URLS`.

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
| `MEMBER_GROUP` | the group every member is in |
| `SYNCED_GROUPS` | comma separated groups kept in step with the identity provider; must include `ADMIN_GROUP` and `MEMBER_GROUP` |
| `SITE_TITLE` | the site's title |
| `UPLOAD_MAX_MB` | upload limit, in whole megabytes |

`ADMIN_GROUP`, `MEMBER_GROUP` and `SYNCED_GROUPS` refuse Wiki.js's built-in
`Administrators` and `Guests` groups.

Optional:

| Variable | Meaning |
|---|---|
| `HEALTH_ADDR` | health listen address, default `:8090` |
| `SITE_DESCRIPTION` | the site's description |
| `LOGO_URL` | the site's logo |
| `GUEST_PATHS` | comma separated page path prefixes a guest may read, each without a leading slash; empty, the default, means the wiki is sign in only |
| `DRIFT_INTERVAL` | how often to compare Wiki.js with the configuration, a Go duration, default `15m` |
| `HEARTBEAT_URLS` | space separated push monitor URLs, each `https://<monitor hostname>/ping/<token>`; empty, the default, pushes nothing |

`GUEST_PATHS` refuses an entry with a leading slash, a space or a control
character, and any entry that would reach `admins/`: `admins`, anything under
it, and anything `admins/` starts with, such as `adm`, because Wiki.js matches
a prefix as a plain string. A prefix matches more than one folder: `docs` also
opens `docs-old/`. An image is checked against its own folder's path, not the
page's, so a public page whose images are filed outside `GUEST_PATHS` shows
guests broken images.

Each poll logs in as the break glass admin, and Wiki.js allows five logins a
minute from one address, so a `DRIFT_INTERVAL` under 15 seconds has polls
refused.

## What it writes, and only that

| Write | Through |
|---|---|
| setup, once, on an empty database | `POST /finalize` |
| the strategy list | `authentication.updateStrategies` |
| the site settings | `site.updateConfig` |
| the `Guests` group's permissions and page rules | `groups.update`, ID 2 |
| the admin group's permissions and its `paisans-admin` page rule | `groups.update` |
| a missing `synced_groups` group | `groups.create` |
| the member group's permissions and page rules, in the run that creates it | `groups.update` |
| a stale instance's restart | `SIGTERM` to the app's process |

What each group gets:

| Group | Written | Permissions | Page rules |
|---|---|---|---|
| admin group | every run | every permission except `manage:system` and `manage:api` | `paisans-admin`: allow, `START ''`, every page role; any other rule is kept |
| member group | in the run that creates it | `read:pages`, `write:pages`, `read:source`, `read:history`, `read:assets`, `write:assets`, `read:comments`, `write:comments` | only `paisans-members`: allow, `REGEX ^(?!admins(/\|$))`, the same roles |
| `Guests` | every run | none; with `GUEST_PATHS`, `read:pages`, `read:assets`, `read:comments` | one allow `START <path>` per `GUEST_PATHS` entry, `paisans-guests-1` on, the same roles |

So `admins/` and everything under it is read by admins alone, members read and
edit every other page, and a member group that already exists keeps what the
admins have given it.

It reads the `settings` table and takes the advisory lock, and writes nothing
to the database directly. It never deletes a group or a user, never changes a
group's members, never edits a group other than `Guests`, the admin group and
a member group it has just created, and never edits a page. Each write is one log line: what, the target, the
result. `internal/audit` fails the tests if any other mutation or any direct
SQL write appears in the code.

## Drift

Every `DRIFT_INTERVAL` the bootstrap logs in as the break glass admin and reads
what each run writes: the strategy list (each strategy's key, type, display
name, enabled state, order, self registration and the config fields it writes),
the site settings it writes, the `Guests` group's permissions and page rules,
and the admin group's permissions and its `paisans-admin` rule. It writes
nothing and takes no lock; the next run, at the next start of the container,
puts the configuration back.

The member group and the other synced groups, any other rule on the admin
group, navigation, theme and users are the admins' and are not compared.
Permissions and roles compare as sets, rules by id, strategies by key and URLs
without a trailing slash. Each difference is one log line naming the setting
and both values; the OAuth client secret is never printed, and a difference in
it reads `client secret differs`.

Every 60 seconds, apart from the poll, the bootstrap pushes its last result to
each of `HEARTBEAT_URLS` at once:

| Last result | Push |
|---|---|
| converged, no drift | `GET <url>` |
| drifted, or a run or a poll failed | `POST <url>/fail`, `Content-Type: text/plain`, the `/healthz` body |
| a run in progress | nothing |

Each push has a 10 second timeout and two retries, and a push that fails logs
one line naming the monitor's host; a URL carries its token and is never
logged.

## Releases

A tag `vX.Y.Z` publishes `X.Y.Z` to `ghcr.io/paisans-software/wikijs-bootstrap`;
tagging is a human's action.
