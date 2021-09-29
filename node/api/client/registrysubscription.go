package client

import (
	"net/http"

	"github.com/gorilla/websocket"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/node/api"
)

// BeginRegistrySubscription starts a new subscription.
func (c *Client) BeginRegistrySubscription() (*RegistrySubscription, error) {
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

	return &RegistrySubscription{
		staticConn: wsconn,
	}, nil
}

// RegistrySubscription is the type for an ongoing subscription to the
// /skynet/registry/subscribe endpoint.
type RegistrySubscription struct {
	staticConn *websocket.Conn
}

// Close closes the websocket connection gracefully.
func (rs *RegistrySubscription) Close() error {
	err := rs.staticConn.WriteJSON(api.RegistrySubscriptionRequest{
		Action: api.RegistrySubscriptionActionStop,
	})
	return errors.Compose(err, rs.staticConn.Close())
}
