# PingFederate auth provider

An Obot authentication provider for Ping Identity PingFederate. It uses the
standard OpenID Connect authorization code flow, so it also works with any
other identity provider that publishes a discovery document.

Like Obot's other auth providers it is a thin wrapper around
[oauth2-proxy](https://github.com/obot-platform/oauth2-proxy), which owns the
login flow and the session cookie. This provider adds what Obot needs on top:
the endpoints Obot calls to identify the user behind a request, read their
profile, and list their groups.

## Setting up PingFederate

1. Create an OAuth client (**Applications > OAuth > Clients**) for Obot:
   - Client authentication: **client secret**.
   - Grant types: **Authorization Code** and **Refresh Token**.
   - Redirect URI: Obot's public URL with a trailing slash, for example
     `https://obot.example.com/`. Obot receives the callback at its root and
     forwards it to the provider; `/oauth2/callback` is not what PingFederate
     calls.
   - PKCE: optional in PingFederate. The provider sends an `S256` code
     challenge by default; if the client is configured to reject PKCE, set the
     PKCE method to `none`.
   - OpenID Connect policy: release `sub`, `email`, `name` (or `given_name`
     and `family_name`), `preferred_username`, and, for group-based access
     control in Obot, a group membership attribute (`groups` by default).
     Groups can be released in the ID token, the userinfo response, or both.
2. In Obot, go to **Identity & Access > Auth Providers**, configure
   **PingFederate**, and enter the client ID, client secret, and issuer URL
   (`https://sso.example.com` or `https://sso.example.com:9031`). The issuer
   must match the `issuer` in `<issuer>/.well-known/openid-configuration`
   exactly, including the port and the absence of a trailing slash; the
   provider refuses to start otherwise and says what it found.
3. If PingFederate's TLS certificate is issued by a private CA, paste the CA
   certificate chain (PEM) into **CA Certificate**. The provider process does
   not inherit the Obot pod's trust store, so mounting a bundle on the pod is
   not enough on its own; alternatively point **CA Certificate File** at a
   path inside the container.

## How Obot identifies users

- The user's identity in Obot is the OIDC `sub` claim, which is stable even if
  the email address changes. Obot stores it as the provider user ID.
- The email address comes from the `email` claim (configurable). Logins are
  accepted when the ID token does not carry `email_verified`, which
  PingFederate typically omits; an explicit `email_verified: false` is also
  accepted unless **Allow Unverified Email** is turned off.
- **Name Claim** chooses the claim shown in **Identity & Access → Users → Name**
  (default: `name`). If missing, the provider falls back to `name`, then
  `given_name` + `family_name`, then `preferred_username`, then the email.
  Release the name claim in userinfo or the ID token; userinfo takes precedence.
- **Email Display Domain** appends a domain to the Email Claim in **Users** and
  **My Account** when it contains only a username. For example, `JDOE` with
  `company.com` displays as `jdoe@company.com`. A full email address keeps its
  existing domain. This is UI formatting; the raw email claim, username,
  subject, account matching, and group lookups keep their original values.
  Leave the domain empty if no suffix is needed.
- Groups are reported to Obot as `pingfederate/<group name>`, matching the
  `groupIDPrefix` in the manifest. Group names are taken from the groups claim
  verbatim; set **Groups Delimiter** when PingFederate sends them as one
  delimited string.
- To map groups to Obot roles (**Identity & Access > Groups** in the admin UI,
  or `POST /api/group-role-assignments` with `groupName:
  "pingfederate/<group>"`), list the groups under **Known Groups** so the
  picker offers them before any member has signed in.

## How it works

Obot forwards the browser's `/oauth2/start`, `/oauth2/callback` and
`/oauth2/sign_out` requests to oauth2-proxy inside the provider, and calls
three provider-specific endpoints over the loopback interface:

| Endpoint | Purpose |
|---|---|
| `POST /obot-get-state` | Identifies the user behind a request from the session cookie, refreshing the upstream tokens when due. The response includes the user's groups. |
| `GET /obot-get-user-info` | Returns the profile for a bearer access token: the userinfo claims plus `id` (the subject), `name`, `email` and `picture`. Responses are cached per token for a few minutes because Obot asks often. If the userinfo endpoint is unavailable, the claims from the ID token are used. |
| `POST /obot-list-user-auth-groups` | Returns the groups recorded for a provider user ID. The provider remembers groups from every state and userinfo lookup. An unknown ID is an error rather than an empty list, so that Obot keeps the memberships it already has. |
| `GET /obot-list-auth-groups` | Backs the group picker in Obot's admin UI. PingFederate has no directory API, so the provider lists the groups declared in **Known Groups** plus every group seen in a token since it started. |
| `GET /obot-get-auth-groups` | Resolves group IDs to names; a group's name is its ID without the `pingfederate/` prefix. |

## Configuration

All settings are environment variables; Obot passes the values entered in the
admin UI. Required:

| Variable | Meaning |
|---|---|
| `OBOT_PINGFEDERATE_AUTH_PROVIDER_CLIENT_ID` | OAuth client ID |
| `OBOT_PINGFEDERATE_AUTH_PROVIDER_CLIENT_SECRET` | OAuth client secret |
| `OBOT_PINGFEDERATE_AUTH_PROVIDER_ISSUER_URL` | OpenID Connect issuer |
| `OBOT_AUTH_PROVIDER_COOKIE_SECRET` | Generated by Obot |
| `OBOT_AUTH_PROVIDER_EMAIL_DOMAINS` | Allowed email domains, `*` for any |

Optional (defaults in parentheses):

| Variable | Meaning |
|---|---|
| `OBOT_PINGFEDERATE_AUTH_PROVIDER_CA_CERTIFICATE` | PEM CA chain to trust for PingFederate |
| `OBOT_PINGFEDERATE_AUTH_PROVIDER_CA_FILE` | Path to a PEM CA bundle to trust |
| `OBOT_PINGFEDERATE_AUTH_PROVIDER_SCOPES` | Scopes to request (`openid email profile`); `openid` is always added |
| `OBOT_PINGFEDERATE_AUTH_PROVIDER_EMAIL_CLAIM` | Email claim (`email`) |
| `OBOT_PINGFEDERATE_AUTH_PROVIDER_NAME_CLAIM` | Name claim for the Users Name column (`name`) |
| `OBOT_PINGFEDERATE_AUTH_PROVIDER_EMAIL_DISPLAY_DOMAIN` | UI-only domain suffix, e.g. `company.com` or `@company.com` (empty by default) |
| `OBOT_PINGFEDERATE_AUTH_PROVIDER_GROUPS_CLAIM` | Groups claim (`groups`) |
| `OBOT_PINGFEDERATE_AUTH_PROVIDER_GROUPS_DELIMITER` | Delimiter when groups arrive as one string (none) |
| `OBOT_PINGFEDERATE_AUTH_PROVIDER_KNOWN_GROUPS` | Groups to list in Obot's group picker up front, one per line or comma-separated (none) |
| `OBOT_PINGFEDERATE_AUTH_PROVIDER_ALLOW_UNVERIFIED_EMAIL` | Accept `email_verified: false` (`true`) |
| `OBOT_PINGFEDERATE_AUTH_PROVIDER_CODE_CHALLENGE_METHOD` | `S256`, `plain` or `none` (`S256`) |
| `OBOT_PINGFEDERATE_AUTH_PROVIDER_SKIP_NONCE` | Do not send or verify a nonce (`false`) |
| `OBOT_PINGFEDERATE_AUTH_PROVIDER_EXTRA_AUDIENCES` | Extra accepted `aud` values, comma separated |
| `OBOT_PINGFEDERATE_AUTH_PROVIDER_SKIP_ISSUER_VERIFICATION` | Accept a discovery document whose issuer differs (`false`) |
| `OBOT_PINGFEDERATE_AUTH_PROVIDER_SKIP_DISCOVERY` | Use the endpoints below instead of discovery (`false`) |
| `OBOT_PINGFEDERATE_AUTH_PROVIDER_AUTHORIZATION_URL` | Authorization endpoint (discovery skipped only) |
| `OBOT_PINGFEDERATE_AUTH_PROVIDER_TOKEN_URL` | Token endpoint (discovery skipped only) |
| `OBOT_PINGFEDERATE_AUTH_PROVIDER_JWKS_URL` | JWKS endpoint (discovery skipped only) |
| `OBOT_PINGFEDERATE_AUTH_PROVIDER_USERINFO_URL` | Userinfo endpoint override |
| `OBOT_PINGFEDERATE_AUTH_PROVIDER_USERINFO_CACHE_TTL` | Profile cache lifetime (`5m`), `0` disables |
| `OBOT_AUTH_PROVIDER_TOKEN_REFRESH_DURATION` | Refresh upstream tokens after this age (`1h`) |
| `OBOT_AUTH_PROVIDER_ENABLE_LOGGING` | oauth2-proxy request and auth logging (`false`) |
| `OBOT_AUTH_PROVIDER_POSTGRES_*` | Session store settings, passed by Obot |

## Development

```sh
go test ./...                              # unit tests and an end-to-end login against a mock IdP
PINGFEDERATE_TEST_LOGGING=1 go test ./...  # same, with oauth2-proxy's own logs
go run ./hack/mockidp                      # a standalone mock IdP for trying the provider in Obot
IMAGE=... ./hack/e2e-container.sh          # runs an Obot image against the mock IdP
```

`internal/mockidp` is a small OpenID Connect provider used by the tests. It
serves discovery, JWKS, the authorization code flow with PKCE, refresh tokens
and userinfo, and signs in as a configurable user without a login page.

`hack/e2e-container.sh` drives a real login through a running Obot container, so
it needs `IMAGE` set to an Obot image that bundles this provider.
