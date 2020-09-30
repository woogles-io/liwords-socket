// Package sockets encapsulates all of the Websocket communication.
// It's part of the framework/drivers layer.
package sockets

import (
	"context"
	"net/http"
	"time"

	// "github.com/domino14/liwords/pkg/entity"
	"github.com/gorilla/websocket"
	"github.com/lithammer/shortuuid"
	"github.com/rs/zerolog/log"

	"github.com/domino14/liwords/pkg/entity"
	pb "github.com/domino14/liwords/rpc/api/proto/realtime"
)

const (
	// Time allowed to write a message to the peer.
	writeWait = 10 * time.Second

	// Time allowed to read the next pong message from the peer.
	pongWait = 60 * time.Second

	// Send pings to peer with this period. Must be less than pongWait.
	pingPeriod = (pongWait * 9) / 10

	// Maximum message size allowed from peer.
	maxMessageSize = 512
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		log.Debug().Msgf("host header %v", r.Header.Get("Host"))
		log.Debug().Msgf("origin %v", r.Header.Get("Origin"))
		// XXX: FIX THIS
		return true
	},
}

// Client is a middleman between the websocket connection and the hub.
type Client struct {
	hub *Hub

	// The websocket connection.
	conn *websocket.Conn

	// Buffered channel of outbound messages.
	send chan []byte

	authenticated bool
	username      string
	// userID is the user database ID, and it should contain solely of base57
	// chars, so we should use it as much as possible.
	userID string
	// It makes things much easier if a client socket is only allowed to be in one
	// single "realm" at a time. A realm is an abstract thing that is akin
	// to a "chatroom". Some example realms:
	// - lobby
	// - game-gameid   -- A realm just for the two players of a game.
	// - gametv-gameid -- A realm for observers of a game.
	// - tourney-tourneyid -- A realm for a tourney "room", with its own chat room and standings
	// If you want to join multiple realms, use multiple tabs (although, that's not a use
	// case we necessarily want to encourage)
	realm  Realm
	connID string
}

func (c *Client) sendError(err error) {
	evt := entity.WrapEvent(&pb.ErrorMessage{Message: err.Error()}, pb.MessageType_ERROR_MESSAGE, "")
	bts, err := evt.Serialize()
	if err != nil {
		// This really shouldn't happen.
		log.Err(err).Msg("error serializing error, lol")
		return
	}
	c.send <- bts
}

// readPump pumps messages from the websocket connection to the hub.
//
// The application runs readPump in a per-connection goroutine. The application
// ensures that there is at most one reader on a connection by executing all
// reads from this goroutine.
func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()
	c.conn.SetReadLimit(maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error { c.conn.SetReadDeadline(time.Now().Add(pongWait)); return nil })
	for {
		// _, message, err
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Err(err).Msg("unexpected-close")
			}
			// Probably a regular disconnect:
			log.Debug().Err(err).Msg("other-error-breaking-out")
			break
		}

		// Here is where we parse the message and send something off to the hub
		// potentially.

		err = c.hub.parseAndExecuteMessage(context.Background(), message, c)
		if err != nil {
			log.Err(err).Str("username", c.username).Msg("parse-and-execute-message")
			c.sendError(err)
			continue
		}

		// message = bytes.TrimSpace(bytes.Replace(message, newline, space, -1))
		// c.hub.broadcast <- message
	}
}

// writePump pumps messages from the hub to the websocket connection.
//
// A goroutine running writePump is started for each connection. The
// application ensures that there is at most one writer to a connection by
// executing all writes from this goroutine.
func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()
	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// The hub closed the channel.
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				log.Info().Msg("hub closed channel")
				// XXX: should we remove the connection here??
				// maybe not since the connection close happens in the defer.
				return
			}

			w, err := c.conn.NextWriter(websocket.BinaryMessage)
			if err != nil {
				return
			}
			w.Write(message)

			// Add queued messages to the current websocket message.
			n := len(c.send)
			for i := 0; i < n; i++ {
				w.Write(<-c.send)
			}
			if n > 0 {
				log.Debug().Int("msgs", n).Msg("wrote-additional-msgs")
			}

			if err := w.Close(); err != nil {
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// close connection with an error string.
func closeMessage(ws *websocket.Conn, errStr string) {
	// close code 1008 is used for a generic "policy violation" message.
	msg := websocket.FormatCloseMessage(websocket.ClosePolicyViolation, errStr)
	log.Debug().Str("closemsg", string(msg)).Msg("writing close message")
	err := ws.WriteMessage(websocket.CloseMessage, msg)
	if err != nil {
		log.Err(err).Msg("writing close message back to user")
	}
	ws.Close()
	return
}

// ServeWS handles websocket requests from the peer. This runs in its own
// goroutine.
func ServeWS(hub *Hub, w http.ResponseWriter, r *http.Request) {
	tokens, ok := r.URL.Query()["token"]
	if !ok || len(tokens[0]) < 1 {
		log.Error().Msg("token is missing")
		return
	}
	paths, ok := r.URL.Query()["path"]
	if !ok || len(paths[0]) < 1 {
		log.Error().Msg("path is missing")
		return
	}
	token := tokens[0]
	path := paths[0]

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Err(err).Msg("upgrading socket")
		return
	}

	client := &Client{
		hub:    hub,
		conn:   conn,
		send:   make(chan []byte, 256),
		connID: shortuuid.New(),
	}
	err = hub.socketLogin(client, token)
	if err != nil {
		log.Err(err).Msg("socket-login-error")
		conn.Close()
		return
	}

	client.hub.register <- client

	// Allow collection of memory referenced by the caller by doing all work in
	// new goroutines.
	go client.writePump()
	go client.readPump()

	//
	err = registerRealm(client, path, hub)
	if err != nil {
		log.Err(err).Msg("register-realm-error")
		conn.Close()
	}
}
