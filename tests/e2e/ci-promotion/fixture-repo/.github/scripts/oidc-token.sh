#!/usr/bin/env bash
# Prints an ermite OIDC ID token for AUDIENCE ($1) on stdout and masks it.
# The token never reaches the log: callers capture it in a variable.
set -euo pipefail
aud="${1:?audience}"
[ -n "${ACTIONS_ID_TOKEN_REQUEST_URL:-}" ] || { echo "::error::no ACTIONS_ID_TOKEN_REQUEST_URL (untrusted run or no id-token: write)" >&2; exit 1; }
enc="$(python3 -c 'import sys, urllib.parse; print(urllib.parse.quote(sys.argv[1], safe=""))' "$aud")"
resp="$(curl -fsS -H "Authorization: Bearer $ACTIONS_ID_TOKEN_REQUEST_TOKEN" "${ACTIONS_ID_TOKEN_REQUEST_URL}&audience=${enc}")"
jwt="$(printf '%s' "$resp" | jq -r '.value')"
echo "::add-mask::$jwt" >&2
printf '%s' "$jwt"
