// Package e2e holds helpers shared by the end-to-end tests and the register command.
package e2e

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"
)

const SharedSecret = "msc4350-e2e-shared-secret"

// Env returns an environment variable or the fallback.
func Env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// RegisterUser creates a user through Synapse's shared-secret registration endpoint.
// It is not an error if the user already exists.
func RegisterUser(ctx context.Context, hs, username, password string) error {
	var nonceResp struct {
		Nonce string `json:"nonce"`
	}
	if err := doJSON(ctx, http.MethodGet, hs+"/_synapse/admin/v1/register", nil, &nonceResp); err != nil {
		return fmt.Errorf("failed to get registration nonce: %w", err)
	}
	mac := hmac.New(sha1.New, []byte(SharedSecret))
	mac.Write([]byte(nonceResp.Nonce))
	mac.Write([]byte{0})
	mac.Write([]byte(username))
	mac.Write([]byte{0})
	mac.Write([]byte(password))
	mac.Write([]byte{0})
	mac.Write([]byte("notadmin"))
	err := doJSON(ctx, http.MethodPost, hs+"/_synapse/admin/v1/register", map[string]any{
		"nonce":    nonceResp.Nonce,
		"username": username,
		"password": password,
		"admin":    false,
		"mac":      hex.EncodeToString(mac.Sum(nil)),
	}, nil)
	if err != nil && !isUserInUse(err) {
		return err
	}
	return nil
}

func isUserInUse(err error) bool {
	return err != nil && bytes.Contains([]byte(err.Error()), []byte("M_USER_IN_USE"))
}

func doJSON(ctx context.Context, method, url string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: status %d: %s", method, url, resp.StatusCode, data)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// Login logs in with a password and returns a ready client.
func Login(ctx context.Context, hs, username, password string) (*mautrix.Client, error) {
	cli, err := mautrix.NewClient(hs, "", "")
	if err != nil {
		return nil, err
	}
	resp, err := cli.Login(ctx, &mautrix.ReqLogin{
		Type:                     mautrix.AuthTypePassword,
		Identifier:               mautrix.UserIdentifier{Type: mautrix.IdentifierTypeUser, User: username},
		Password:                 password,
		InitialDeviceDisplayName: "msc4350 e2e test client",
		StoreCredentials:         true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to log in as %s: %w", username, err)
	}
	_ = resp
	return cli, nil
}

// UserID builds a full Matrix user ID on the test homeserver.
func UserID(localpart string) id.UserID {
	return id.NewUserID(localpart, "test.local")
}
