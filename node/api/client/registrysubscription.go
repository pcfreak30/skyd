package client

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gorilla/websocket"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/node/api"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"
	"golang.org/x/net/context"
)

// BeginRegistrySubscription starts a new subscription.
func (c *Client) BeginRegistrySubscription(notifyFunc func(srv *modules.SignedRegistryValue)) (*RegistrySubscription, error) {
	// Build the URL.
	url := "ws://" + c.Address + "/skynet/registry/subscribe"

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

	ctx, cancel := context.WithCancel(context.Background())
	rs := &RegistrySubscription{
		ctx:        ctx,
		cancel:     cancel,
		notifyFunc: notifyFunc,
		staticConn: wsconn,
	}
	go rs.threadedListen()
	return rs, nil
}

// RegistrySubscription is the type for an ongoing subscription to the
// /skynet/registry/subscribe endpoint.
type RegistrySubscription struct {
	ctx        context.Context
	cancel     context.CancelFunc
	notifyFunc func(srv *modules.SignedRegistryValue)
	staticConn *websocket.Conn
}

// Close closes the websocket connection gracefully.
func (rs *RegistrySubscription) Close() error {
	rs.cancel()
	err := rs.staticConn.WriteJSON(api.RegistrySubscriptionRequest{
		Action: api.RegistrySubscriptionActionStop,
	})
	return errors.Compose(err, rs.staticConn.Close())
}

func (rs *RegistrySubscription) threadedListen() {
	var resp api.RegistrySubscriptionResponse
	for {
		err := rs.staticConn.ReadJSON(&resp)
		if err != nil && strings.Contains(err.Error(), "use of closed network connection") {
			return // done
		}
		if err != nil {
			fmt.Println("err", err)
			_ = rs.staticConn.Close()
			return
		}
		if resp.Error != "" {
			fmt.Println("err2", resp.Error)
			_ = rs.staticConn.Close()
			return
		}
		srv := modules.NewSignedRegistryValue(resp.DataKey, resp.Data, resp.Revision, resp.Signature, resp.Type)

		// TODO: verify entry

		rs.notifyFunc(&srv)
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
