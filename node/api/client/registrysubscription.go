package client

import (
	"encoding/hex"
	"fmt"
	"math"
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

// RegistrySubscriptionResponse contains all the fields of a potential
// notification from the server. That way we can parse the response in one try
// and later look at the type to figure out what type of response we actually
// received.
type RegistrySubscriptionResponse struct {
	ResponseType  string                    `json:"responsetype"`
	Error         string                    `json:"error"`
	DataKey       string                    `json:"datakey"`
	PubKey        string                    `json:"pubkey"`
	Signature     string                    `json:"signature"`
	Data          string                    `json:"data"`
	Revision      uint64                    `json:"revision"`
	Type          modules.RegistryEntryType `json:"type"`
	Subscriptions []string                  `json:"subscriptions"`
}

// ParseRegistryEntry tries to parse a RegistryEntry from the response. It will
// fail if the response doesn't have the 'notification' type.
func (rsr *RegistrySubscriptionResponse) ParseRegistryEntry() (skymodules.RegistryEntry, error) {
	// Check error.
	if rsr.Error != "" {
		return skymodules.RegistryEntry{}, fmt.Errorf("can't parse failed response: %v", rsr.Error)
	}
	// Check type first.
	if rsr.ResponseType != api.RegistrySubscriptionResponseTypeNotification {
		return skymodules.RegistryEntry{}, errors.New("can't parse registry entry - wrong type")
	}
	// Parse entry.
	var sig crypto.Signature
	if len(rsr.Signature) > 0 {
		signature, err := hex.DecodeString(rsr.Signature)
		if err != nil {
			return skymodules.RegistryEntry{}, err
		}
		copy(sig[:], signature)
	}
	data, err := hex.DecodeString(rsr.Data)
	if err != nil {
		return skymodules.RegistryEntry{}, err
	}
	var dataKey crypto.Hash
	err = dataKey.LoadString(rsr.DataKey)
	if err != nil {
		return skymodules.RegistryEntry{}, err
	}
	var pubKey types.SiaPublicKey
	err = pubKey.LoadString(rsr.PubKey)
	if err != nil {
		return skymodules.RegistryEntry{}, err
	}
	srv := modules.NewSignedRegistryValue(dataKey, data, rsr.Revision, sig, rsr.Type)
	return skymodules.NewRegistryEntry(pubKey, srv), nil
}

// ParseSubscriptions tries to parse the subscriptions from the response. It
// will fail if the response doesn't have the 'subscriptions' type.
func (rsr *RegistrySubscriptionResponse) ParseSubscriptions() ([]modules.RegistryEntryID, error) {
	// Check error.
	if rsr.Error != "" {
		return nil, fmt.Errorf("can't parse failed response: %v", rsr.Error)
	}
	// Check type first.
	if rsr.ResponseType != api.RegistrySubscriptionResponseTypeSubscriptions {
		return nil, errors.New("can't parse subscriptions response - wrong type")
	}
	subs := make([]modules.RegistryEntryID, 0, len(rsr.Subscriptions))
	for _, sub := range rsr.Subscriptions {
		var h crypto.Hash
		if err := h.LoadString(sub); err != nil {
			return nil, err
		}
		subs = append(subs, modules.RegistryEntryID(h))
	}
	return subs, nil
}

// BeginRegistrySubscription starts a new subscription.
func (c *Client) BeginRegistrySubscription(notifyFunc func(RegistrySubscriptionResponse), closeHandler func(_ int, _ string) error) (*RegistrySubscription, error) {
	// Subscribe without limits.
	return c.BeginRegistrySubscriptionCustom(math.MaxUint64, 0, notifyFunc, closeHandler)
}

// BeginRegistrySubscriptionCustom starts a new subscription with custom params.
func (c *Client) BeginRegistrySubscriptionCustom(bandwidthLimit uint64, notificationDelay time.Duration, notifyFunc func(RegistrySubscriptionResponse), closeHandler func(_ int, _ string) error) (*RegistrySubscription, error) {
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
	staticNotifyFunc func(RegistrySubscriptionResponse)
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
	for {
		// Read the notification. This will block until we receive one.
		var resp RegistrySubscriptionResponse
		err := rs.staticConn.ReadJSON(&resp)
		if err != nil {
			_ = rs.staticConn.Close()
			return
		}
		if resp.Error != "" {
			_ = rs.staticConn.Close()
			return
		}
		rs.staticNotifyFunc(resp)
	}
}

// Subscribe subscribes the session to the given pubkey and datakey.
func (rs *RegistrySubscription) Subscribe(spk types.SiaPublicKey, datakey crypto.Hash) error {
	err := rs.staticConn.WriteJSON(api.RegistrySubscriptionRequest{
		Action:  api.RegistrySubscriptionActionSubscribe,
		PubKey:  spk.String(),
		DataKey: datakey.String(),
	})
	if err != nil {
		return err
	}
	return nil
}

// Subscriptions returns the active subscriptions.
func (rs *RegistrySubscription) Subscriptions() error {
	err := rs.staticConn.WriteJSON(api.RegistrySubscriptionRequest{
		Action: api.RegistrySubscriptionActionSubscriptions,
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
		PubKey:  spk.String(),
		DataKey: datakey.String(),
	})
	if err != nil {
		return err
	}
	return nil
}
