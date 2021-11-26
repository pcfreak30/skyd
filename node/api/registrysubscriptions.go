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
)

// RegistrySubscriptionResponse is the datatype defining the response the server
// sends to a client.
type RegistrySubscriptionResponse struct {
	Error string `json:"error"`

	DataKey   crypto.Hash        `json:"datakey"`
	PubKey    types.SiaPublicKey `json:"pubkey"`
	Signature string             `json:"signature"`

	Data     string                    `json:"data"`
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

// queuedNotification describes a queued websocket update.
type queuedNotification struct {
	staticSRV        skymodules.RegistryEntry
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
	// Upgrade connection to use websocket.
	c, err := upgrader.Upgrade(w, req, nil)
	if err != nil {
		handleSkynetError(w, "failed to upgrade connection to websocket connection", err)
		return
	}
	defer c.Close()

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
	_, err = fmt.Sscan(bandwidthLimitStr, &bandwidthLimit)
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

	// Declare a handler for queuing notifications.
	var queueMu sync.Mutex
	queue := newNotificationQueue()
	wakeChan := make(chan struct{}, 1)
	var lastWrite time.Time
	queueNotification := func(srv skymodules.RegistryEntry) error {
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
			staticSRV:        srv,
			staticNotifyTime: lastWrite,
		})
		queueMu.Unlock()
		select {
		case wakeChan <- struct{}{}:
		default:
		}
		return nil
	}

	// Start a worker for pushing notifications.
	go func() {
		for {
			select {
			case <-req.Context().Done():
				return
			case <-wakeChan:
			}

			queueMu.Lock()
			next := queue.Pop()
			queueMu.Unlock()
			if next == nil {
				continue
			}

			// Sleep until the notification time.
			select {
			case <-req.Context().Done():
				return
			case <-time.After(time.Until(next.staticNotifyTime)):
			}

			err := c.WriteJSON(RegistrySubscriptionResponse{
				Error:     "",
				DataKey:   next.staticSRV.Tweak,
				PubKey:    next.staticSRV.PubKey,
				Signature: hex.EncodeToString(next.staticSRV.Signature[:]),
				Data:      hex.EncodeToString(next.staticSRV.Data),
				Revision:  next.staticSRV.Revision,
				Type:      next.staticSRV.Type,
			})
			if err != nil {
				msg := fmt.Sprintf("failed to notify client: %v", err)
				_ = c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseAbnormalClosure, msg))
			}
		}
	}()

	// Start subscription.
	subscriber, err := api.renter.NewRegistrySubscriber(queueNotification)
	if err != nil {
		c.WriteJSON(RegistrySubscriptionResponse{Error: fmt.Sprintf("failed to create subscriber: %v", err)})
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
			_ = c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseAbnormalClosure, msg))
			return
		}
		switch r.Action {
		case RegistrySubscriptionActionSubscribe:
			srv := subscriber.Subscribe(r.PubKey, r.DataKey)
			if srv != nil {
				if err := queueNotification(*srv); err != nil {
					// This probably won't reach the client
					// but try a graceful close anyway.
					msg := fmt.Sprintf("failed to notify client: %v", err)
					_ = c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseAbnormalClosure, msg))
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
