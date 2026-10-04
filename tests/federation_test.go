// Copyright (c) 2026 gchahcg
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package tests

// Federation: MSC4350 applies to both the client-server and the server-server APIs. These tests put a user of a
// second homeserver (b.test.local) in a room with a ghost of the first one (test.local) and check what that user's
// own server returns for the ghost's keys. Run with E2E_FEDERATION=1 ./run.sh all.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/cryptohelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/gchahcg/msc4350-e2e/internal/e2e"
)

var hsBURL = e2e.Env("E2E_HS_B", "")

const remoteServerName = "b.test.local"

func requireFederation(t *testing.T) {
	t.Helper()
	requireStack(t)
	if hsBURL == "" {
		t.Skip("the second homeserver isn't running, use E2E_FEDERATION=1 ./run.sh all")
	}
}

// newRemoteUser registers and logs in a user on the second homeserver.
func newRemoteUser(t *testing.T) (*mautrix.Client, id.UserID) {
	t.Helper()
	name := uniq("carol")
	require.NoError(t, e2e.RegisterUser(t.Context(), hsBURL, name, password))
	cli, err := e2e.Login(t.Context(), hsBURL, name, password)
	require.NoError(t, err)
	return cli, e2e.UserIDOn(name, remoteServerName)
}

// joinRemote has the bot invite the remote user to the room and makes the remote user join it over federation.
func joinRemote(t *testing.T, remote *mautrix.Client, room id.RoomID) {
	t.Helper()
	ctx := t.Context()
	_, err := asClient(t).InviteUser(ctx, room, &mautrix.ReqInviteUser{UserID: remote.UserID})
	require.NoError(t, err)
	_, err = remote.JoinRoom(ctx, room.String(), &mautrix.ReqJoinRoom{Via: []string{"test.local"}})
	require.NoError(t, err)
}

// waitForGhostDevice waits until the remote user's homeserver knows a device for the ghost.
func waitForGhostDevice(t *testing.T, cli *mautrix.Client, ghost id.UserID) {
	t.Helper()
	require.Eventually(t, func() bool {
		var resp struct {
			DeviceKeys map[id.UserID]rawDevices `json:"device_keys"`
		}
		_, err := cli.MakeRequest(t.Context(), "POST", cli.BuildClientURL("v3", "keys", "query"),
			map[string]any{"device_keys": map[id.UserID][]id.DeviceID{ghost: {}, botMXID: {}}}, &resp)
		return err == nil && len(resp.DeviceKeys[ghost]) > 0 && len(resp.DeviceKeys[botMXID]) > 0
	}, 30*time.Second, 500*time.Millisecond, "the remote homeserver never returned the ghost's device")
}

// F1: the remote user joins after the ghost registered, so the second homeserver fetches the device over
// federation (POST /_matrix/federation/v1/user/keys/query) when the remote user asks for it.
func TestF1_RemoteUserJoinsAfterRegistration(t *testing.T) {
	requireFederation(t)
	_, owner := newUser(t)
	res := inject(t, owner, uniq("ghost"), "hello")

	remote, _ := newRemoteUser(t)
	joinRemote(t, remote, res.RoomID)
	waitForGhostDevice(t, remote, res.GhostMXID)
	requireImpersonatableDevice(t, remote, res.GhostMXID)
}

// F2: a new ghost starts talking in a room that the remote user is already in, so the second homeserver learns
// about the ghost's device after the fact (device list update, or a later federation query).
func TestF2_GhostRegistersAfterRemoteUserIsInTheRoom(t *testing.T) {
	requireFederation(t)
	_, owner := newUser(t)
	first := inject(t, owner, uniq("ghost"), "creates the room")

	remote, _ := newRemoteUser(t)
	joinRemote(t, remote, first.RoomID)

	// A second ghost speaks in the same room, and only now gets its impersonatable device.
	second := injectInto(t, owner, uniq("ghost"), first.PortalID, "from a new ghost")
	require.Equal(t, first.RoomID, second.RoomID)
	waitForGhostDevice(t, remote, second.GhostMXID)
	requireImpersonatableDevice(t, remote, second.GhostMXID)
}

// F3: a client on the second homeserver decrypts the ghost's message, and the message points at the ghost's
// device as the remote homeserver reports it.
func TestF3_RemoteClientDecryptsGhostMessage(t *testing.T) {
	requireFederation(t)
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()

	_, owner := newUser(t)
	first := inject(t, owner, uniq("ghost"), "creates the room")

	name := uniq("carol")
	require.NoError(t, e2e.RegisterUser(ctx, hsBURL, name, password))
	cli, err := mautrix.NewClient(hsBURL, "", "")
	require.NoError(t, err)

	var mu sync.Mutex
	var encrypted, decrypted *event.Event
	syncer := cli.Syncer.(*mautrix.DefaultSyncer)
	syncer.OnEventType(event.EventEncrypted, func(_ context.Context, evt *event.Event) {
		mu.Lock()
		defer mu.Unlock()
		if evt.RoomID == first.RoomID && evt.Sender != botMXID {
			encrypted = evt
		}
	})
	syncer.OnEventType(event.EventMessage, func(_ context.Context, evt *event.Event) {
		mu.Lock()
		defer mu.Unlock()
		if evt.RoomID == first.RoomID && evt.Content.AsMessage().Body == "from a new ghost" {
			decrypted = evt
		}
	})
	helper, err := cryptohelper.NewCryptoHelper(cli, []byte("e2e"), t.TempDir()+"/crypto.db")
	require.NoError(t, err)
	helper.LoginAs = passwordLogin(name)
	require.NoError(t, helper.Init(ctx))
	defer helper.Close()
	cli.Crypto = helper
	syncCtx, stopSync := context.WithCancel(ctx)
	syncDone := make(chan struct{})
	go func() {
		defer close(syncDone)
		_ = cli.SyncWithContext(syncCtx)
	}()
	defer func() { stopSync(); <-syncDone }()

	// The remote user is in the room (with its device known) before the next ghost speaks.
	joinRemote(t, cli, first.RoomID)
	second := injectInto(t, owner, uniq("ghost"), first.PortalID, "from a new ghost")

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return encrypted != nil && decrypted != nil
	}, 60*time.Second, 250*time.Millisecond, "the remote client never received and decrypted the message")

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, second.GhostMXID, decrypted.Sender)
	assert.True(t, decrypted.Mautrix.WasEncrypted)
	// The remote client validates the impersonation with keys it got over federation.
	assert.GreaterOrEqual(t, int(decrypted.Mautrix.TrustState), int(id.TrustStateCrossSignedUntrusted),
		"the ghost's message should be trusted by the remote client, got %v", decrypted.Mautrix.TrustState)
	waitForGhostDevice(t, cli, second.GhostMXID)
	ghostDevice := requireImpersonatableDevice(t, cli, second.GhostMXID)
	assert.Equal(t, ghostDevice, encrypted.Content.AsEncrypted().DeviceID,
		"the message must reference the ghost's device as the remote homeserver reports it")
}
