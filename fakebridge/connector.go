package main

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"go.mau.fi/util/configupgrade"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/status"
	"maunium.net/go/mautrix/event"
)

type Connector struct {
	br *bridgev2.Bridge
}

var (
	_ bridgev2.NetworkConnector = (*Connector)(nil)
	_ bridgev2.StoppableNetwork = (*Connector)(nil)
)

func (c *Connector) Init(br *bridgev2.Bridge) { c.br = br }

func (c *Connector) Start(ctx context.Context) error {
	return startInjectServer(c)
}

func (c *Connector) Stop() { stopInjectServer() }

func (c *Connector) GetName() bridgev2.BridgeName {
	return bridgev2.BridgeName{
		DisplayName:          "Fake",
		NetworkURL:           "https://example.invalid",
		NetworkID:            "fake",
		BeeperBridgeType:     "msc4350-fakebridge",
		DefaultPort:          29399,
		DefaultCommandPrefix: "!fake",
	}
}

func (c *Connector) GetDBMetaTypes() database.MetaTypes { return database.MetaTypes{} }

func (c *Connector) GetCapabilities() *bridgev2.NetworkGeneralCapabilities {
	return &bridgev2.NetworkGeneralCapabilities{}
}

func (c *Connector) GetConfig() (string, any, configupgrade.Upgrader) {
	return "", nil, configupgrade.NoopUpgrader
}

func (c *Connector) LoadUserLogin(ctx context.Context, login *bridgev2.UserLogin) error {
	login.Client = &Client{login: login}
	return nil
}

func (c *Connector) GetLoginFlows() []bridgev2.LoginFlow { return nil }

func (c *Connector) CreateLogin(ctx context.Context, user *bridgev2.User, flowID string) (bridgev2.LoginProcess, error) {
	return nil, fmt.Errorf("interactive login not supported, use the inject endpoint")
}

func (c *Connector) GetBridgeInfoVersion() (info, capabilities int) { return 1, 1 }

type Client struct {
	login *bridgev2.UserLogin
}

var _ bridgev2.NetworkAPI = (*Client)(nil)

func (cl *Client) Connect(ctx context.Context) {
	cl.login.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnected})
}

func (cl *Client) Disconnect() {}

func (cl *Client) IsLoggedIn() bool { return true }

func (cl *Client) LogoutRemote(ctx context.Context) {}

func (cl *Client) IsThisUser(ctx context.Context, userID networkid.UserID) bool {
	return userID == networkid.UserID(cl.login.ID)
}

func ptr[T any](v T) *T { return &v }

func (cl *Client) GetChatInfo(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	// Portal IDs are the ghost's remote user ID, see inject.go.
	ghost := networkid.UserID(portal.ID)
	members := bridgev2.ChatMemberMap{}
	members.Set(bridgev2.ChatMember{
		EventSender: bridgev2.EventSender{IsFromMe: true, SenderLogin: cl.login.ID, Sender: networkid.UserID(cl.login.ID)},
		Membership:  event.MembershipJoin,
	})
	members.Set(bridgev2.ChatMember{
		EventSender: bridgev2.EventSender{Sender: ghost},
		Membership:  event.MembershipJoin,
	})
	return &bridgev2.ChatInfo{
		Name: ptr("fake chat with " + string(ghost)),
		Type: ptr(database.RoomTypeDM),
		Members: &bridgev2.ChatMemberList{
			IsFull:      true,
			OtherUserID: ghost,
			MemberMap:   members,
		},
	}, nil
}

func (cl *Client) GetUserInfo(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	return &bridgev2.UserInfo{Name: ptr(string(ghost.ID))}, nil
}

func (cl *Client) GetCapabilities(ctx context.Context, portal *bridgev2.Portal) *event.RoomFeatures {
	return &event.RoomFeatures{ID: "fake"}
}

var msgCounter atomic.Int64

func (cl *Client) HandleMatrixMessage(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error) {
	return &bridgev2.MatrixMessageResponse{
		DB: &database.Message{
			ID:        networkid.MessageID(fmt.Sprintf("matrix-%d", msgCounter.Add(1))),
			SenderID:  networkid.UserID(cl.login.ID),
			Timestamp: time.UnixMilli(msg.Event.Timestamp),
		},
	}, nil
}
