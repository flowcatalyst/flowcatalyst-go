#!/usr/bin/env bash
#
# End-to-end check of the auth/2FA/client-admin control surface against a
# running fc-dev (default admin admin@flowcatalyst.local / DevPassword123!).
#
#   ./bin/fc-dev start &        # in one terminal
#   scripts/e2e_auth_controls.sh
#
# Asserts the security controls are wired: reset-approval queue gating, the
# seeded client-admin role, 2FA endpoints, and — the important part — that a
# client-administrator is confined to their own client and blocked from
# anchor-only ops and platform-role assignment (no privilege escalation).
set -uo pipefail
BASE=${BASE:-http://localhost:8080}
PASS=0 FAIL=0

check() { # desc, actual, expected
	if [ "$2" = "$3" ]; then
		printf 'PASS  %-52s [%s]\n' "$1" "$2"; PASS=$((PASS + 1))
	else
		printf 'FAIL  %-52s [got %s, want %s]\n' "$1" "$2" "$3"; FAIL=$((FAIL + 1))
	fi
}

# code METHOD PATH JAR-READ [BODY] → prints the HTTP status.
code() {
	local method=$1 path=$2 jar=$3 body=${4:-}
	if [ -n "$body" ]; then
		curl -s -o /dev/null -w '%{http_code}' -b "$jar" -X "$method" "$BASE$path" \
			-H 'Content-Type: application/json' -d "$body"
	else
		curl -s -o /dev/null -w '%{http_code}' -b "$jar" -X "$method" "$BASE$path"
	fi
}

# loginAs EMAIL PASSWORD JAR-WRITE → prints the HTTP status (and stores cookie).
loginAs() {
	curl -s -o /dev/null -w '%{http_code}' -c "$3" -X POST "$BASE/auth/login" \
		-H 'Content-Type: application/json' \
		-d "{\"email\":\"$1\",\"password\":\"$2\"}"
}

# json METHOD PATH JAR-READ [BODY] → prints the response body.
json() {
	local method=$1 path=$2 jar=$3 body=${4:-}
	if [ -n "$body" ]; then
		curl -s -b "$jar" -X "$method" "$BASE$path" -H 'Content-Type: application/json' -d "$body"
	else
		curl -s -b "$jar" -X "$method" "$BASE$path"
	fi
}

ADMIN=$(mktemp); CA=$(mktemp); NONE=/dev/null
SUFFIX="e2e$(date +%s)"
PW="DevPassword123!"

# ── platform controls ──────────────────────────────────────────────────────
check "admin login" "$(loginAs admin@flowcatalyst.local "$PW" "$ADMIN")" "200"
check "reset-approvals blocked when unauthenticated" "$(code GET /api/reset-approvals "$NONE")" "403"
check "reset-approvals allowed for admin" "$(code GET /api/reset-approvals "$ADMIN")" "200"

check "client-admin role is seeded" \
	"$(json GET /api/roles "$ADMIN" | jq -r '.roles[].name' 2>/dev/null | grep -qx 'platform:client-admin' && echo yes || echo no)" "yes"

check "2FA verify endpoint mounted" \
	"$(code POST /auth/2fa/verify "$NONE" '{"mfaToken":"x","method":"TOTP","code":"000000"}')" "401"

CLIENT=$(json GET /api/clients "$ADMIN" | jq -r '.clients[0].id // empty')
if [ -z "$CLIENT" ]; then
	echo "FAIL  no client to test client-admin scoping against"; FAIL=$((FAIL + 1))
	echo "---"; echo "PASS=$PASS FAIL=$FAIL"; exit 1
fi

# ── seed a client-administrator (CLIENT scope + client-admin role) ──────────
CAEMAIL="cadmin-$SUFFIX@example.test"
CAID=$(json POST /api/principals "$ADMIN" \
	"{\"email\":\"$CAEMAIL\",\"name\":\"E2E Client Admin\",\"scope\":\"CLIENT\",\"clientId\":\"$CLIENT\",\"password\":\"$PW\"}" \
	| jq -r '.id // empty')
check "admin created a CLIENT user" "$([ -n "$CAID" ] && echo yes || echo no)" "yes"
json PUT "/api/principals/$CAID/roles" "$ADMIN" '{"roles":["platform:client-admin"]}' >/dev/null

check "client-admin login" "$(loginAs "$CAEMAIL" "$PW" "$CA")" "200"

# ── client-admin authority + boundaries ─────────────────────────────────────
check "client-admin CAN list reset-approvals (scoped)" "$(code GET /api/reset-approvals "$CA")" "200"

check "client-admin CAN create a CLIENT user in own client" \
	"$(code POST /api/principals "$CA" \
		"{\"email\":\"u-$SUFFIX@example.test\",\"name\":\"U\",\"scope\":\"CLIENT\",\"clientId\":\"$CLIENT\",\"password\":\"$PW\"}")" "201"

check "client-admin BLOCKED from creating an ANCHOR user" \
	"$(code POST /api/principals "$CA" '{"email":"a@example.test","name":"A","scope":"ANCHOR"}')" "403"

check "client-admin BLOCKED from grant-client-access (anchor-only)" \
	"$(code POST "/api/principals/$CAID/client-access" "$CA" "{\"clientId\":\"$CLIENT\"}")" "403"

# create a target user in the client, then try to give them a platform role
TUID=$(json POST /api/principals "$CA" \
	"{\"email\":\"t-$SUFFIX@example.test\",\"name\":\"T\",\"scope\":\"CLIENT\",\"clientId\":\"$CLIENT\",\"password\":\"$PW\"}" \
	| jq -r '.id // empty')
check "client-admin BLOCKED from assigning a platform role" \
	"$(code PUT "/api/principals/$TUID/roles" "$CA" '{"roles":["platform:admin"]}')" "403"

echo "----------------------------------------------------------------------"
echo "PASS=$PASS  FAIL=$FAIL"
[ "$FAIL" -eq 0 ]
