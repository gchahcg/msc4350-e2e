#!/usr/bin/env python3
# Copyright (c) 2026 gchahcg
#
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at http://mozilla.org/MPL/2.0/.
"""Render the fakebridge config from the generated example config.

usage: render_bridge_config.py EXAMPLE OUT RUN_DIR msc4190=true|false msc4350=true|false
"""
import sys
import yaml

example, out, run_dir = sys.argv[1:4]
opts = dict(a.split("=", 1) for a in sys.argv[4:])

import os

# Re-rendering an existing config keeps the as_token/hs_token generated with the registration.
with open(out if os.path.exists(out) else example) as f:
    cfg = yaml.safe_load(f)

cfg["homeserver"]["address"] = "http://127.0.0.1:18008"
cfg["homeserver"]["domain"] = "test.local"
cfg["appservice"]["address"] = "http://host.docker.internal:29399"
cfg["appservice"]["hostname"] = "0.0.0.0"
cfg["appservice"]["port"] = 29399
cfg["database"] = {
    "type": "sqlite3-fk-wal",
    "uri": f"file:{run_dir}/bridge.db?_txlock=immediate",
    "max_open_conns": 5,
    "max_idle_conns": 1,
}
cfg["bridge"]["permissions"] = {"*": "relay", "test.local": "admin"}
cfg["provisioning"]["shared_secret"] = "disable"
enc = cfg["encryption"]
enc.update({
    "allow": True,
    "default": True,
    "appservice": True,
    "msc4190": opts.get("msc4190", "true") == "true",
    "msc4350": opts.get("msc4350", "true") == "true",
    "self_sign": True,
    "pickle_key": "msc4350-e2e-pickle-key",
})
# JSON on stdout only: run.sh appends stdout to bridge.stdout across restarts, and the tests read that file.
# (The file writer starts a fresh file on every start, which breaks reading logs across restarts.)
cfg["logging"] = {
    "min_level": "debug",
    "writers": [{"type": "stdout", "format": "json"}],
}
with open(out, "w") as f:
    yaml.safe_dump(cfg, f, sort_keys=False)
