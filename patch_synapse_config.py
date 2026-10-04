#!/usr/bin/env python3
"""Patch a generated Synapse homeserver.yaml for the MSC4350 end-to-end tests.

usage: patch_synapse_config.py HOMESERVER_YAML [legacy]

"legacy" turns msc4190_enabled off, so appservices can use the legacy appservice login.
"""
import sys
import yaml

path = sys.argv[1]
legacy = len(sys.argv) > 2 and sys.argv[2] == "legacy"
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
cfg["database"] = {"name": "sqlite3", "args": {"database": "/data/homeserver.db"}}
cfg["registration_shared_secret"] = "msc4350-e2e-shared-secret"
cfg["enable_registration"] = False
cfg["app_service_config_files"] = ["/data/registration.yaml"]
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
