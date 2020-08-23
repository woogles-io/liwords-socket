package sockets

import (
	"errors"
	"sync"

	"github.com/domino14/liwords-socket/pkg/config"
	"github.com/rs/zerolog/log"
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
}

func NewHub(cfg *config.Config) (*Hub, error) {
	pubsub, err := newPubSub(cfg.NatsURL)
	if err != nil {
		return nil, err
	}

	return &Hub{
		// broadcast:         make(chan []byte),
		broadcastRealm:  make(chan RealmMessage),
		register:        make(chan *Client),
		unregister:      make(chan *Client),
		clients:         make(map[*Client]Realm),
		clientsByUserID: make(map[string]map[*Client]bool),
		realms:          make(map[Realm]map[*Client]bool),
		pubsub:          pubsub,
	}, nil
}

func (h *Hub) addClient(client *Client) error {
	// no need to protect with mutex, only called from
	// single-threaded Run
	h.clients[client] = NullRealm
	byUser := h.clientsByUserID[client.userID]

	if byUser == nil {
		h.clientsByUserID[client.userID] = make(map[*Client]bool)
	}
	h.clientsByUserID[client.userID][client] = true

	return nil
}

func (h *Hub) removeClient(c *Client) error {
	// no need to protect with mutex, only called from
	// single-threaded Run
	log.Debug().Str("client", c.username).Str("userid", c.userID).Msg("removing client")
	close(c.send)

	realm := h.clients[c]
	if c.realm != realm {
		// Error here before the panic. {"level":"error","realm":"","c.realm":"lobby"

		log.Error().Str("realm", string(realm)).Str("c.realm", string(c.realm)).
			Msg("client realm doesn't match")
	}

	delete(h.realms[realm], c)
	//{"level":"debug","time":"2020-08-22T20:40:33Z","message":"deleted client from realm . New length 0"}
	log.Debug().Msgf("deleted client from realm %v. New length %v", realm, len(
		h.realms[realm]))

	if len(h.realms[realm]) == 0 {
		delete(h.realms, realm)
	}

	delete(h.clients, c)
	// {"level":"debug","time":"2020-08-22T20:40:33Z","message":"deleted client from clients. New length 2"}
	log.Debug().Msgf("deleted client from clients. New length %v", len(
		h.clients))

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
	// Otherwise, delete just one of the sockets.
	// {"level":"debug","userid":"uNNCgSXCB2LEMjN6shBp7J","numconn":2,"time":"2020-08-22T20:40:33Z","message":"non-o
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

	if len(h.realms[realm]) == 0 {
		return errors.New("realm is empty")
	}
	h.broadcastRealm <- RealmMessage{realm: realm, msg: msg}
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
				case client.send <- message.msg:
				default:
					h.removeClient(client)
				}
			}
		}
	}
}

// since addToRealm can be called multithreaded, we must protect the map with
// a mutex.
func (h *Hub) addToRealm(realm Realm, client *Client) {
	// adding to a realm means we must remove from another realm, since a client
	// can only be in one realm at once. We cannot have the realm map hold on
	// to stale clients in memory!

	if client.realm == realm {
		log.Info().Str("realm", string(realm)).Msg("user already in realm")
		return
	}

	h.realmMutex.Lock()
	defer h.realmMutex.Unlock()

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

func (h *Hub) removeFromRealm(client *Client) {
	h.realmMutex.Lock()
	defer h.realmMutex.Unlock()
	realm := client.realm
	if realm == NullRealm {
		log.Error().Msg("tried to remove from null realm")
		return
	}
	if h.realms[realm] != nil {
		delete(h.realms[realm], client)
	} else {
		log.Error().Str("realm", string(realm)).Msg("tried to delete a from a null realm")
	}
	// Delete realm if empty.
	if len(h.realms[realm]) == 0 {
		delete(h.realms, realm)
	}
	h.clients[client] = NullRealm
}
