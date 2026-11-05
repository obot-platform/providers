#!/usr/bin/env bash
# Runs an Obot image against the mock identity provider and performs a
# real login through Obot's own OAuth plumbing, then checks what Obot learned
# about the user. Needs docker, curl, openssl, python3 and go.
#
# IMAGE must be an Obot image that bundles this provider, i.e. one built against
# a providers image containing pingfederate-auth-provider:
#
#   IMAGE=ghcr.io/obot-platform/obot:latest ./hack/e2e-container.sh
set -euo pipefail

IMAGE=${IMAGE:?set IMAGE to an Obot image that bundles pingfederate-auth-provider}
OBOT_PORT=${OBOT_PORT:-18080}
IDP_PORT=${IDP_PORT:-9400}
CONTAINER=${CONTAINER:-obot-pf-e2e}
WORKDIR=$(mktemp -d)
cd "$(dirname "$0")/.."

# The mock IdP must be reachable under one hostname both from inside the Obot
# container (the provider talks to it) and from this host, which plays the
# browser. Containers reach the host as host.docker.internal; curl on the host
# is told to resolve that name to loopback.
IDP_HOST=host.docker.internal
ISSUER="http://${IDP_HOST}:${IDP_PORT}"
CURL=(curl -s --max-time 15 --resolve "${IDP_HOST}:${IDP_PORT}:127.0.0.1")
BOOTSTRAP=$(openssl rand -hex 16)

# json EXPR reads JSON on stdin and prints a Python expression over it as d.
json() { python3 -c 'import json,sys; d=json.load(sys.stdin); print(eval(sys.argv[1]))' "$1"; }
# expect EXPR fails unless the expression over the JSON on stdin is true.
expect() { test "$(json "$1")" = "True" || { echo "    FAILED: $1"; return 1; }; }

cleanup() {
  status=$?
  echo "--- obot provider log lines"
  docker logs "$CONTAINER" 2>&1 | grep -i -E "pingfederate|provider daemon|auth provider" | tail -n 20 || true
  if [ "$status" -ne 0 ]; then
    echo "--- last obot log lines (container $CONTAINER is kept for inspection)"
    docker logs "$CONTAINER" 2>&1 | tail -n 15 || true
  else
    docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
  fi
  kill "${IDP_PID:-}" 2>/dev/null || true
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

echo "--- starting mock identity provider at $ISSUER"
go build -o "$WORKDIR/mockidp" ./hack/mockidp
"$WORKDIR/mockidp" -listen "0.0.0.0:${IDP_PORT}" -issuer "$ISSUER" > "$WORKDIR/mockidp.log" 2>&1 &
IDP_PID=$!
for _ in $(seq 1 15); do
  "${CURL[@]}" -f "$ISSUER/.well-known/openid-configuration" > /dev/null && break
  sleep 1
done
"${CURL[@]}" -f "$ISSUER/.well-known/openid-configuration" | expect "d['issuer'] == '$ISSUER'"

echo "--- starting $IMAGE"
docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
# The Docker socket is what the upstream quick start mounts; Obot's MCP
# backend refuses to start without it.
docker run -d --name "$CONTAINER" -p "${OBOT_PORT}:8080" --add-host=host.docker.internal:host-gateway \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -e OBOT_SERVER_ENABLE_AUTHENTICATION=true \
  -e OBOT_BOOTSTRAP_TOKEN="$BOOTSTRAP" \
  -e OBOT_SERVER_HOSTNAME="http://127.0.0.1:${OBOT_PORT}" \
  "$IMAGE" > /dev/null
OBOT="http://127.0.0.1:${OBOT_PORT}"
for _ in $(seq 1 120); do
  "${CURL[@]}" -f "$OBOT/api/healthz" > /dev/null 2>&1 && break
  sleep 2
done
"${CURL[@]}" -f "$OBOT/api/healthz" > /dev/null

echo "--- provider is registered"
"${CURL[@]}" -f "$OBOT/api/auth-providers" | expect "any(p['id'] == 'pingfederate-auth-provider' and p['name'] == 'PingFederate' for p in d['items'])"
echo "    ok"

echo "--- configuring the provider"
"${CURL[@]}" -f -X POST -H "Authorization: Bearer $BOOTSTRAP" -H "Content-Type: application/json" \
  "$OBOT/api/auth-providers/pingfederate-auth-provider/configure" \
  -d "{\"OBOT_PINGFEDERATE_AUTH_PROVIDER_CLIENT_ID\":\"obot\",\"OBOT_PINGFEDERATE_AUTH_PROVIDER_CLIENT_SECRET\":\"obot-secret\",\"OBOT_PINGFEDERATE_AUTH_PROVIDER_ISSUER_URL\":\"$ISSUER\",\"OBOT_AUTH_PROVIDER_EMAIL_DOMAINS\":\"*\",\"OBOT_PINGFEDERATE_AUTH_PROVIDER_KNOWN_GROUPS\":\"LDAP-Obot-Admins\\nLDAP-Obot-Auditors\"}" > /dev/null
for _ in $(seq 1 60); do
  "${CURL[@]}" -f "$OBOT/api/auth-providers/pingfederate-auth-provider" | expect "d.get('configured') == True" > /dev/null 2>&1 && break
  sleep 1
done
"${CURL[@]}" -f "$OBOT/api/auth-providers/pingfederate-auth-provider" | json "{k: d.get(k) for k in ('configured', 'missingConfigurationParameters', 'error')}"

echo "--- assigning Admin + Auditor (48) to the mock user's 'admins' group before they sign in"
"${CURL[@]}" -f -X POST -H "Authorization: Bearer $BOOTSTRAP" -H "Content-Type: application/json" \
  "$OBOT/api/group-role-assignments" \
  -d '{"groupName":"pingfederate/admins","role":48,"description":"e2e"}' > /dev/null
"${CURL[@]}" -f -H "Authorization: Bearer $BOOTSTRAP" "$OBOT/api/group-role-assignments" | expect "any(a['groupName'] == 'pingfederate/admins' and a['role'] == 48 for a in d['items'])"
echo "    ok"

echo "--- logging in through Obot"
JAR="$WORKDIR/cookies"
FINAL=$("${CURL[@]}" -L -c "$JAR" -b "$JAR" -o "$WORKDIR/final.html" -w '%{http_code} %{url_effective}' "$OBOT/oauth2/start?rd=%2F")
echo "    final: $FINAL"
grep -q "obot_access_token" "$JAR" || { echo "no session cookie after login"; cat "$WORKDIR/mockidp.log"; exit 1; }

echo "--- /api/me"
ME=$("${CURL[@]}" -f -b "$JAR" "$OBOT/api/me")
echo "$ME" | json "{k: d.get(k) for k in ('id', 'username', 'email', 'displayName', 'role', 'effectiveRole', 'authProviderGroups')}"
echo "$ME" | expect "d['email'] == 'ada@example.com'"

echo "--- the group role assignment reaches the user"
dump_state() {
  echo "    users / memberships / assignments / groups in the database:"
  docker exec "$CONTAINER" psql -U obot -d obot -At \
    -c "select 'user', id, email, role from users" \
    -c "select 'membership', user_id, group_id from group_memberships" \
    -c "select 'assignment', group_name, role from group_role_assignments" \
    -c "select 'group', id, auth_provider_name from groups" | sed 's/^/      /'
  docker logs "$CONTAINER" 2>&1 | grep -i -E "group" | grep -i -E "warn|error|fail" | tail -n 5 | sed 's/^/      /'
}
# "role" is the role granted to the user directly; "effectiveRole" adds the
# roles of the groups they belong to, which is what Obot enforces.
for _ in $(seq 1 10); do
  ME=$("${CURL[@]}" -f -b "$JAR" "$OBOT/api/me")
  echo "$ME" | expect "d.get('effectiveRole', 0) & 48 == 48 and 'pingfederate/admins' in d.get('authProviderGroups', [])" > /dev/null 2>&1 && break
  sleep 3
done
echo "$ME" | expect "d.get('effectiveRole', 0) & 48 == 48 and 'pingfederate/admins' in d.get('authProviderGroups', [])" || { echo "    role=$(echo "$ME" | json "d.get('role')") effectiveRole=$(echo "$ME" | json "d.get('effectiveRole')")"; dump_state; exit 1; }
echo "    ok (effective role $(echo "$ME" | json "d['effectiveRole']"), groups $(echo "$ME" | json "d['authProviderGroups']"))"

echo "--- group search for a signed-in owner lists declared and seen groups (the UI picker's source)"
GROUPS_JSON=$("${CURL[@]}" -f -b "$JAR" "$OBOT/api/groups?limit=50")
echo "$GROUPS_JSON" | json "[(g['id'], g.get('name')) for g in d['items']]"
echo "$GROUPS_JSON" | expect "{'pingfederate/admins', 'pingfederate/devs', 'pingfederate/LDAP-Obot-Admins', 'pingfederate/LDAP-Obot-Auditors'} <= {g['id'] for g in d['items']} and d.get('source') == 'provider' and not d.get('degraded')"
FILTERED=$("${CURL[@]}" -f -b "$JAR" "$OBOT/api/groups?name=ldap&limit=50")
echo "$FILTERED" | expect "[g['id'] for g in d['items']] == ['pingfederate/LDAP-Obot-Admins', 'pingfederate/LDAP-Obot-Auditors']"
echo "    ok"

echo "--- provider sessions are stored in Obot's PostgreSQL (table prefix pingfederate_)"
docker exec "$CONTAINER" psql -U obot -d obot -At -c "select count(*) from information_schema.tables where table_name like 'pingfederate_%'" | grep -q -v '^0$' || { echo "    FAILED: no pingfederate_ session tables"; exit 1; }
docker exec "$CONTAINER" psql -U obot -d obot -At -c "select table_name from information_schema.tables where table_name like 'pingfederate_%' order by 1" | sed 's/^/    /'

echo "--- obot logged no provider warnings"
if docker logs "$CONTAINER" 2>&1 | grep -E '"severity":"(WARNING|ERROR)"' | grep -i -E "auth provider|pingfederate|groups"; then
  echo "    FAILED: unexpected warnings or errors about the auth provider"
  exit 1
fi
echo "    ok"

echo "--- mock identity provider saw"
grep -E "^[0-9:]+ (GET|POST) " "$WORKDIR/mockidp.log" | awk '{print $2, $3}' | sort | uniq -c

echo "PASS"
