// Copyright (c) 2026 gchahcg
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package tests

// MSC4350 for double puppets (real users whose messages the bridge sends through its own device).
// Every scenario sends a message as the double-puppeted user into an encrypted room and reads the trust state a
// validating receiver (the mautrix crypto machine) assigns to it. Run with: ./run.sh test -run TestDP

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/cryptohelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type dpReceiver struct {
	cli *mautrix.Client
	mu  sync.Mutex
	got map[string]*event.Event
	enc map[string]bool
}

func newDPReceiver(t *testing.T, ctx context.Context) *dpReceiver {
	t.Helper()
	name := uniq("recv")
	require.NoError(t, registerUser(ctx, name))
	cli, err := mautrix.NewClient(hsURL, "", "")
	require.NoError(t, err)
	r := &dpReceiver{cli: cli, got: map[string]*event.Event{}}
	syncer := cli.Syncer.(*mautrix.DefaultSyncer)
	syncer.OnEventType(event.StateMember, func(ctx context.Context, evt *event.Event) {
		if evt.GetStateKey() == cli.UserID.String() && evt.Content.AsMember().Membership == event.MembershipInvite {
			_, _ = cli.JoinRoomByID(ctx, evt.RoomID)
		}
	})
	syncer.OnEventType(event.EventMessage, func(ctx context.Context, evt *event.Event) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.got[evt.Content.AsMessage().Body] = evt
	})
	helper, err := cryptohelper.NewCryptoHelper(cli, []byte("e2e"), t.TempDir()+"/crypto.db")
	require.NoError(t, err)
	helper.LoginAs = passwordLogin(name)
	require.NoError(t, helper.Init(ctx))
	t.Cleanup(func() { _ = helper.Close() })
	cli.Crypto = helper
	syncCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); _ = cli.SyncWithContext(syncCtx) }()
	t.Cleanup(func() { stop(); <-done })
	return r
}

func (r *dpReceiver) wait(t *testing.T, text string) *event.Event {
	t.Helper()
	var evt *event.Event
	require.Eventually(t, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		evt = r.got[text]
		return evt != nil
	}, 45*time.Second, 250*time.Millisecond, "receiver never saw %q", text)
	return evt
}

func injectFromMe(ctx context.Context, user id.UserID, ghost, text string) (injectResult, error) {
	var res injectResult
	body, _ := json.Marshal(map[string]any{"user_mxid": user, "ghost": ghost, "text": text, "from_me": true})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, injectURL+"/inject", bytes.NewReader(body))
	if err != nil {
		return res, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return res, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return res, fmt.Errorf("inject failed: status %d: %s", resp.StatusCode, data)
	}
	return res, json.Unmarshal(data, &res)
}

func setBridgeToken(t *testing.T, user id.UserID, token string) {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+runDir+"/bridge.db?_txlock=immediate&_busy_timeout=5000")
	require.NoError(t, err)
	defer db.Close()
	res, err := db.Exec(`UPDATE "user" SET access_token=$1 WHERE mxid=$2`, token, string(user))
	require.NoError(t, err)
	n, _ := res.RowsAffected()
	require.EqualValues(t, 1, n, "bridge user row for %s", user)
}

func asToken(t *testing.T) string {
	data, err := os.ReadFile(runDir + "/registration.yaml")
	require.NoError(t, err)
	return string(regexp.MustCompile(`(?m)^as_token:\s*(\S+)`).FindSubmatch(data)[1])
}

// useASTokenDoublePuppeting configures the bridge to double puppet users on test.local through the appservice.
func useASTokenDoublePuppeting(t *testing.T) {
	t.Helper()
	path := runDir + "/config.yaml"
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var cfg map[string]any
	require.NoError(t, yaml.Unmarshal(raw, &cfg))
	t.Cleanup(func() {
		require.NoError(t, os.WriteFile(path, raw, 0o600))
		runScript(t, "bridge", "default")
	})
	dpc := cfg["double_puppet"].(map[string]any)
	dpc["secrets"] = map[string]any{"test.local": "as_token:" + asToken(t)}
	out, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, out, 0o600))
	runScript(t, "bridge", "default")
}

type dpUser struct {
	mxid     id.UserID
	token    string
	deviceID id.DeviceID     // device of the token
	cli      *mautrix.Client // authenticated as the user
}

type dpOpts struct {
	// realDevice: the device behind the token has its own device keys, like a token copied from a normal client.
	realDevice bool
	// crossSigning: the user has cross-signing keys (set up on another device).
	crossSigning bool
	// signed: the user's client signs the impersonatable device with the self-signing key.
	signed bool
}

func newDPUser(t *testing.T, ctx context.Context, o dpOpts) *dpUser {
	t.Helper()
	name := uniq("dp")
	require.NoError(t, registerUser(ctx, name))
	if o.crossSigning {
		// A separate "main" device holds the cross-signing keys.
		cli, err := mautrix.NewClient(hsURL, "", "")
		require.NoError(t, err)
		helper, err := cryptohelper.NewCryptoHelper(cli, []byte("e2e"), t.TempDir()+"/main.db")
		require.NoError(t, err)
		helper.LoginAs = passwordLogin(name)
		require.NoError(t, helper.Init(ctx))
		t.Cleanup(func() { _ = helper.Close() })
		_, _, err = helper.Machine().GenerateAndUploadCrossSigningKeysWithPassword(ctx, password, "")
		require.NoError(t, err)
		mainDP = helper
	}
	cli, err := mautrix.NewClient(hsURL, "", "")
	require.NoError(t, err)
	if o.realDevice {
		helper, err := cryptohelper.NewCryptoHelper(cli, []byte("e2e"), t.TempDir()+"/real.db")
		require.NoError(t, err)
		helper.LoginAs = passwordLogin(name)
		require.NoError(t, helper.Init(ctx))
		t.Cleanup(func() { _ = helper.Close() })
	} else {
		_, err = cli.Login(ctx, &mautrix.ReqLogin{
			Type: mautrix.AuthTypePassword, Identifier: mautrix.UserIdentifier{Type: mautrix.IdentifierTypeUser, User: name},
			Password: password, StoreCredentials: true,
		})
		require.NoError(t, err)
	}
	return &dpUser{mxid: cli.UserID, token: cli.AccessToken, deviceID: cli.DeviceID, cli: cli}
}

var mainDP *cryptohelper.CryptoHelper

// queryDevice returns the raw keys of one device of the user.
func queryDevice(t *testing.T, cli *mautrix.Client, user id.UserID, dev id.DeviceID) map[string]any {
	t.Helper()
	raw, ok := queryKeys(t, cli, user)[user][dev]
	if !ok {
		return nil
	}
	return decodeObject(t, raw)
}

// runDoublePuppetScenario sends one message as the double puppet and reports what the receiver made of it.
func runDoublePuppetScenario(t *testing.T, ctx context.Context, name string, user *dpUser, o dpOpts, tokenMode bool) (*event.Event, map[id.DeviceID]map[string]any) {
	t.Helper()
	recv := newDPReceiver(t, ctx)
	ghost := uniq("ghost")
	res, err := tryInject(ctx, user.mxid, ghost, "setup")
	require.NoError(t, err)
	if tokenMode {
		setBridgeToken(t, user.mxid, user.token)
		runScript(t, "bridge", "default")
	}
	_, err = asClient(t).InviteUser(ctx, res.RoomID, &mautrix.ReqInviteUser{UserID: recv.cli.UserID})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		m, err := recv.cli.JoinedMembers(ctx, res.RoomID)
		if err != nil {
			return false
		}
		_, ok := m.Joined[recv.cli.UserID]
		return ok
	}, 30*time.Second, 250*time.Millisecond)
	time.Sleep(5 * time.Second) // the bridge learns about the join through its own sync of events

	text := "from the double puppet " + name
	_, err = injectFromMe(ctx, user.mxid, ghost, text)
	require.NoError(t, err)
	evt := recv.wait(t, text)
	devices := map[id.DeviceID]map[string]any{}
	for devID, raw := range queryKeys(t, recv.cli, user.mxid)[user.mxid] {
		devices[devID] = decodeObject(t, raw)
	}
	t.Logf("[%s] sender=%s trust=%v source=%v devices=%d", name, evt.Sender, evt.Mautrix.TrustState, evt.Mautrix.TrustSource != nil, len(devices))
	return evt, devices
}

func requireTrusted(t *testing.T, evt *event.Event, user id.UserID) {
	t.Helper()
	require.True(t, evt.Mautrix.WasEncrypted)
	assert.GreaterOrEqual(t, int(evt.Mautrix.TrustState), int(id.TrustStateCrossSignedUntrusted), "got %v", evt.Mautrix.TrustState)
	if assert.NotNil(t, evt.Mautrix.TrustSource) {
		assert.Equal(t, user, evt.Mautrix.TrustSource.UserID)
	}
}

func impersonatableDevices(devices map[id.DeviceID]map[string]any) (out []id.DeviceID) {
	for devID, d := range devices {
		if _, ok := d[impersonatorKey]; ok {
			out = append(out, devID)
		}
	}
	return
}

func TestDP1_TokenWithoutDeviceKeys(t *testing.T) {
	requireStack(t)
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	o := dpOpts{}
	u := newDPUser(t, ctx, o)
	evt, devices := runDoublePuppetScenario(t, ctx, "token without device keys", u, o, true)
	requireTrusted(t, evt, u.mxid)
	// The impersonatable device took the place of the (key-less) device of the token.
	assert.Equal(t, []id.DeviceID{u.deviceID}, impersonatableDevices(devices))
}

func TestDP2_TokenOfARealClientDevice(t *testing.T) {
	requireStack(t)
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	o := dpOpts{realDevice: true}
	u := newDPUser(t, ctx, o)
	evt, devices := runDoublePuppetScenario(t, ctx, "token of a real client device", u, o, true)
	// The device keeps its own keys, so the message isn't trusted as coming from the user.
	assert.Equal(t, id.TrustStateUnknownDevice, evt.Mautrix.TrustState)
	assert.Empty(t, impersonatableDevices(devices))
	require.Contains(t, devices, u.deviceID)
	assert.NotEmpty(t, devices[u.deviceID]["keys"], "the device's own keys must not be replaced")
}

func TestDP3_UserWithCrossSigningKeys(t *testing.T) {
	requireStack(t)
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	o := dpOpts{crossSigning: true}
	u := newDPUser(t, ctx, o)
	evt, devices := runDoublePuppetScenario(t, ctx, "bare token, user has cross-signing, device not signed", u, o, true)
	// Clients require the user's self-signing key to have signed the impersonatable device, see DP4.
	assert.Equal(t, id.TrustStateUnknownDevice, evt.Mautrix.TrustState)
	assert.Equal(t, []id.DeviceID{u.deviceID}, impersonatableDevices(devices))
}

func TestDP5_ASTokenMode(t *testing.T) {
	requireStack(t)
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	useASTokenDoublePuppeting(t)
	name := uniq("fake_dp")
	_, _, err := asClient(t).Register(ctx, &mautrix.ReqRegister[any]{Username: name, Type: mautrix.AuthTypeAppservice, InhibitLogin: true})
	require.NoError(t, err)
	u := &dpUser{mxid: id.NewUserID(name, "test.local")}
	evt, devices := runDoublePuppetScenario(t, ctx, "as_token mode", u, dpOpts{}, false)
	requireTrusted(t, evt, u.mxid)
	botDevID, _ := botDevice(t, asClient(t))
	assert.Equal(t, []id.DeviceID{botDevID}, impersonatableDevices(devices))
}

// DP4: like DP3, but the user's own client signs the impersonatable device with the self-signing key.
func TestDP4_UserWithCrossSigningKeysClientSignsTheDevice(t *testing.T) {
	requireStack(t)
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	o := dpOpts{crossSigning: true}
	u := newDPUser(t, ctx, o)
	main := mainDP.Machine()
	ghost := uniq("ghost")
	res, err := tryInject(ctx, u.mxid, ghost, "setup")
	require.NoError(t, err)
	setBridgeToken(t, u.mxid, u.token)
	runScript(t, "bridge", "default")
	// A first message makes the bridge register the impersonatable device.
	_, err = injectFromMe(ctx, u.mxid, ghost, "warmup")
	require.NoError(t, err)

	var impDev id.DeviceID
	var keys mautrix.DeviceKeys
	for devID, raw := range queryKeys(t, main.Client, u.mxid)[u.mxid] {
		if _, ok := decodeObject(t, raw)[impersonatorKey]; ok {
			impDev = devID
			require.NoError(t, json.Unmarshal(raw, &keys))
		}
	}
	require.NotEmpty(t, impDev, "the bridge should have registered an impersonatable device")
	keys.Unsigned = nil
	keys.Signatures = nil
	sig, err := main.CrossSigningKeys.SelfSigningKey.SignJSON(&keys)
	require.NoError(t, err)
	keys.Signatures = map[id.UserID]map[id.KeyID]string{
		u.mxid: {id.NewKeyID(id.KeyAlgorithmEd25519, main.CrossSigningKeys.SelfSigningKey.PublicKey().String()): sig},
	}
	var upload struct {
		Failures map[string]any `json:"failures"`
	}
	_, err = main.Client.MakeRequest(ctx, http.MethodPost, main.Client.BuildClientURL("v3", "keys", "signatures", "upload"),
		map[id.UserID]map[id.DeviceID]*mautrix.DeviceKeys{u.mxid: {impDev: &keys}}, &upload)
	require.NoError(t, err)
	require.Empty(t, upload.Failures, "uploading the self-signing signature failed")

	recv := newDPReceiver(t, ctx)
	_, err = asClient(t).InviteUser(ctx, res.RoomID, &mautrix.ReqInviteUser{UserID: recv.cli.UserID})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		m, err := recv.cli.JoinedMembers(ctx, res.RoomID)
		if err != nil {
			return false
		}
		_, ok := m.Joined[recv.cli.UserID]
		return ok
	}, 30*time.Second, 250*time.Millisecond)
	time.Sleep(5 * time.Second)
	_, err = injectFromMe(ctx, u.mxid, ghost, "signed by the user")
	require.NoError(t, err)
	evt := recv.wait(t, "signed by the user")
	requireTrusted(t, evt, u.mxid)
}
