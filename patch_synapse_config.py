#!/usr/bin/env python3
# Copyright (c) 2026 gchahcg
#
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at http://mozilla.org/MPL/2.0/.
"""Patch a generated Synapse homeserver.yaml for the MSC4350 end-to-end tests.

usage: patch_synapse_config.py HOMESERVER_YAML [legacy] [federation] [remote]

"legacy" turns msc4190_enabled off, so appservices can use the legacy appservice login.
"federation" enables federation over TLS with a self-signed certificate and without certificate or IP checks
(test only, between the containers of the test network).
"remote" is for the second homeserver: no appservice registration.
"""
import sys
import yaml

path = sys.argv[1]
modes = set(sys.argv[2:])
legacy = "legacy" in modes
federation = "federation" in modes
remote = "remote" in modes
with open(path) as f:
    cfg = yaml.safe_load(f)

cfg["listeners"] = [{
    "port": 8008,
    "tls": False,
    "type": "http",
    "x_forwarded": False,
    "bind_addresses": ["0.0.0.0"],
    "resources": [{"names": ["client", "federation"], "compress": False}],
}]
if federation:
    cfg["listeners"].append({
        "port": 8448,
        "tls": True,
        "type": "http",
        "bind_addresses": ["0.0.0.0"],
        "resources": [{"names": ["federation"], "compress": False}],
    })
    # The servers find each other through /.well-known/matrix/server on port 443 (which names port 443), because
    # docker's DNS answers the SRV lookup that would otherwise come first with SERVFAIL instead of "no such record".
    cfg["listeners"].append({
        "port": 443,
        "tls": True,
        "type": "http",
        "bind_addresses": ["0.0.0.0"],
        # Synapse's well-known document names port 443, so federation is served there as well.
        "resources": [{"names": ["client", "federation"], "compress": False}],
    })
    cfg["serve_server_wellknown"] = True
    cfg["public_baseurl"] = f"https://{cfg['server_name']}:8448/"
    cfg["tls_certificate_path"] = "/data/tls.crt"
    cfg["tls_private_key_path"] = "/data/tls.key"
    cfg["federation_verify_certificates"] = False
    # The test servers talk to each other over private docker addresses.
    cfg["ip_range_blacklist"] = []
    cfg["federation_ip_range_blacklist"] = []
cfg["database"] = {"name": "sqlite3", "args": {"database": "/data/homeserver.db"}}
cfg["registration_shared_secret"] = "msc4350-e2e-shared-secret"
cfg["enable_registration"] = False
cfg["app_service_config_files"] = [] if remote else ["/data/registration.yaml"]
cfg["suppress_key_server_warning"] = True
cfg["trusted_key_servers"] = []
cfg["report_stats"] = False
# Same flags the live element stack enables for bridge encryption.
cfg["experimental_features"] = {
    "msc4190_enabled": not legacy,
    "msc2409_to_device_messages_enabled": True,
    "msc3202_transaction_extensions": True,
    "msc3983_appservice_otk_claims": True,
    "msc3984_appservice_key_query": True,
}
# The tests create users and send bursts of requests.
cfg["rc_message"] = {"per_second": 1000, "burst_count": 1000}
cfg["rc_registration"] = {"per_second": 1000, "burst_count": 1000}
cfg["rc_login"] = {
    "address": {"per_second": 1000, "burst_count": 1000},
    "account": {"per_second": 1000, "burst_count": 1000},
    "failed_attempts": {"per_second": 1000, "burst_count": 1000},
}
cfg["rc_joins"] = {
    "local": {"per_second": 1000, "burst_count": 1000},
    "remote": {"per_second": 1000, "burst_count": 1000},
}
cfg["rc_invites"] = {
    "per_room": {"per_second": 1000, "burst_count": 1000},
    "per_user": {"per_second": 1000, "burst_count": 1000},
    "per_issuer": {"per_second": 1000, "burst_count": 1000},
}
with open(path, "w") as f:
    yaml.safe_dump(cfg, f, sort_keys=False)
