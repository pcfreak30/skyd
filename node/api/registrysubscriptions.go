package api

import (
	"container/list"
	"encoding/hex"
	"fmt"
	"net/http"
	"sync"
	"time"

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
	// RegistrySubscriptionActionSubscribe subscribes to a new entry.
	RegistrySubscriptionActionSubscribe = "subscribe"

	// RegistrySubscriptionActionUnsubscribe unsubscribes from an entry.
	RegistrySubscriptionActionUnsubscribe = "unsubscribe"

	// RegistrySubscriptionActionSubscriptions requests the active subscriptions
	// from the portal.
	RegistrySubscriptionActionSubscriptions = "subscriptions"
)

const (
	// RegistrySubscriptionResponseTypeNotification is the type for a
	// response that notifies the subscriber of an update to one of their
	// subscriptions.
	RegistrySubscriptionResponseTypeNotification = "notification"

	// RegistrySubscriptionResponseTypeSubscriptions is the type for a
	// response that tells the subscriber about all active subscriptions.
	RegistrySubscriptionResponseTypeSubscriptions = "activesubscriptions"
)

type (
	// RegistrySubscriptionResponseCommon foo
	RegistrySubscriptionResponseCommon struct {
		// Mandatory fields. These always need to be set for every response.
		ResponseType string `json:"responsetype"`
	}

	// RegistrySubscriptionResponseNotification foo
	RegistrySubscriptionResponseNotification struct {
		RegistrySubscriptionResponseCommon

		DataKey   string                    `json:"datakey"`
		PubKey    string                    `json:"pubkey"`
		Signature string                    `json:"signature,omitempty"`
		Data      string                    `json:"data,omitempty"`
		Revision  uint64                    `json:"revision"`
		Type      modules.RegistryEntryType `json:"type,omitempty"`
	}

	// RegistrySubscriptionResponseSubscriptions foo
	RegistrySubscriptionResponseSubscriptions struct {
		RegistrySubscriptionResponseCommon

		Subscriptions []string `json:"subscriptions"`
	}

	// RegistrySubscriptionResponseError foo
	RegistrySubscriptionResponseError struct {
		Error string `json:"error"`
	}
)

func newRegistrySubscriptionError(err string) RegistrySubscriptionResponseError {
	return RegistrySubscriptionResponseError{
		Error: err,
	}
}

// RegistrySubscriptionRequest defines the request the client sends to the
// server to trigger actions such as subscribing and unsubscribing.
type RegistrySubscriptionRequest struct {
	Action  string `json:"action"`
	PubKey  string `json:"pubkey,omitempty"`
	DataKey string `json:"datakey,omitempty"`
}

// queuedNotification describes a queued websocket update.
type queuedNotification struct {
	staticResponse   interface{}
	staticNotifyTime time.Time
}

// notificationQueue holds all undelivered websocket updates.
type notificationQueue struct {
	*list.List
}

// newNotificationQueue creates a new queue.
func newNotificationQueue() *notificationQueue {
	return &notificationQueue{
		List: list.New(),
	}
}

// Pop removes the first element of the queue.
func (queue *notificationQueue) Pop() *queuedNotification {
	mr := queue.Front()
	if mr == nil {
		return nil
	}
	return queue.List.Remove(mr).(*queuedNotification)
}

// skynetRegistrySubscriptionHandler handles websocket subscriptions to the registry.
func (api *API) skynetRegistrySubscriptionHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	// Make sure the limit and delay are set.
	bandwidthLimitStr := req.FormValue("bandwidthlimit")
	if bandwidthLimitStr == "" {
		WriteError(w, Error{"bandwidthlimit param not specified"}, http.StatusBadRequest)
		return
	}
	notificationDelayStr := req.FormValue("notificationdelay")
	if notificationDelayStr == "" {
		WriteError(w, Error{"notificationdelay param not specified"}, http.StatusBadRequest)
		return
	}

	// Parse them.
	var bandwidthLimit uint64
	_, err := fmt.Sscan(bandwidthLimitStr, &bandwidthLimit)
	if err != nil {
		WriteError(w, Error{"failed to parse bandwidthlimit" + err.Error()}, http.StatusBadRequest)
		return
	}
	var notificationDelayMS uint64
	_, err = fmt.Sscan(notificationDelayStr, &notificationDelayMS)
	if err != nil {
		WriteError(w, Error{"failed to parse notificationdelay" + err.Error()}, http.StatusBadRequest)
		return
	}
	notificationDelay := time.Millisecond * time.Duration(notificationDelayMS)

	// Upgrade connection to use websocket.
	upgrader.CheckOrigin = func(r *http.Request) bool { return true } // TODO: this is not safe
	c, err := upgrader.Upgrade(w, req, nil)
	if err != nil {
		handleSkynetError(w, "failed to upgrade connection to websocket connection", err)
		return
	}
	defer c.Close()

	// Compute how many notifications per second we want to serve.
	notificationsPerSecond := float64(bandwidthLimit) / RegistrySubscriptionNotificationSize

	// Compute how much time needs to pass between notifications to reach
	// that limit.
	var timeBetweenNotifications time.Duration
	if bandwidthLimit == 0 {
		timeBetweenNotifications = 0
	} else {
		timeBetweenNotifications = time.Duration(float64(time.Second) / notificationsPerSecond)
	}

	// Declare a handler for queuing responses.
	var queueMu sync.Mutex
	queue := newNotificationQueue()
	wakeChan := make(chan struct{}, 1)
	var lastWrite time.Time
	queueResponse := func(resp interface{}) {
		queueMu.Lock()
		// Compute lastWrite1 by adding he delay to the current time.
		lastWrite1 := time.Now().Add(notificationDelay)

		// Compute lastWrite2 by adding the minimum time between
		// notifications to the last update we gave the client.
		lastWrite2 := lastWrite.Add(timeBetweenNotifications)

		// We push the next update, at the time that is further in the
		// future.
		if lastWrite1.After(lastWrite2) {
			lastWrite = lastWrite1
		} else {
			lastWrite = lastWrite2
		}
		queue.PushBack(&queuedNotification{
			staticResponse:   resp,
			staticNotifyTime: lastWrite,
		})
		queueMu.Unlock()
		select {
		case wakeChan <- struct{}{}:
		default:
		}
	}

	// specific handler for queueing notification.
	queueNotification := func(srv skymodules.RegistryEntry) error {
		var sig string
		if srv.Signature != (crypto.Signature{}) {
			sig = hex.EncodeToString(srv.Signature[:])
		}
		queueResponse(RegistrySubscriptionResponseNotification{
			RegistrySubscriptionResponseCommon: RegistrySubscriptionResponseCommon{
				ResponseType: RegistrySubscriptionResponseTypeNotification,
			},
			DataKey:   srv.Tweak.String(),
			PubKey:    srv.PubKey.String(),
			Signature: sig,
			Data:      hex.EncodeToString(srv.Data),
			Revision:  srv.Revision,
			Type:      srv.Type,
		})
		return nil
	}

	// specific handler for queueing subscriptions response.
	queueSubscriptions := func(eids []modules.RegistryEntryID) {
		subs := make([]string, 0, len(eids))
		for _, eid := range eids {
			subs = append(subs, crypto.Hash(eid).String())
		}
		queueResponse(RegistrySubscriptionResponseSubscriptions{
			RegistrySubscriptionResponseCommon: RegistrySubscriptionResponseCommon{
				ResponseType: RegistrySubscriptionResponseTypeSubscriptions,
			},
			Subscriptions: subs,
		})
	}

	// Start a worker for pushing notifications.
	go func() {
		for {
			// Check for shutdown.
			select {
			case <-req.Context().Done():
				return
			default:
			}

			queueMu.Lock()
			next := queue.Pop()
			queueMu.Unlock()
			if next == nil {
				// No work. Wait for wake signal.
				select {
				case <-req.Context().Done():
					return
				case <-wakeChan:
				}
				continue
			}

			// Sleep until the notification time.
			select {
			case <-req.Context().Done():
				return
			case <-time.After(time.Until(next.staticNotifyTime)):
			}

			err := c.WriteJSON(next.staticResponse)
			if err != nil {
				msg := fmt.Sprintf("failed to notify client: %v", err)
				_ = c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseAbnormalClosure, msg))
			}
		}
	}()

	// Start subscription.
	subscriber, err := api.renter.NewRegistrySubscriber(queueNotification)
	if err != nil {
		msg := fmt.Sprintf("failed to create subscriber: %v", err)
		_ = c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseAbnormalClosure, msg))
		return
	}

	// Unsubscribe when the connection is closed.
	c.SetCloseHandler(func(_ int, _ string) error {
		return subscriber.Close()
	})

	// Forward incoming requests to the subscription manager.
	var r RegistrySubscriptionRequest
	for {
		err = c.ReadJSON(&r)
		if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
			return // client closed connection gracefully
		}
		if err != nil {
			msg := fmt.Sprintf("failed to read JSON request: %v", err)
			c.WriteJSON(newRegistrySubscriptionError(msg))
			fmt.Println("msg", msg)
			return
		}
		switch r.Action {
		case RegistrySubscriptionActionSubscribe:
			var spk types.SiaPublicKey
			if err := spk.LoadString(r.PubKey); err != nil {
				msg := fmt.Sprintf("failed to parse pubkey: %v", err)
				c.WriteJSON(newRegistrySubscriptionError(msg))
				continue
			}
			var dataKey crypto.Hash
			if err := dataKey.LoadString(r.DataKey); err != nil {
				msg := fmt.Sprintf("failed to parse datakey: %v", err)
				c.WriteJSON(newRegistrySubscriptionError(msg))
				continue
			}
			srv := subscriber.Subscribe(spk, dataKey)
			if srv != nil {
				if err := queueNotification(*srv); err != nil {
					msg := fmt.Sprintf("failed to notify client: %v", err)
					c.WriteJSON(newRegistrySubscriptionError(msg))
					continue
				}
			}
		case RegistrySubscriptionActionUnsubscribe:
			var spk types.SiaPublicKey
			if err := spk.LoadString(r.PubKey); err != nil {
				msg := fmt.Sprintf("failed to parse pubkey: %v", err)
				c.WriteJSON(newRegistrySubscriptionError(msg))
				continue
			}
			var dataKey crypto.Hash
			if err := dataKey.LoadString(r.DataKey); err != nil {
				msg := fmt.Sprintf("failed to parse datakey: %v", err)
				c.WriteJSON(newRegistrySubscriptionError(msg))
				continue
			}
			subscriber.Unsubscribe(modules.DeriveRegistryEntryID(spk, dataKey))
		case RegistrySubscriptionActionSubscriptions:
			queueSubscriptions(subscriber.Subscriptions())
		default:
			c.WriteJSON(RegistrySubscriptionResponseError{Error: "unknown action"})
		}
	}
}
