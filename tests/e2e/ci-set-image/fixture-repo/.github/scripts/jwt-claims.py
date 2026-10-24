#!/usr/bin/env python3
"""Prints the header and payload of the JWT in $JWT as JSON, never the
signature or the encoded token, and checks the expected claims."""
import base64
import json
import os
import sys


def part(s):
    return json.loads(base64.urlsafe_b64decode(s + "=" * (-len(s) % 4)))


jwt = os.environ["JWT"]
header_b64, payload_b64, _signature = jwt.split(".")
header, payload = part(header_b64), part(payload_b64)
print("header:", json.dumps(header, sort_keys=True))
print("payload:", json.dumps(payload, sort_keys=True))

want = {
    "iss": os.environ.get("WANT_ISS"),
    "aud": os.environ.get("WANT_AUD"),
    "sub": os.environ.get("WANT_SUB"),
}
failed = False
for claim, value in want.items():
    if not value:
        continue
    got = payload.get(claim)
    ok = got == value or (isinstance(got, list) and value in got)
    print(f"CHECK {claim} {'PASS' if ok else 'FAIL'} (want {value!r}, got {got!r})")
    failed |= not ok
if header.get("alg") != "RS256" or not header.get("kid"):
    print(f"CHECK header FAIL (alg={header.get('alg')!r}, kid present={bool(header.get('kid'))})")
    failed = True
else:
    print("CHECK header PASS (RS256 with kid)")
lifetime = payload.get("exp", 0) - payload.get("iat", 0)
print(f"CHECK lifetime {'PASS' if 0 < lifetime <= 3600 else 'FAIL'} ({lifetime}s)")
failed |= not (0 < lifetime <= 3600)
sys.exit(1 if failed else 0)
