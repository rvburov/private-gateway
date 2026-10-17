#!/usr/bin/env python3
"""Функциональная проверка локального API для разработки. Секреты никогда не печатает."""
import json
import os
import secrets
import urllib.error
import urllib.parse
import urllib.request

base = os.environ.get("GATEWAY_API_URL", "http://127.0.0.1:8080")
token = os.environ["GATEWAY_API_TOKEN"]
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def request(method, path, body=None, authorized=True, expected=200):
    headers = {"Content-Type": "application/json"}
    if authorized:
        headers["Authorization"] = "Bearer " + token
    req = urllib.request.Request(base + path, method=method, headers=headers,
                                 data=json.dumps(body).encode() if body is not None else None)
    try:
        response = opener.open(req, timeout=70)
    except urllib.error.HTTPError as exc:
        assert exc.code == expected, f"{method} {path}: unexpected HTTP {exc.code}"
        exc.close()
        return None
    with response:
        assert response.status == expected
        assert response.headers.get("Cache-Control") == "no-store"
        return json.load(response)


request("GET", "/readyz", authorized=False)
request("GET", "/api/v1/devices", authorized=False, expected=401)
device = request("POST", "/api/v1/devices", {"name": "smoke-" + secrets.token_hex(4)}, expected=201)
path = "/api/v1/devices/" + device["id"]
profile = request("GET", path + "/profile")
uri = urllib.parse.urlparse(profile["uri"])
assert uri.scheme == "vless" and urllib.parse.parse_qs(uri.query)["security"] == ["reality"]
assert "credential" not in json.dumps(device)
request("POST", "/api/v1/config/validate")
request("POST", "/api/v1/config/apply")
status = request("GET", "/api/v1/status")
assert status["desired_revision"] == status["applied_revision"]
request("POST", path + "/revoke")
request("GET", path + "/profile", expected=410)
request("POST", "/api/v1/config/apply")
status = request("GET", "/api/v1/status")
assert status["desired_revision"] == status["applied_revision"]
audit = request("GET", "/api/v1/audit")
assert uri.username not in json.dumps(audit)
with opener.open(base + "/", timeout=5) as response:
    assert b'<div id="root">' in response.read()
print("API smoke passed: database, auth, create/export/revoke, real Xray validate/apply, audit, web assets.")
