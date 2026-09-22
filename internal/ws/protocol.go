/*
FILE: internal/ws/protocol.go

DESCRIPTION:
Wire structs and routing rules of the Hyperliquid WebSocket API.

CLIENT → SERVER:
  {"method":"subscribe","subscription":{...}}
  {"method":"unsubscribe","subscription":{...}}
  {"method":"ping"}
  {"method":"post","id":<number>,"request":{"type":"info"|"action","payload":{...}}}

SERVER → CLIENT (every frame is {"channel": "...", "data": ...}):
  "subscriptionResponse" : ack of subscribe / unsubscribe;
  "pong"                 : reply to ping;
  "post"                 : {"id","response":{"type":"info"|"action"|"error","payload"}};
  "error"                : {"data":"<text>"} — NOT documented, observed live
                           ("Already subscribed: ...", invalid requests);
  anything else          : a data push of the subscription with that name.
  The very first frame of a connection is the plain text
  "Websocket connection established." — it is not JSON and is skipped.

ROUTING:
A push does not echo its subscription, so the owner is derived from the channel
plus a discriminating field of the payload (same approach as the official
Python SDK's ws_msg_to_identifier):
  l2Book, bbo, activeAssetCtx, activeAssetData  → "<channel>:<data.coin>"
  activeSpotAssetCtx                            → "activeAssetCtx:<data.coin>"
  trades                                        → "trades:<data[0].coin>"
  candle                                        → "candle:<data.s>,<data.i>"
  user                                          → "userEvents"
  clearinghouseState, openOrders                → "<channel>[:<data.dex>]"
  everything else                               → "<channel>"
Consequences (documented limitations of the protocol, not of the SDK):
  - one l2Book subscription per coin per connection (pushes of different
    nSigFigs / mantissa aggregations are indistinguishable);
  - one user per connection for the user-scoped channels that carry no user
    field (orderUpdates, userEvents).
*/

package ws

import "github.com/tonymontanov/go-hyperliquid/internal/codec"

// Channel names with special handling.
const (
	channelPong                 string = "pong"
	channelPost                 string = "post"
	channelError                string = "error"
	channelSubscriptionResponse string = "subscriptionResponse"
)

// Post request / response types.
const (
	// PostTypeInfo — an info request tunnelled through the socket.
	PostTypeInfo string = "info"
	// PostTypeAction — a signed exchange action tunnelled through the socket.
	PostTypeAction string = "action"
	// PostTypeError — response type of a rejected post; payload is a string.
	PostTypeError string = "error"
)

// envelope — outer frame of every server message.
type envelope struct {
	Channel string           `json:"channel"`
	Data    codec.RawMessage `json:"data"`
}

// postData — payload of a "post" channel frame.
type postData struct {
	ID       uint64       `json:"id"`
	Response postResponse `json:"response"`
}

// postResponse — inner response of a post.
type postResponse struct {
	Type    string           `json:"type"`
	Payload codec.RawMessage `json:"payload"`
}

// RouteKey builds the routing key of a SUBSCRIPTION. Sections call it with the
// subscription type and its discriminator ("" when the channel has none).
func RouteKey(subscriptionType string, discriminator string) string {
	if discriminator == "" {
		return subscriptionType
	}
	return subscriptionType + ":" + discriminator
}

// pushRouteKey derives the routing key of an incoming PUSH.
func pushRouteKey(channel string, data []byte) string {
	switch channel {
	case "l2Book", "bbo", "activeAssetCtx", "activeAssetData":
		return channel + ":" + codec.GetString(data, "coin")
	case "activeSpotAssetCtx":
		return "activeAssetCtx:" + codec.GetString(data, "coin")
	case "trades":
		return "trades:" + codec.GetString(data, 0, "coin")
	case "candle":
		return "candle:" + codec.GetString(data, "s") + "," + codec.GetString(data, "i")
	case "user":
		return "userEvents"
	case "clearinghouseState", "openOrders":
		// Dex-scoped user channels: {"dex":"<dex>","user":...}. The first perp
		// dex is the empty string, which RouteKey renders without a suffix.
		return RouteKey(channel, codec.GetString(data, "dex"))
	default:
		return channel
	}
}
