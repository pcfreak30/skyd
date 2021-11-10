package client

import (
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/gorilla/websocket"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/node/api"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"
	"golang.org/x/net/context"
)

// BeginRegistrySubscription starts a new subscription.
func (c *Client) BeginRegistrySubscription(notifyFunc func(skymodules.RegistryEntry), closeHandler func(_ int, _ string) error) (*RegistrySubscription, error) {
	// Subscribe without limits.
	return c.BeginRegistrySubscriptionCustom(0, 0, notifyFunc, closeHandler)
}

// BeginRegistrySubscriptionCustom starts a new subscription with custom params.
func (c *Client) BeginRegistrySubscriptionCustom(bandwidthLimit uint64, notificationDelay time.Duration, notifyFunc func(skymodules.RegistryEntry), closeHandler func(_ int, _ string) error) (*RegistrySubscription, error) {
	// Build the URL.
	values := url.Values{}
	values.Set("bandwidthlimit", fmt.Sprint(bandwidthLimit))
	values.Set("notificationdelay", fmt.Sprint(notificationDelay.Milliseconds()))
	url := fmt.Sprintf("ws://%v/skynet/registry/subscription?%v", c.Address, values.Encode())

	// Set the useragent.
	agent := c.UserAgent
	if agent == "" {
		agent = "Sia-Agent"
	}
	h := http.Header{}
	h.Set("User-Agent", agent)

	// Init the connection.
	wsconn, resp, err := websocket.DefaultDialer.Dial(url, h)
	if err != nil {
		return nil, errors.AddContext(err, "failed to connect to subscription endpoint")
	}
	defer resp.Body.Close()

	wsconn.SetCloseHandler(closeHandler)

	ctx, cancel := context.WithCancel(context.Background())
	rs := &RegistrySubscription{
		staticCtx:        ctx,
		staticCancel:     cancel,
		staticNotifyFunc: notifyFunc,
		staticConn:       wsconn,
	}
	go rs.threadedListen()
	return rs, nil
}

// RegistrySubscription is the type for an ongoing subscription to the
// /skynet/registry/subscribe endpoint.
type RegistrySubscription struct {
	staticCtx        context.Context
	staticCancel     context.CancelFunc
	staticNotifyFunc func(skymodules.RegistryEntry)
	staticConn       *websocket.Conn
}

// Close closes the websocket connection gracefully.
func (rs *RegistrySubscription) Close() error {
	rs.staticCancel()
	err := rs.staticConn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	return errors.Compose(err, rs.staticConn.Close())
}

// threadedListen listens for notifications from the server.
func (rs *RegistrySubscription) threadedListen() {
	var resp api.RegistrySubscriptionResponse
	for {
		// Read the notification. This will block until we receive one.
		err := rs.staticConn.ReadJSON(&resp)
		if err != nil {
			_ = rs.staticConn.Close()
			return
		}
		if resp.Error != "" {
			_ = rs.staticConn.Close()
			return
		}
		var sig crypto.Signature
		signature, err := hex.DecodeString(resp.Signature)
		if err != nil {
			_ = rs.staticConn.Close()
			return
		}
		data, err := hex.DecodeString(resp.Data)
		if err != nil {
			_ = rs.staticConn.Close()
			return
		}
		copy(sig[:], signature)
		srv := modules.NewSignedRegistryValue(resp.DataKey, data, resp.Revision, sig, resp.Type)
		rs.staticNotifyFunc(skymodules.NewRegistryEntry(resp.PubKey, srv))
	}
}

// Subscribe subscribes the session to the given pubkey and datakey.
func (rs *RegistrySubscription) Subscribe(spk types.SiaPublicKey, datakey crypto.Hash) error {
	err := rs.staticConn.WriteJSON(api.RegistrySubscriptionRequest{
		Action:  api.RegistrySubscriptionActionSubscribe,
		PubKey:  spk,
		DataKey: datakey,
	})
	if err != nil {
		return err
	}
	return nil
}

// Unsubscribe unsubscribes the session from the given pubkey and datakey.
func (rs *RegistrySubscription) Unsubscribe(spk types.SiaPublicKey, datakey crypto.Hash) error {
	err := rs.staticConn.WriteJSON(api.RegistrySubscriptionRequest{
		Action:  api.RegistrySubscriptionActionUnsubscribe,
		PubKey:  spk,
		DataKey: datakey,
	})
	if err != nil {
		return err
	}
	return nil
}
