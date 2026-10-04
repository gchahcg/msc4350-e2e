#!/usr/bin/env python3
"""Rename the hashed export names of a rebuilt crypto-wasm module to match a reference build.

wasm-bindgen embeds a compiler-version dependent hash in a few export names
(e.g. wasm_bindgen_36d9b51539b33e43___convert__closures_...). Element's JS glue looks those up by name, so a
module rebuilt with a different rustc needs the reference names. The hashes have equal length, so the names are
rewritten in place, byte for byte.

usage: match_wasm_exports.py REFERENCE.wasm REBUILT.wasm OUT.wasm
"""
import re
import sys


def leb(data, pos):
    result = shift = 0
    while True:
        b = data[pos]
        pos += 1
        result |= (b & 0x7F) << shift
        if not b & 0x80:
            return result, pos
        shift += 7


def export_names(data):
    pos = 8
    while pos < len(data):
        section_id = data[pos]
        size, body = leb(data, pos + 1)
        if section_id == 7:
            count, p = leb(data, body)
            names = []
            for _ in range(count):
                n, p = leb(data, p)
                names.append(data[p:p + n].decode())
                p += n
                p += 1  # kind
                _, p = leb(data, p)  # index
            return names
        pos = body + size
    raise SystemExit("no export section")


ref, rebuilt, out = sys.argv[1:4]
ref_data = open(ref, "rb").read()
new_data = open(rebuilt, "rb").read()
ref_names, new_names = export_names(ref_data), export_names(new_data)
norm = lambda n: re.sub(r"[0-9a-f]{16}", "H", n)
only_ref = [n for n in ref_names if n not in set(new_names)]
only_new = [n for n in new_names if n not in set(ref_names)]
by_norm = {}
for n in only_ref:
    by_norm.setdefault(norm(n), []).append(n)
mapping = {}
for n in only_new:
    candidates = by_norm.get(norm(n), [])
    if len(candidates) != 1 or len(candidates[0]) != len(n):
        raise SystemExit(f"cannot match export {n!r} unambiguously: {candidates}")
    mapping[n] = candidates[0]
# Replace hash tokens (not whole names), so names that merely contain a changed hash stay consistent.
tokens = {}
for new_name, ref_name in mapping.items():
    for a, b in zip(re.findall(r"[0-9a-f]{16}", new_name), re.findall(r"[0-9a-f]{16}", ref_name)):
        if tokens.setdefault(a, b) != b:
            raise SystemExit(f"hash {a} maps to two values")
for a, b in tokens.items():
    new_data = new_data.replace(a.encode(), b.encode())
open(out, "wb").write(new_data)
fixed = export_names(new_data)
left = [n for n in fixed if n not in set(ref_names)]
print(f"renamed {len(mapping)} exports ({len(tokens)} hashes); exports not in reference afterwards: {len(left)}")
sys.exit(1 if left else 0)
