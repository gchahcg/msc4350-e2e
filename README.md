# MSC4350 end-to-end tests

Automated version of runbook Chunk 7 for the `msc4350-ghost-impersonation` branch of mautrix-go.
It brings up a throwaway Synapse in Docker, runs a tiny fake bridgev2 bridge built from the local
branch of the fork (see the `replace` in `go.mod`, which pins the commit under test), and asserts on `/keys/query`,
the bridge logs and the bridge database. Nothing in `/opt/stacks/element` is touched.

```
./run.sh all            # up, run all scenarios, tear down (exit code = test result)
./run.sh up             # just bring the stack up (then ./run.sh test [-run T2], ./run.sh down)
./run.sh keep           # stack + Element over self-signed HTTPS, for the one-off manual look
./run.sh say [text]     # (keep mode) send an encrypted message from a bridge ghost to alice
./run.sh down           # tear down; logs of the last run are kept in artifacts/last/
```

## Scenarios (tests/e2e_test.go)

| Test | Runbook | What it checks |
|---|---|---|
| T1 | step 1 | first message from a new ghost logs exactly one registration; a second message logs none |
| T2 | step 2 | `/keys/query`: ghost device ID = bot device ID, `algorithms: []`, `keys: {}`, only the bot signed it, impersonator == bot's device keys minus signatures, bot signature verifies and fails on tampering |
| T3 | step 3 (API part), client | a real client (cryptohelper) decrypts the ghost's message, the encrypted event's `device_id` is the ghost's registered device, and the client **trusts** the message (it validates the impersonation) |
| T4 | step 4 | bridge restart: no second registration, same single ghost device, one `crypto_impersonatable_device` row |
| T5 | step 5 | `msc4350` without `msc4190`: startup warning, no registration, no ghost devices, messages still delivered |
| T6 | extra | 8 concurrent first messages from one ghost register it once (singleflight) |
| T7 | extra | negative control: `msc4350: false` means no ghost devices |
| T8 | extra | the bot's own device keeps its keys and is not impersonatable |

| T9 | server | a user who shares no room with the ghost gets the complete device |
| T10 | server | a user with cross-signing keys gets the same, signed only by the bot |
| T11 | server | the bridge bot itself (appservice token) gets its ghost's device |
| T12 | server | explicit device ID query, other ID returns nothing, ghost appears in `/keys/changes` after joining |
| T14 | client | negative control: with `msc4350` off the receiving client doesn't trust the ghost's message (unknown device) |
| T13 | observation | after the bot's device changes, the ghost registers a new device and the old one stays behind (documented, not a requirement) |
| F1 | federation | remote user joins after registration: the remote homeserver fetches the device over federation |
| F2 | federation | a new ghost registers after the remote user is in the room: the remote homeserver learns about it via device list updates / resync |
| F3 | federation | a client on the remote homeserver decrypts the ghost's message, which references the ghost device as the remote homeserver reports it |

The F tests need the second homeserver: `E2E_FEDERATION=1 ./run.sh all` (they skip otherwise).

Sanity check of the harness itself: `./run.sh up nomsc4350 && ./run.sh test` makes T1, T2, T3 and T6 fail.

## Server conformance (MSC4350: "servers MUST include signatures from the impersonator user in /keys/query responses, in both the C-S and S-S APIs, regardless of who is querying")

Tested against Synapse `matrixdotorg/synapse@sha256:7155ddc4835e5b8afa4e1598d92aa16ff1d03109f7d1cff61d493237d1210b1d`
(the image the live element stack runs). Synapse needs no change: it builds each device in `/keys/query` (and in the
federation `user/keys/query`) from the JSON the ghost uploaded, so the bot's signature and the unknown
`fi.mau.msc4350.impersonator` field come back exactly as uploaded
(`get_e2e_device_keys_and_signatures` and `_get_e2e_device_keys_for_federation_query_inner` in
`synapse/storage/databases/main/end_to_end_keys.py`).

| Querier | Result |
|---|---|
| user sharing a room with the ghost (T2) | complete device, bot signature verifies |
| user sharing no room (T9) | same |
| user with cross-signing keys (T10) | same, nothing extra signed |
| the bot itself (T11) | same |
| explicit device ID / unknown device ID (T12) | returned / empty |
| remote user on a second Synapse, joined after registration (F1) | same, fetched over federation (`user/keys/query`) |
| remote user, ghost registered later (F2) | same, learned via device list update / resync |
| decrypting client on the remote homeserver (F3) | message decrypts, `device_id` matches the remote server's view of the ghost device |

Quirks: Synapse adds an empty `"signatures": {"<ghost>": {}}` entry (not a signature; tests ignore empty signer maps)
and its own `unsigned.device_display_name`. After the bot's device changes the old ghost device stays (T13).
Not covered: other homeserver implementations.

How the federation setup works: two Synapses (`test.local`, `b.test.local`) on one docker network, TLS with a
self-signed certificate and certificate/IP checks disabled (test only). They find each other through
`/.well-known/matrix/server` on port 443, which also serves federation, because docker's DNS answers the SRV lookup
that would come first with SERVFAIL.


## Client conformance (MSC4350: "a client that validates impersonation requirements")

Three receiving implementations were exercised; none of the first two is merged anywhere yet.

| Client | Where | Result |
|---|---|---|
| mautrix-go crypto (`crypto/impersonation_receive.go`) | branch `msc4350-receive-validation` of the fork, stacked on the bridge branch | T3 (same homeserver) and F3 (client on another homeserver, keys fetched over federation) assert a trusted state; T14 (feature off) stays "unknown device"; unit tests cover each rule |
| Element Web 1.12.29 with a patched matrix-rust-sdk crypto | local patch, not published | the shield disappears for ghost messages once the ghost's device is known; the stock crypto still shows "The sender of the event does not match the owner of the device who sent it" |
| matrix-rust-sdk `matrix-sdk-crypto` (feature `experimental-msc4350`) | local branch of a matrix-rust-sdk checkout | 477 tests pass with the feature (465 without); rustfmt and clippy clean; every rule has a test and was mutation-checked |

All three apply the MSC's rules: the impersonating bot's device signed the ghost's device, the embedded impersonator is the bot's real device, the bot's device is cross-signed, the bot is in the room (mautrix-go enforces it, the rust crypto crate only through an optional hook, because it has no room membership), and, if the ghost has cross-signing keys, the ghost's self-signing key signed the device.

### Cross-implementation test vector

`vectors/rust_ghost_device.json` is a ghost device and the bot device keys produced and signed by matrix-rust-sdk
(`cargo test -p matrix-sdk-crypto --features experimental-msc4350 test_print_test_vector -- --ignored --nocapture`).
mautrix-go's receive tests verify it, so a verifier written in another language accepts a signature produced by Rust
code (and the harness shows the reverse: Rust code accepting devices produced by mautrix-go).

### Notes for client implementers (from trying it in Element)

- **Re-checking.** Element decides a message's shield when it first decrypts it, which for a freshly joined room is
  before the `/keys/query` for the room's members returns. Element re-checks on `Decrypted` and
  `UserTrustStatusChanged`, and js-sdk emits the latter only for cross-signing identity updates. A ghost has no
  identity, so a device that arrives later doesn't refresh an already displayed message. A fix needs a small change
  in Element/js-sdk (also re-check on `DevicesUpdated` for the sender, and have the crypto library report impersonatable
  device changes through the device updates stream), or the library needs to answer synchronously.
- **Persistence.** Impersonatable devices must be remembered across restarts. The rust patch stores them in the crypto
  store's generic custom values (no schema change); the mautrix-go version refetches lazily.
- **Room membership.** The rule that the impersonator must be in the room needs membership data that a crypto library
  usually doesn't have. The rust patch takes an optional callback; embedding SDKs should supply it.

## Layout

- `compose.yml`: Synapse (same image digest as the live stack), plus optional Element and Caddy (`--profile element`).
- `patch_synapse_config.py`: patches a generated `homeserver.yaml` (same experimental flags as the live stack, no MAS, no rate limits). `legacy` mode turns `msc4190_enabled` off.
- `render_bridge_config.py`: renders the fakebridge config. Variants: `default`, `nomsc4350`, `nomsc4190`.
- `fakebridge/`: `mxmain.BridgeMain` plus a minimal connector. `POST /inject` makes a remote "ghost" send a message to a Matrix user, which creates the portal and an encrypted room.
- `tests/`: scenarios and helpers. `internal/e2e`: user registration and login helpers.

## Differences from the live stack

- No MAS (MSC3861); plain Synapse with SQLite. Appservice masquerading and MSC4190 behave the same, but this has not been run against the MAS-delegated live homeserver.
- T5 needs a homeserver in "legacy" mode (`msc4190_enabled: false` and an appservice registration without `io.element.msc4190`), because with MSC4190 on, a bridge configured without it cannot even log in.

## Receive-side check in a real client (Element Web with a patched crypto)

Element's crypto (matrix-rust-sdk, compiled to wasm) doesn't implement the receiving side of MSC4350 yet, so Element
keeps showing "The sender of the event does not match the owner of the device who sent it" for bridged messages.
`./run.sh keep` brings up Element behind a self-signed HTTPS proxy, and `./run.sh wasm <file|stock>` swaps its crypto
module for a build you provide, which is how a patched matrix-rust-sdk was tried against these tests. The patch
itself is a prototype and isn't published here.

```
./run.sh keep                  # stack + Element over HTTPS (login alice / alicepw, server test.local)
./run.sh say "text" ghostname  # a new ghost per message makes a fresh room and key query
./run.sh wasm <file>           # serve Element with your own crypto wasm
./run.sh wasm stock            # back to the stock module
```

`tools/match_wasm_exports.py` renames the few compiler-hash dependent exports of a wasm built with a different rustc
than Element's, so it loads with Element's unchanged JS glue.

Notes from trying a receive-side implementation: Element decides a message's shield when it first decrypts it, which
for a freshly joined room is before the `/keys/query` for its members returns, so a re-check on device-list changes
is needed. A client also needs room-membership data to enforce the MSC's "impersonator is in the room" rule.

## Requirements

Docker (with compose), Go 1.26+, Python 3 with PyYAML, libolm headers (or build with the goolm tag), and curl.
The test homeserver and credentials are throwaway and only listen on localhost.

## License

Mozilla Public License 2.0, the same as mautrix-go. See `LICENSE`.
