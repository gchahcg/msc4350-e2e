// Copyright (c) 2026 gchahcg
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package tests

// Server conformance: MSC4350 requires the homeserver to include the impersonator's signature when returning an
// impersonatable device from /keys/query, regardless of who is querying. These tests query the same ghost device as
// different kinds of users and check that the result is always complete and unmodified.

import (
	"context"
	"database/sql"
	"net/http"
	"os"
	"regexp"
	"slices"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/cryptohelper"
	"maunium.net/go/mautrix/crypto/signatures"
	"maunium.net/go/mautrix/id"
)

// asClient returns a client that acts as the bridge bot, using the appservice token from the generated registration.
func asClient(t *testing.T) *mautrix.Client {
	t.Helper()
	data, err := os.ReadFile(runDir + "/registration.yaml")
	require.NoError(t, err)
	match := regexp.MustCompile(`(?m)^as_token:\s*(\S+)`).FindSubmatch(data)
	require.NotNil(t, match, "no as_token in the registration")
	cli, err := mautrix.NewClient(hsURL, botMXID, string(match[1]))
	require.NoError(t, err)
	cli.SetAppServiceUserID = true
	return cli
}

// T9: a user who shares no room with the ghost sees the complete device.
func TestT9_QuerierSharingNoRoomWithGhost(t *testing.T) {
	requireStack(t)
	_, owner := newUser(t)
	res := inject(t, owner, uniq("ghost"), "hello")

	stranger, _ := newUser(t)
	requireImpersonatableDevice(t, stranger, res.GhostMXID)
}

// T10: a user who has cross-signing keys themselves sees the same, and nothing extra is signed by them.
func TestT10_QuerierWithCrossSigningKeys(t *testing.T) {
	requireStack(t)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()

	_, owner := newUser(t)
	res := inject(t, owner, uniq("ghost"), "hello")

	name := uniq("xsign")
	require.NoError(t, e2eRegister(ctx, name))
	cli, err := mautrix.NewClient(hsURL, "", "")
	require.NoError(t, err)
	helper, err := cryptohelper.NewCryptoHelper(cli, []byte("e2e"), t.TempDir()+"/crypto.db")
	require.NoError(t, err)
	helper.LoginAs = passwordLogin(name)
	require.NoError(t, helper.Init(ctx))
	defer helper.Close()
	_, _, err = helper.Machine().GenerateAndUploadCrossSigningKeysWithPassword(ctx, password, "")
	require.NoError(t, err)

	requireImpersonatableDevice(t, cli, res.GhostMXID)
}

// T11: the bridge bot itself, querying with its appservice token, sees its ghost's device.
func TestT11_BotQueriesItsGhost(t *testing.T) {
	requireStack(t)
	_, owner := newUser(t)
	res := inject(t, owner, uniq("ghost"), "hello")
	requireImpersonatableDevice(t, asClient(t), res.GhostMXID)
}

// T12: asking for the device by ID works, asking for another ID returns nothing, and the ghost shows up in
// /keys/changes for a user who joins its room afterwards.
func TestT12_ExplicitDeviceListAndKeyChanges(t *testing.T) {
	requireStack(t)
	ctx := t.Context()
	cli, owner := newUser(t)

	// A baseline sync token from before the ghost exists.
	sync, err := cli.SyncRequest(ctx, 0, "", "", false, "")
	require.NoError(t, err)
	since := sync.NextBatch

	ghost := uniq("ghost")
	res := inject(t, owner, ghost, "hello")
	botDevID := requireImpersonatableDevice(t, cli, res.GhostMXID)

	query := func(devices ...id.DeviceID) rawDevices {
		var resp struct {
			DeviceKeys map[id.UserID]rawDevices `json:"device_keys"`
		}
		_, err := cli.MakeRequest(ctx, http.MethodPost, cli.BuildClientURL("v3", "keys", "query"),
			map[string]any{"device_keys": map[id.UserID][]id.DeviceID{res.GhostMXID: devices}}, &resp)
		require.NoError(t, err)
		return resp.DeviceKeys[res.GhostMXID]
	}
	assert.Contains(t, query(botDevID), botDevID, "asking for the ghost's device by ID should return it")
	assert.Empty(t, query("NOSUCHDEVICE"), "asking for another device ID should return nothing")

	// A user who joins a room with the ghost learns about its device through /keys/changes, which is how clients
	// find out that they should query the ghost's keys.
	_, err = cli.JoinRoomByID(ctx, res.RoomID)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		sync, err := cli.SyncRequest(ctx, 0, "", "", false, "")
		if err != nil {
			return false
		}
		// Not cli.GetKeyChanges: it sends a POST, but /keys/changes is a GET in the spec.
		var changes mautrix.RespKeyChanges
		_, err = cli.MakeRequest(ctx, http.MethodGet, cli.BuildURLWithQuery(mautrix.ClientURLPath{"v3", "keys", "changes"},
			map[string]string{"from": since, "to": sync.NextBatch}), nil, &changes)
		return err == nil && slices.Contains(changes.Changed, res.GhostMXID)
	}, 20*time.Second, 500*time.Millisecond, "the ghost's device should appear in /keys/changes")
}

// T13 (keep it last: it gives the bot a second device, which tests that expect exactly one bot device don't handle):
// observation, not a requirement. When the bridge gets a new device (here: its crypto account is wiped, as
// after a crypto reset), the ghost registers a new impersonatable device on its next encrypted message, and the old
// one stays on the server. Clients that validate the old one would still find the old bot device's keys in it.
func TestT13_BotDeviceChangeLeavesOldGhostDeviceBehind(t *testing.T) {
	requireStack(t)
	cli, owner := newUser(t)
	ghost := uniq("ghost")
	res := inject(t, owner, ghost, "before the reset")
	oldDevID := requireImpersonatableDevice(t, cli, res.GhostMXID)

	// Wipe the bridge's crypto account while it is stopped, so it logs in with a new device on the next start.
	runScript(t, "bridge-stop")
	t.Cleanup(func() { runScript(t, "bridge", "default") })
	db, err := sql.Open("sqlite3", "file:"+runDir+"/bridge.db?_txlock=immediate")
	require.NoError(t, err)
	for _, table := range []string{"crypto_account", "crypto_olm_session", "crypto_megolm_outbound_session"} {
		_, err = db.Exec("DELETE FROM " + table)
		require.NoError(t, err)
	}
	require.NoError(t, db.Close())
	runScript(t, "bridge", "default")

	inject(t, owner, ghost, "after the reset")

	devs := queryKeys(t, cli, res.GhostMXID, botMXID)
	require.Len(t, devs[res.GhostMXID], 2, "the ghost should now have its old and its new impersonatable device")
	var newDevID id.DeviceID
	for devID := range devs[res.GhostMXID] {
		if devID != oldDevID {
			newDevID = devID
		}
	}
	require.NotEmpty(t, newDevID)

	// The new ghost device is valid against the bot's new device.
	newBotKeys := decodeObject(t, devs[botMXID][newDevID])
	botEd25519 := id.Ed25519(newBotKeys["keys"].(map[string]any)["ed25519:"+string(newDevID)].(string))
	valid, err := signatures.VerifySignatureJSON(devs[res.GhostMXID][newDevID], botMXID, string(newDevID), botEd25519)
	require.NoError(t, err)
	assert.True(t, valid, "the new ghost device must be signed by the bot's new device")

	// The old ghost device is untouched: still there, and still pointing at the old bot device.
	oldGhost := decodeObject(t, devs[res.GhostMXID][oldDevID])
	oldImpersonator := oldGhost[impersonatorKey].(map[string]any)
	assert.Equal(t, string(oldDevID), oldImpersonator["device_id"])
	t.Logf("old ghost device %s still present after the bot moved to device %s, impersonator device_id=%v",
		oldDevID, newDevID, oldImpersonator["device_id"])
}
