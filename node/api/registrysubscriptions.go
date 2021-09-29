package api

import (
	"fmt"
	"net/http"

	"github.com/gorilla/websocket"
	"github.com/julienschmidt/httprouter"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"
)

// upgrader is the upgrader used to upgrade the http connection to a websocket
// connection.
var upgrader = websocket.Upgrader{}

// The various request types a client can use.
const (
	// RegistrySubscriptionActionStop stops the subscription and causes the
	// endpoint to return and close the connection.
	RegistrySubscriptionActionStop = "stop"

	// RegistrySubscriptionActionSubscribe subscribes to a new entry.
	RegistrySubscriptionActionSubscribe = "subscribe"

	// RegistrySubscriptionActionUnsubscribe unsubscribes from an entry.
	RegistrySubscriptionActionUnsubscribe = "unsubscribe"
)

// RegistrySubscriptionResponse is the datatype defining the response the server
// sends to a client.
type RegistrySubscriptionResponse struct {
	Error string `json:"error"`

	DataKey   crypto.Hash        `json:"datakey"`
	PubKey    types.SiaPublicKey `json:"pubkey"`
	Signature crypto.Signature   `json:"signature"`

	Data     []byte                    `json:"data"`
	Revision uint64                    `json:"revision"`
	Type     modules.RegistryEntryType `json:"type"`
}

// RegistrySubscriptionRequest defines the request the client sends to the
// server to trigger actions such as subscribing and unsubscribing.
type RegistrySubscriptionRequest struct {
	Action  string             `json:"action"`
	PubKey  types.SiaPublicKey `json:"pubkey,omitempty"`
	DataKey crypto.Hash        `json:"datakey,omitempty"`
}

// skynetRegistrySubscriptionHandler handles websocket subscriptions to the registry.
func (api *API) skynetRegistrySubscriptionHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	// Upgrade connection to use websocket.
	c, err := upgrader.Upgrade(w, req, nil)
	if err != nil {
		if err != nil {
			handleSkynetError(w, "failed to upgrade connection to websocket connection", err)
			return
		}
	}
	defer c.Close()

	// Declare a handler for notifications.
	notifier := func(srv skymodules.RegistryEntry) error {
		return c.WriteJSON(RegistrySubscriptionResponse{
			Error:     "",
			DataKey:   srv.Tweak,
			PubKey:    srv.PubKey,
			Signature: srv.Signature,
			Data:      srv.Data,
			Revision:  srv.Revision,
			Type:      srv.Type,
		})
	}

	// Start subscription.
	subscriber, err := api.renter.NewRegistrySubscriber(notifier)
	if err != nil {
		c.WriteJSON(RegistrySubscriptionResponse{Error: fmt.Sprintf("failed to create subscriber: %v", err)})
	}
	defer subscriber.Close()

	// Forward incoming requests to the subscription manager.
	var r RegistrySubscriptionRequest
	for {
		err = c.ReadJSON(&r)
		if err != nil {
			return // connection is broken, nothing we can do
		}
		switch r.Action {
		case RegistrySubscriptionActionStop:
			return // done
		case RegistrySubscriptionActionSubscribe:
			srv := subscriber.Subscribe(r.PubKey, r.DataKey)
			if srv != nil {
				if err := notifier(*srv); err != nil {
					return // connection is broken, nothing we can do
				}
			}
		case RegistrySubscriptionActionUnsubscribe:
			subscriber.Unsubscribe(modules.DeriveRegistryEntryID(r.PubKey, r.DataKey))
		default:
			c.WriteJSON(RegistrySubscriptionResponse{Error: "unknown action"})
		}
	}
}
