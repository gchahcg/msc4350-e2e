// Copyright (c) 2026 gchahcg
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

// Package tests contains the MSC4350 end-to-end scenarios. Run them through ./run.sh test (or ./run.sh all),
// which brings up the isolated homeserver and the fake bridge first.
package tests

import (
	"context"
	"database/sql"
	"encoding/json"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/cryptohelper"
	"maunium.net/go/mautrix/crypto/signatures"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

const impersonatorKey = "fi.mau.msc4350.impersonator"

// botDevice returns the bot's only device ID and the raw device keys of the bot.
func botDevice(t *testing.T, cli *mautrix.Client) (id.DeviceID, map[string]any) {
	t.Helper()
	devs := queryKeys(t, cli, botMXID)[botMXID]
	require.Len(t, devs, 1, "bot should have exactly one device")
	for devID, raw := range devs {
		return devID, decodeObject(t, raw)
	}
	panic("unreachable")
}

func sortedStrings(t *testing.T, v any) []string {
	t.Helper()
	list, ok := v.([]any)
	require.True(t, ok, "expected a list, got %T", v)
	out := make([]string, 0, len(list))
	for _, item := range list {
		out = append(out, item.(string))
	}
	slices.Sort(out)
	return out
}

// requireImpersonatableDevice asserts that the ghost has exactly one device, that it matches the MSC4350 shape,
// and that it is bound to the bot's device. It returns the ghost's device ID.
func requireImpersonatableDevice(t *testing.T, cli *mautrix.Client, ghost id.UserID) id.DeviceID {
	t.Helper()
	botDevID, botKeys := botDevice(t, cli)
	devs := queryKeys(t, cli, ghost)[ghost]
	require.Len(t, devs, 1, "ghost should have exactly one device")
	raw, ok := devs[botDevID]
	require.True(t, ok, "ghost device ID should equal the bot's device ID %s", botDevID)
	dk := decodeObject(t, raw)

	// Nothing may be added to or dropped from the uploaded object, apart from the server's own `unsigned` data.
	for field := range dk {
		assert.Contains(t, []string{"algorithms", "device_id", impersonatorKey, "keys", "signatures", "unsigned", "user_id"}, field,
			"unexpected field in the ghost device keys")
	}
	for _, field := range []string{"algorithms", "device_id", impersonatorKey, "keys", "signatures", "user_id"} {
		assert.Contains(t, dk, field, "missing field in the ghost device keys")
	}

	assert.Equal(t, string(ghost), dk["user_id"])
	assert.Equal(t, string(botDevID), dk["device_id"])
	assert.Equal(t, []any{}, dk["algorithms"], "ghost device must advertise no algorithms")
	assert.Equal(t, map[string]any{}, dk["keys"], "ghost device must have no keys")

	// Only the bot may have signed it. Synapse adds an empty entry for the device owner, which is not a signature.
	signers := map[string]map[string]any{}
	for user, sigs := range dk["signatures"].(map[string]any) {
		if m := sigs.(map[string]any); len(m) > 0 {
			signers[user] = m
		}
	}
	require.Len(t, signers, 1, "ghost device must be signed by exactly one user")
	botSigs, ok := signers[string(botMXID)]
	require.True(t, ok, "ghost device must be signed by the bot, got %v", slices.Collect(maps.Keys(signers)))
	require.Len(t, botSigs, 1)
	require.Contains(t, botSigs, "ed25519:"+string(botDevID))

	// The impersonator is the bot's device keys without signatures (and without server-added unsigned data).
	imp, ok := dk[impersonatorKey].(map[string]any)
	require.True(t, ok, "ghost device must carry %s", impersonatorKey)
	assert.NotContains(t, imp, "signatures")
	assert.Equal(t, botKeys["user_id"], imp["user_id"])
	assert.Equal(t, botKeys["device_id"], imp["device_id"])
	assert.Equal(t, sortedStrings(t, botKeys["algorithms"]), sortedStrings(t, imp["algorithms"]))
	assert.Equal(t, botKeys["keys"], imp["keys"])

	// The bot's signature must verify with the bot's own signing key, and fail on tampering.
	botEd25519 := id.Ed25519(botKeys["keys"].(map[string]any)["ed25519:"+string(botDevID)].(string))
	valid, err := signatures.VerifySignatureJSON(raw, botMXID, string(botDevID), botEd25519)
	require.NoError(t, err)
	assert.True(t, valid, "bot signature on the ghost device did not verify")
	tampered := decodeObject(t, raw)
	tampered[impersonatorKey].(map[string]any)["device_id"] = "TAMPERED"
	tamperedRaw, err := json.Marshal(tampered)
	require.NoError(t, err)
	valid, err = signatures.VerifySignatureJSON(json.RawMessage(tamperedRaw), botMXID, string(botDevID), botEd25519)
	require.NoError(t, err)
	assert.False(t, valid, "signature verified on tampered device keys")
	return botDevID
}

func requireNoDevices(t *testing.T, cli *mautrix.Client, ghost id.UserID) {
	t.Helper()
	assert.Empty(t, queryKeys(t, cli, ghost)[ghost], "ghost %s should have no devices", ghost)
}

// T1: runbook step 1.
func TestT1_SingleRegistrationForNewGhost(t *testing.T) {
	requireStack(t)
	cli, user := newUser(t)
	ghost := uniq("ghost")
	offset := logOffset(t)

	res := inject(t, user, ghost, "first message")
	require.Len(t, registrations(t, offset, res.GhostMXID), 1, "expected exactly one registration after the first message")
	reg := registrations(t, offset, res.GhostMXID)[0]
	devID, _ := botDevice(t, cli)
	assert.Equal(t, string(devID), reg.str("device_id"), "registration should log the bot's device ID")

	inject(t, user, ghost, "second message")
	assert.Len(t, registrations(t, offset, res.GhostMXID), 1, "a second message must not register again")
}

// T2: runbook step 2.
func TestT2_KeysQueryShape(t *testing.T) {
	requireStack(t)
	cli, user := newUser(t)
	res := inject(t, user, uniq("ghost"), "hello")
	requireImpersonatableDevice(t, cli, res.GhostMXID)
}

// T3: runbook step 3, the API-level part. The test user decrypts the ghost's message and
// the encrypted event points at the ghost's registered (impersonatable) device.
func TestT3_GhostMessageDecryptsWithGhostDevice(t *testing.T) {
	requireStack(t)
	cli, res, encrypted, decrypted := receiveGhostMessage(t, "decrypt me")

	assert.Equal(t, res.GhostMXID, encrypted.Sender)
	assert.Equal(t, res.GhostMXID, decrypted.Sender)
	assert.Equal(t, "decrypt me", decrypted.Content.AsMessage().Body)
	assert.True(t, decrypted.Mautrix.WasEncrypted)

	ghostDevID := requireImpersonatableDevice(t, cli, res.GhostMXID)
	assert.Equal(t, ghostDevID, encrypted.Content.AsEncrypted().DeviceID, "the encrypted event must reference the ghost's registered device")

	// The receiving client validates the impersonation (MSC4350): the bridge bot's device is cross-signed
	// (self_sign), so the message is trusted as coming from a device of the ghost.
	assert.GreaterOrEqual(t, int(decrypted.Mautrix.TrustState), int(id.TrustStateCrossSignedUntrusted),
		"the ghost's message should be trusted, got %v", decrypted.Mautrix.TrustState)
	if assert.NotNil(t, decrypted.Mautrix.TrustSource) {
		assert.Equal(t, res.GhostMXID, decrypted.Mautrix.TrustSource.UserID)
	}
}

// T14: negative control for the receiving side: without an impersonatable device the same message is not trusted
// as coming from a device of the ghost.
func TestT14_WithoutImpersonatableDeviceMessageIsNotTrusted(t *testing.T) {
	requireStack(t)
	restartBridge(t, "nomsc4350")
	_, _, _, decrypted := receiveGhostMessage(t, "no impersonation")
	assert.Equal(t, id.TrustStateUnknownDevice, decrypted.Mautrix.TrustState)
}

// receiveGhostMessage starts a client with end-to-end encryption, has a new ghost send it a message, and returns
// the encrypted and the decrypted form of that message.
func receiveGhostMessage(t *testing.T, text string) (*mautrix.Client, injectResult, *event.Event, *event.Event) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	t.Cleanup(cancel)

	name := uniq("alice")
	require.NoError(t, registerUser(ctx, name))
	cli, err := mautrix.NewClient(hsURL, "", "")
	require.NoError(t, err)

	type seen struct {
		encrypted *event.Event
		decrypted *event.Event
	}
	var mu sync.Mutex
	got := map[id.RoomID]*seen{}
	record := func(roomID id.RoomID, f func(s *seen)) {
		mu.Lock()
		defer mu.Unlock()
		if got[roomID] == nil {
			got[roomID] = &seen{}
		}
		f(got[roomID])
	}
	syncer := cli.Syncer.(*mautrix.DefaultSyncer)
	syncer.OnEventType(event.StateMember, func(ctx context.Context, evt *event.Event) {
		if evt.GetStateKey() == cli.UserID.String() && evt.Content.AsMember().Membership == event.MembershipInvite {
			_, _ = cli.JoinRoomByID(ctx, evt.RoomID)
		}
	})
	syncer.OnEventType(event.EventEncrypted, func(ctx context.Context, evt *event.Event) {
		record(evt.RoomID, func(s *seen) { s.encrypted = evt })
	})
	syncer.OnEventType(event.EventMessage, func(ctx context.Context, evt *event.Event) {
		record(evt.RoomID, func(s *seen) { s.decrypted = evt })
	})

	helper, err := cryptohelper.NewCryptoHelper(cli, []byte("e2e"), t.TempDir()+"/crypto.db")
	require.NoError(t, err)
	helper.LoginAs = passwordLogin(name)
	require.NoError(t, helper.Init(ctx))
	t.Cleanup(func() { _ = helper.Close() })
	cli.Crypto = helper
	syncCtx, stopSync := context.WithCancel(ctx)
	syncDone := make(chan struct{})
	go func() {
		defer close(syncDone)
		_ = cli.SyncWithContext(syncCtx)
	}()
	t.Cleanup(func() { stopSync(); <-syncDone })

	res := inject(t, cli.UserID, uniq("ghost"), text)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		s := got[res.RoomID]
		return s != nil && s.encrypted != nil && s.decrypted != nil
	}, 60*time.Second, 250*time.Millisecond, "timed out waiting for the encrypted and decrypted event")

	mu.Lock()
	defer mu.Unlock()
	s := got[res.RoomID]
	return cli, res, s.encrypted, s.decrypted
}

// T4: runbook step 4.
func TestT4_RestartDoesNotRegisterAgain(t *testing.T) {
	requireStack(t)
	cli, user := newUser(t)
	ghost := uniq("ghost")
	res := inject(t, user, ghost, "before restart")
	devID := requireImpersonatableDevice(t, cli, res.GhostMXID)

	offset := logOffset(t)
	restartBridge(t, "default")
	inject(t, user, ghost, "after restart")

	assert.Empty(t, registrations(t, offset, res.GhostMXID), "restart must not register the ghost again")
	assert.Equal(t, devID, requireImpersonatableDevice(t, cli, res.GhostMXID), "ghost device changed after restart")

	db, err := sql.Open("sqlite3", "file:"+runDir+"/bridge.db?mode=ro")
	require.NoError(t, err)
	defer db.Close()
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM crypto_impersonatable_device WHERE user_id=?`, string(res.GhostMXID)).Scan(&count))
	assert.Equal(t, 1, count, "expected one crypto_impersonatable_device row for the ghost")
}

// T5: runbook step 5.
func TestT5_Msc4350WithoutMsc4190IsDisabled(t *testing.T) {
	requireStack(t)
	// With msc4190 enabled on the homeserver, appservices can't use the legacy login a bridge without
	// msc4190 needs, so run this scenario against a homeserver in legacy mode.
	setSynapseMode(t, "legacy")
	cli, user := newUser(t)
	offset := logOffset(t)
	restartBridge(t, "nomsc4190")
	assert.True(t, logContains(t, offset, nomsc4190Msg), "expected the msc4190 warning at startup")

	ghost := uniq("ghost")
	res := inject(t, user, ghost, "message without impersonation")
	assert.Empty(t, registrations(t, offset, res.GhostMXID))
	requireNoDevices(t, cli, res.GhostMXID)
}

// T6: concurrent first messages from one ghost register it once.
func TestT6_ConcurrentMessagesRegisterOnce(t *testing.T) {
	requireStack(t)
	cli, user := newUser(t)
	ghost := uniq("ghost")
	offset := logOffset(t)

	const n = 8
	var wg sync.WaitGroup
	results := make([]injectResult, n)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = tryInject(t.Context(), user, ghost, "parallel message")
		}()
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	assert.Len(t, registrations(t, offset, results[0].GhostMXID), 1, "concurrent sends must register the ghost exactly once")
	requireImpersonatableDevice(t, cli, results[0].GhostMXID)
}

// T7: negative control, msc4350 disabled means no ghost devices.
func TestT7_DisabledMeansNoGhostDevices(t *testing.T) {
	requireStack(t)
	cli, user := newUser(t)
	restartBridge(t, "nomsc4350")
	offset := logOffset(t)
	res := inject(t, user, uniq("ghost"), "no impersonation please")
	assert.Empty(t, registrations(t, offset, res.GhostMXID))
	requireNoDevices(t, cli, res.GhostMXID)
}

// T8: the bot's own device is never replaced by an impersonatable one.
func TestT8_BotDeviceUntouched(t *testing.T) {
	requireStack(t)
	cli, user := newUser(t)
	inject(t, user, uniq("ghost"), "hello")
	devID, botKeys := botDevice(t, cli)
	assert.NotEmpty(t, botKeys["keys"], "the bot device must keep its keys")
	assert.NotEmpty(t, botKeys["algorithms"], "the bot device must keep its algorithms")
	assert.NotContains(t, botKeys, impersonatorKey, "the bot device must not be impersonatable")
	assert.NotEmpty(t, devID)
}

func registerUser(ctx context.Context, name string) error {
	return e2eRegister(ctx, name)
}

func passwordLogin(name string) *mautrix.ReqLogin {
	return &mautrix.ReqLogin{
		Type:       mautrix.AuthTypePassword,
		Identifier: mautrix.UserIdentifier{Type: mautrix.IdentifierTypeUser, User: name},
		Password:   password,
	}
}
