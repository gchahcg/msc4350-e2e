// Copyright (c) 2026 gchahcg
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

var injectServer *http.Server

type injectRequest struct {
	// UserMXID is the Matrix user who owns the login and receives the message.
	UserMXID id.UserID `json:"user_mxid"`
	// Ghost is the remote user ID of the sender, which becomes a bridge ghost.
	Ghost string `json:"ghost"`
	Text  string `json:"text"`
}

type injectResponse struct {
	RoomID    id.RoomID `json:"room_id"`
	GhostMXID id.UserID `json:"ghost_mxid"`
	MessageID string    `json:"message_id"`
}

var injectCounter atomic.Int64

func startInjectServer(c *Connector) error {
	addr := os.Getenv("FAKEBRIDGE_INJECT_ADDR")
	if addr == "" {
		addr = "127.0.0.1:29400"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /inject", func(w http.ResponseWriter, r *http.Request) {
		var req injectRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.UserMXID == "" || req.Ghost == "" {
			http.Error(w, "bad request: need user_mxid and ghost", http.StatusBadRequest)
			return
		}
		resp, err := c.inject(r.Context(), &req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen for inject server: %w", err)
	}
	injectServer = &http.Server{Handler: mux}
	go func() {
		if err := injectServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			c.br.Log.Err(err).Msg("Inject server failed")
		}
	}()
	c.br.Log.Info().Str("addr", addr).Msg("Inject server listening")
	return nil
}

func stopInjectServer() {
	if injectServer != nil {
		_ = injectServer.Close()
	}
}

func (c *Connector) inject(ctx context.Context, req *injectRequest) (*injectResponse, error) {
	user, err := c.br.GetUserByMXID(ctx, req.UserMXID)
	if err != nil {
		return nil, fmt.Errorf("failed to get user: %w", err)
	}
	loginID := networkid.UserLoginID("fake-" + strings.TrimPrefix(strings.SplitN(string(req.UserMXID), ":", 2)[0], "@"))
	login := c.br.GetCachedUserLoginByID(loginID)
	if login == nil {
		login, err = user.NewLogin(ctx, &database.UserLogin{ID: loginID, RemoteName: string(req.UserMXID)}, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create login: %w", err)
		}
	}
	key := networkid.PortalKey{ID: networkid.PortalID(req.Ghost), Receiver: loginID}
	msgID := networkid.MessageID(fmt.Sprintf("inject-%d-%d", time.Now().UnixNano(), injectCounter.Add(1)))
	text := req.Text
	res := login.QueueRemoteEvent(&simplevent.Message[string]{
		EventMeta: simplevent.EventMeta{
			Type:         bridgev2.RemoteEventMessage,
			PortalKey:    key,
			Sender:       bridgev2.EventSender{Sender: networkid.UserID(req.Ghost)},
			CreatePortal: true,
			Timestamp:    time.Now(),
		},
		Data: text,
		ID:   msgID,
		ConvertMessageFunc: func(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, data string) (*bridgev2.ConvertedMessage, error) {
			return &bridgev2.ConvertedMessage{Parts: []*bridgev2.ConvertedMessagePart{{
				Type:    event.EventMessage,
				Content: &event.MessageEventContent{MsgType: event.MsgText, Body: data},
			}}}, nil
		},
	})
	if res.Error != nil {
		return nil, fmt.Errorf("failed to queue event: %w", res.Error)
	}

	// Wait until the portal room exists and the message has been stored, i.e. sent to Matrix.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		portal, err := c.br.GetExistingPortalByKey(ctx, key)
		if err == nil && portal != nil && portal.MXID != "" {
			msg, err := c.br.DB.Message.GetFirstPartByID(ctx, loginID, msgID)
			if err == nil && msg != nil {
				ghost, err := c.br.GetGhostByID(ctx, networkid.UserID(req.Ghost))
				if err != nil {
					return nil, fmt.Errorf("failed to get ghost: %w", err)
				}
				return &injectResponse{RoomID: portal.MXID, GhostMXID: ghost.Intent.GetMXID(), MessageID: string(msgID)}, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return nil, fmt.Errorf("timed out waiting for message to be bridged")
}
