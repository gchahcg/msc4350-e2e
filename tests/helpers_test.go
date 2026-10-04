// Copyright (c) 2026 gchahcg
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package tests

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"

	"github.com/gchahcg/msc4350-e2e/internal/e2e"
)

var (
	runDir    = e2e.Env("E2E_RUN_DIR", "")
	hsURL     = e2e.Env("E2E_HS", "http://127.0.0.1:18008")
	injectURL = e2e.Env("E2E_INJECT", "http://127.0.0.1:29400")
	script    = e2e.Env("E2E_SCRIPT", "")

	botMXID = id.UserID("@fakebot:test.local")

	registrationMsg = "Registered MSC4350 impersonatable device for ghost"
	nomsc4190Msg    = "encryption.msc4350 requires encryption.msc4190, disabling ghost impersonation"
)

const password = "e2e-test-password"

var suffixCounter struct {
	sync.Mutex
	n int
}

// uniq returns a name that's unique within this test run (and across runs on a reused stack).
func uniq(prefix string) string {
	suffixCounter.Lock()
	defer suffixCounter.Unlock()
	suffixCounter.n++
	return fmt.Sprintf("%s%d%d", prefix, time.Now().UnixNano()%1_000_000_000, suffixCounter.n)
}

func requireStack(t *testing.T) {
	t.Helper()
	if runDir == "" || script == "" {
		t.Skip("E2E_RUN_DIR/E2E_SCRIPT not set, run the tests through ./run.sh test")
	}
}

// newUser registers and logs in a fresh Matrix user.
func newUser(t *testing.T) (*mautrix.Client, id.UserID) {
	t.Helper()
	ctx := t.Context()
	name := uniq("alice")
	require.NoError(t, e2e.RegisterUser(ctx, hsURL, name, password))
	cli, err := e2e.Login(ctx, hsURL, name, password)
	require.NoError(t, err)
	return cli, e2e.UserID(name)
}

type injectResult struct {
	RoomID    id.RoomID `json:"room_id"`
	GhostMXID id.UserID `json:"ghost_mxid"`
	MessageID string    `json:"message_id"`
}

// inject makes the fake bridge deliver a message from the given remote ghost to the Matrix user.
func inject(t *testing.T, user id.UserID, ghost, text string) injectResult {
	t.Helper()
	res, err := tryInject(t.Context(), user, ghost, text)
	require.NoError(t, err)
	return res
}

func tryInject(ctx context.Context, user id.UserID, ghost, text string) (injectResult, error) {
	var res injectResult
	body, _ := json.Marshal(map[string]string{"user_mxid": string(user), "ghost": ghost, "text": text})
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

// ---- bridge log access ----

type logLine map[string]any

func (l logLine) str(key string) string {
	v, _ := l[key].(string)
	return v
}

func logPath() string { return runDir + "/bridge.stdout" }

// logOffset returns the current size of the bridge log, to read only newer lines later.
func logOffset(t *testing.T) int64 {
	t.Helper()
	st, err := os.Stat(logPath())
	if os.IsNotExist(err) {
		return 0
	}
	require.NoError(t, err)
	return st.Size()
}

func logSince(t *testing.T, offset int64) []logLine {
	t.Helper()
	f, err := os.Open(logPath())
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	defer f.Close()
	_, err = f.Seek(offset, io.SeekStart)
	require.NoError(t, err)
	var lines []logLine
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for sc.Scan() {
		var l logLine
		if json.Unmarshal(sc.Bytes(), &l) == nil {
			lines = append(lines, l)
		}
	}
	return lines
}

// registrations returns the "Registered MSC4350 impersonatable device" log lines for the given ghost.
func registrations(t *testing.T, offset int64, ghost id.UserID) []logLine {
	t.Helper()
	var out []logLine
	for _, l := range logSince(t, offset) {
		if l.str("message") == registrationMsg && l.str("ghost_user_id") == string(ghost) {
			out = append(out, l)
		}
	}
	return out
}

func logContains(t *testing.T, offset int64, msg string) bool {
	t.Helper()
	for _, l := range logSince(t, offset) {
		if l.str("message") == msg {
			return true
		}
	}
	return false
}

// ---- bridge control ----

// restartBridge restarts the fake bridge with the given config variant (default, nomsc4350, nomsc4190)
// and restores the default variant when the test ends.
func restartBridge(t *testing.T, variant string) {
	t.Helper()
	if variant != "default" {
		// Register the cleanup first so a failed start doesn't leave the bridge down for later tests.
		t.Cleanup(func() { runScript(t, "bridge", "default") })
	}
	runScript(t, "bridge", variant)
}

// setSynapseMode switches the homeserver between "default" (msc4190 enabled) and "legacy" (msc4190 disabled,
// needed for a bridge that logs in with the legacy appservice login). The default mode is restored afterwards.
func setSynapseMode(t *testing.T, mode string) {
	t.Helper()
	if mode != "default" {
		t.Cleanup(func() {
			runScript(t, "bridge-stop")
			runScript(t, "synapse", "default")
			runScript(t, "bridge", "default")
		})
	}
	runScript(t, "synapse", mode)
}

func runScript(t *testing.T, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, script, args...).CombinedOutput()
	require.NoError(t, err, "run.sh %s failed: %s", strings.Join(args, " "), out)
}

// ---- key queries ----

// rawDevices maps device ID to the raw JSON of its device keys, as returned by /keys/query.
type rawDevices map[id.DeviceID]json.RawMessage

func queryKeys(t *testing.T, cli *mautrix.Client, users ...id.UserID) map[id.UserID]rawDevices {
	t.Helper()
	req := map[id.UserID][]id.DeviceID{}
	for _, u := range users {
		req[u] = []id.DeviceID{}
	}
	var resp struct {
		DeviceKeys map[id.UserID]rawDevices `json:"device_keys"`
	}
	_, err := cli.MakeRequest(t.Context(), http.MethodPost, cli.BuildClientURL("v3", "keys", "query"),
		map[string]any{"device_keys": req}, &resp)
	require.NoError(t, err)
	return resp.DeviceKeys
}

func decodeObject(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

func e2eRegister(ctx context.Context, name string) error {
	return e2e.RegisterUser(ctx, hsURL, name, password)
}
