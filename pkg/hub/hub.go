package sockets

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/dgrijalva/jwt-go"
	"github.com/domino14/liwords-socket/pkg/config"
	"github.com/rs/zerolog/log"
	"google.golang.org/protobuf/proto"

	pb "github.com/domino14/liwords/rpc/api/proto/realtime"
)

// A Realm is basically a set of clients. It can be thought of as a game room,
// or perhaps a lobby.
type Realm string

const NullRealm Realm = ""
const LobbyRealm Realm = "lobby"

// A RealmMessage is a message that should be sent to a socket Realm.
type RealmMessage struct {
	realm Realm
	msg   []byte
}

// A UserMessage is a message that should be sent to a user (across all
// of the sockets that they are connected to, unless the channel says otherwise).
type UserMessage struct {
	userID  string
	channel string
	msg     []byte
}

// Hub maintains the set of active clients and broadcasts messages to the
// clients.
type Hub struct {
	// Registered clients.
	clients         map[*Client]Realm
	clientsByUserID map[string]map[*Client]bool

	// Inbound messages from the clients.
	// broadcast chan []byte

	// Register requests from the clients.
	register chan *Client

	// Unregister requests from clients.
	unregister chan *Client

	pubsub *PubSub

	realmMutex sync.Mutex
	// Each realm has a list of clients in it.
	realms map[Realm]map[*Client]bool

	broadcastRealm chan RealmMessage
	broadcastUser  chan UserMessage
}

func NewHub(cfg *config.Config) (*Hub, error) {
	pubsub, err := newPubSub(cfg.NatsURL)
	if err != nil {
		return nil, err
	}

	return &Hub{
		// broadcast:         make(chan []byte),
		broadcastRealm:  make(chan RealmMessage),
		broadcastUser:   make(chan UserMessage),
		register:        make(chan *Client),
		unregister:      make(chan *Client),
		clients:         make(map[*Client]Realm),
		clientsByUserID: make(map[string]map[*Client]bool),
		realms:          make(map[Realm]map[*Client]bool),
		pubsub:          pubsub,
	}, nil
}

func (h *Hub) addClient(client *Client) error {
	// This function is called from Run() and also indirectly from
	// the ServeWS function, which runs in another goroutine.
	// Therefore, we must protect the maps with a mutex.
	// h.realmMutex.Lock()
	// defer h.realmMutex.Unlock()

	// Add client to appropriate maps
	byUser := h.clientsByUserID[client.userID]
	if byUser == nil {
		h.clientsByUserID[client.userID] = make(map[*Client]bool)
	}
	// Add the new user ID to the map.
	h.clientsByUserID[client.userID][client] = true
	h.clients[client] = client.realm

	// add to the realm map.
	if client.tempRealm != NullRealm {
		h.addToRealm(client.tempRealm, client)
		client.tempRealm = NullRealm
	}

	// Meow, depending on the realm, request that the API publish
	// initial information pertaining to this realm. For example,
	// lobby visitors will want to see a list of sought games,
	// or newcomers to a game realm will want to see the history
	// of the game so far.
	return h.sendRealmInitInfo(client.realm, client.userID)
	// The API will publish the initial realm information to this user's channel.
	// (user.userID - see pubsub.go)

}

func (h *Hub) removeClient(c *Client) error {
	// no need to protect with mutex, only called from
	// single-threaded Run
	log.Debug().Str("client", c.username).Str("connid", c.connID).Str("userid", c.userID).Msg("removing client")
	close(c.send)

	realm := h.clients[c]
	if c.realm != realm {
		// There is a problem here. If this line triggers then we will delete
		// the client from the wrong map -- somewhere the client might not actually
		// be. This will cause a panic if we then try to send data to the client
		// on a closed channel.
		log.Error().Str("realm", string(realm)).Str("c.realm", string(c.realm)).
			Msg("client realm doesn't match")
		// Try deleting from c.realm map just in case
		delete(h.realms[c.realm], c)
	}

	delete(h.realms[realm], c)
	log.Debug().Msgf("deleted client %v from realm %v. New length %v", c.connID, realm, len(
		h.realms[realm]))

	if len(h.realms[realm]) == 0 {
		delete(h.realms, realm)
	}

	delete(h.clients, c)
	log.Debug().Msgf("deleted client %v from clients. New length %v", c.connID, len(
		h.clients))

	// xxx: trigger leaveSite even if this isn't the last tab. We would
	// pass in a conn ID of some sort. We would associate outgoing
	// seek / match requests with a conn ID.

	h.pubsub.natsconn.Publish(extendTopic(c, "ipc.pb.leaveTab"), []byte{})

	if (len(h.clientsByUserID[c.userID])) == 1 {
		delete(h.clientsByUserID, c.userID)
		log.Debug().Msgf("deleted client from clientsbyuserid. New length %v", len(
			h.clientsByUserID))

		// Tell the backend that this user has left the site. The backend
		// can then do things (cancel seek requests, inform players their
		// opponent has left, etc).
		h.pubsub.natsconn.Publish(extendTopic(c, "ipc.pb.leaveSite"), []byte{})
		return nil
	}
	// Otherwise, delete just the right socket (this one: c)
	log.Debug().Interface("userid", c.userID).Int("numconn", len(h.clientsByUserID[c.userID])).
		Msg("non-one-num-conns")
	delete(h.clientsByUserID[c.userID], c)
	return nil
}

func (h *Hub) sendToRealm(realm Realm, msg []byte) error {
	log.Debug().
		Str("realm", string(realm)).
		Int("inrealm", len(h.realms[realm])).
		Msg("sending to realm")

	h.broadcastRealm <- RealmMessage{realm: realm, msg: msg}
	return nil
}

func (h *Hub) sendToUser(userID string, msg []byte) error {
	log.Debug().
		Str("userid", userID).
		Msg("sending to all user sockets")
	h.broadcastUser <- UserMessage{userID: userID, msg: msg}
	return nil
}

func (h *Hub) sendToUserChannel(userID string, msg []byte, channel string) error {
	log.Debug().Str("userid", userID).Str("channel", channel).Msg("sending to channel sockets")
	h.broadcastUser <- UserMessage{userID: userID, msg: msg, channel: channel}
	return nil
}

func (h *Hub) Run() {
	go h.PubsubProcess()
	for {
		select {
		case client := <-h.register:
			err := h.addClient(client)
			if err != nil {
				log.Err(err).Msg("error-adding-client")
			}

		case client := <-h.unregister:
			if _, ok := h.clients[client]; ok {
				err := h.removeClient(client)
				if err != nil {
					log.Err(err).Msg("error-removing-client")
				}
				log.Info().Str("username", client.username).Msg("unregistered-client")
			} else {
				log.Error().Msg("unregistered-but-not-in-map")
			}

		case message := <-h.broadcastRealm:
			// {"level":"debug","realm":"lobby","clients":2,"time":"2020-08-22T20:40:40Z","message":"sending broadcast message to realm"}
			log.Debug().Str("realm", string(message.realm)).
				Int("clients", len(h.realms[message.realm])).
				Msg("sending broadcast message to realm")
			for client := range h.realms[message.realm] {
				select {
				// XXX: got a panic: send on closed channel from this line:
				// I think this is because the client wasn't done registering
				// (register-realm-path) before it was disconnected abnormally.
				case client.send <- message.msg:
				default:
					h.removeClient(client)
				}
			}

		case message := <-h.broadcastUser:
			log.Debug().Str("user", string(message.userID)).
				Msg("sending to all user sockets")
			// Send the message to every socket belonging to this user.
			for client := range h.clientsByUserID[message.userID] {
				realmToChannel := strings.ReplaceAll(string(client.realm), "-", ".")
				if message.channel != "" && !strings.HasPrefix(message.channel, realmToChannel) {
					// if the message has a channel attached to it, it needs to be
					// a prefix of the realm in order to be delivered.
					continue
				}
				select {
				case client.send <- message.msg:
				default:
					h.removeClient(client)
				}
			}
		}
	}
}

func (h *Hub) addToRealm(realm Realm, client *Client) {
	// adding to a realm means we must remove from another realm, since a client
	// can only be in one realm at once. We cannot have the realm map hold on
	// to stale clients in memory!

	if client.realm != NullRealm {
		log.Debug().Msgf("before adding to realm %v, deleting from realm %v",
			realm, client.realm)
		if h.realms[client.realm] != nil {
			delete(h.realms[client.realm], client)
		} else {
			log.Error().Str("realm", string(client.realm)).Msg("tried to delete a from a null realm")
		}
		if len(h.realms[client.realm]) == 0 {
			delete(h.realms, client.realm)
		}
	}

	// Now add to the given realm.

	// log.Debug().Msgf("h.reamls[realm] %v (%v) %v", h.realms, realm, h.realms[realm])
	if h.realms[realm] == nil {
		h.realms[realm] = make(map[*Client]bool)
	}
	client.realm = realm
	h.realms[realm][client] = true
	h.clients[client] = realm
}

func (h *Hub) socketLogin(c *Client) error {

	token, err := jwt.Parse(c.connToken, func(token *jwt.Token) (interface{}, error) {
		// Don't forget to validate the alg is what you expect:
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("Unexpected signing method: %v", token.Header["alg"])
		}

		// hmacSampleSecret is a []byte containing your secret, e.g. []byte("my_secret_key")
		return []byte(os.Getenv("SECRET_KEY")), nil
	})
	if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {

		c.authenticated, ok = claims["a"].(bool)
		if !ok {
			return errors.New("malformed token - a")
		}
		c.username, ok = claims["unn"].(string)
		if !ok {
			return errors.New("malformed token - unn")
		}

		c.userID = claims["uid"].(string)
		log.Debug().Str("username", c.username).Str("userID", c.userID).
			Bool("auth", c.authenticated).Msg("socket connection")
	}
	if err != nil {
		log.Err(err).Msg("socket-login-failure")
	}
	if !token.Valid {
		return errors.New("invalid token")
	}
	return err
}

// Note: This is a BLOCKING call -- see natsconn.Request below.
func registerRealm(c *Client, path string, h *Hub) error {
	// There are a variety of possible realms that a person joining a game
	// can be in. We should not trust the user to send the right realm
	// (for example they can send a TV mode realm if they're a player
	// in the game or vice versa). The backend should determine the right realm
	// and assign it accordingly.
	log.Debug().Str("connid", c.connID).Str("path", path).Msg("register-realm-path")
	var realm string

	if path == "/" {
		// This is the lobby; no need to request a realm.
		realm = string(LobbyRealm)
	} else {
		// First, create a request and send to the IPC api:
		rrr := &pb.RegisterRealmRequest{}
		rrr.Realm = path
		rrr.UserId = c.userID
		data, err := proto.Marshal(rrr)
		if err != nil {
			return err
		}
		resp, err := h.pubsub.natsconn.Request("ipc.request.registerRealm", data, ipcTimeout)
		if err != nil {
			log.Err(err).Msg("timeout registering realm")
			return err
		}
		log.Debug().Msg("got response from registerRealmReq")
		// The response contains the correct realm for the user.
		rrResp := &pb.RegisterRealmResponse{}
		err = proto.Unmarshal(resp.Data, rrResp)
		if err != nil {
			return err
		}
		realm = rrResp.Realm
	}

	c.tempRealm = Realm(realm)
	return nil

}

func (h *Hub) sendRealmInitInfo(realm Realm, userID string) error {
	req := &pb.InitRealmInfo{
		Realm:  string(realm),
		UserId: userID,
	}
	data, err := proto.Marshal(req)
	if err != nil {
		return err
	}
	log.Debug().Interface("initRealmInfo", req).Msg("req-init-realm-info")

	return h.pubsub.natsconn.Publish("ipc.pb.initRealmInfo", data)
}
