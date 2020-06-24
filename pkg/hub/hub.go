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
	// subconn *nats.Conn
	// subchan chan *nats.Msg
	// Topics (i.e. realms)

	// gameStore       gameplay.GameStore
	// soughtGameStore gameplay.SoughtGameStore
	// config          *config.Config

	realmMutex sync.Mutex
	// Each realm has a list of clients in it.
	realms map[Realm]map[*Client]bool

	broadcastRealm chan RealmMessage

	// eventChan chan *entity.EventWrapper
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
		// subconn:         subconn,
		// subchan:         make(chan *nats.Msg, 64),
		// pubconn:           pub,
		// eventChan should be buffered to keep the game logic itself
		// as fast as possible.
		// eventChan:       make(chan *entity.EventWrapper, 50),
		// gameStore:       gameStore,
		// soughtGameStore: soughtGameStore,
		// config:          cfg,
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
		log.Error().Str("realm", string(realm)).Str("c.realm", string(c.realm)).
			Msg("client realm doesn't match")
	}

	delete(h.realms[realm], c)
	log.Debug().Msgf("deleted client from realm %v. New length %v", realm, len(
		h.realms[realm]))

	if len(h.realms[realm]) == 0 {
		delete(h.realms, realm)
	}

	delete(h.clients, c)
	log.Debug().Msgf("deleted client from clients. New length %v", len(
		h.clients))

	if (len(h.clientsByUserID[c.userID])) == 1 {
		delete(h.clientsByUserID, c.userID)
		log.Debug().Msgf("deleted client from clientsbyuserid. New length %v", len(
			h.clientsByUserID))
		return nil
	}
	// Otherwise, delete just one of the sockets.
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
	log.Debug().Msg("returning nil")
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
		// case message := <-h.broadcast:
		// 	for client := range h.clients {
		// 		select {
		// 		case client.send <- message:
		// 		default:
		// 			h.removeClient(client)
		// 		}
		// 	}

		case message := <-h.broadcastRealm:
			log.Debug().Str("realm", string(message.realm)).
				Int("clients", len(h.realms[message.realm])).
				Msg("sending broadcast message to realm")
			for client := range h.realms[message.realm] {
				select {
				case client.send <- message.msg:
				default:
					h.removeClient(client)
				}
			}
		}
	}
}

// RunGameEventHandler runs a separate loop that just handles game events,
// and forwards them to the appropriate sockets. All other events, like chat,
// should be handled in the Run function (I think, subject to change).
// func (h *Hub) RunGameEventHandler() {
// 	for {
// 		select {
// 		case w := <-h.eventChan:
// 			realm := Realm(w.GameID())
// 			err := h.sendToRealm(realm, w)
// 			if err != nil {
// 				log.Err(err).Str("realm", string(realm)).Msg("sending to realm")
// 			}
// 			n := len(h.eventChan)
// 			// Also send any backed up events to the appropriate realms.
// 			if n > 0 {
// 				log.Info().Int("backpressure", n).Msg("game event channel")
// 			}
// 			for i := 0; i < n; i++ {
// 				w := <-h.eventChan
// 				err := h.sendToRealm(realm, w)
// 				if err != nil {
// 					log.Err(err).Str("realm", string(realm)).Msg("sending to realm")
// 				}
// 			}

// 		}
// 	}
// }

// since addNewRealm can be called from different goroutines, we must protect
// the realm map.
// func (h *Hub) addNewRealm(id string) Realm {
// 	h.realmMutex.Lock()
// 	defer h.realmMutex.Unlock()
// 	realm := Realm(id)
// 	h.realms[realm] = make(map[*Client]bool)
// 	return realm
// }

// since addToRealm can be called multithreaded, we must protect the map with
// a mutex.
func (h *Hub) addToRealm(realm Realm, client *Client) {
	// adding to a realm means we must remove from another realm, since a client
	// can only be in one realm at once. We cannot have the realm map hold on
	// to stale clients in memory!

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

// func (h *Hub) deleteRealm(realm Realm) {
// 	// XXX: maybe disallow if users in realm.
// 	h.realmMutex.Lock()
// 	defer h.realmMutex.Unlock()
// 	delete(h.realms, realm)
// }

// func (h *Hub) broadcastEvent(w *entity.EventWrapper) error {
// 	bts, err := w.Serialize()
// 	if err != nil {
// 		return err
// 	}
// 	h.broadcast <- bts
// 	return nil
// }

// func (h *Hub) NewSeekRequest(cs *pb.SeekRequest) error {
// 	evt := entity.WrapEvent(cs, pb.MessageType_SEEK_REQUEST, "")
// 	return h.broadcastEvent(evt)
// }

// func (h *Hub) DeleteSeek(id string) error {
// 	// essentially just send the same game accepted event back.
// 	evt := entity.WrapEvent(&pb.GameAcceptedEvent{RequestId: id}, pb.MessageType_GAME_ACCEPTED_EVENT, "")
// 	return h.broadcastEvent(evt)
// }

// func (h *Hub) sendToClient(username string, evt *entity.EventWrapper) error {
// 	clients := h.clientsByUsername[username]
// 	if clients == nil {
// 		return errors.New("client not in list")
// 	}
// 	bts, err := evt.Serialize()
// 	if err != nil {
// 		return err
// 	}
// 	for _, client := range clients {
// 		client.send <- bts
// 	}
// 	return nil
// }

// func (h *Hub) sendRealmData(ctx context.Context, realm Realm, username string) {
// 	// Send the client relevant data depending on its realm(s). This
// 	// should be called when the client joins a realm.
// 	clients := h.clientsByUsername[username]
// 	for _, client := range clients {
// 		if realm == Realm(LobbyRealm) {
// 			msg, err := h.openSeeks(ctx)
// 			if err != nil {
// 				log.Err(err).Msg("getting open seeks")
// 				return
// 			}
// 			client.send <- msg
// 		} else {
// 			// assume it's a game
// 			// c.send <- c.hub.gameRefresher(realm)
// 			msg, err := h.gameRefresher(ctx, realm, username)
// 			if err != nil {
// 				log.Err(err).Msg("getting game info")
// 				return
// 			}
// 			client.send <- msg
// 		}
// 	}

// }

// // Not sure where else to put this
// func (h *Hub) openSeeks(ctx context.Context) ([]byte, error) {
// 	sgs, err := h.soughtGameStore.ListOpen(ctx)
// 	if err != nil {
// 		return nil, err
// 	}

// 	pbobj := &pb.SeekRequests{Requests: []*pb.SeekRequest{}}
// 	for _, sg := range sgs {
// 		pbobj.Requests = append(pbobj.Requests, sg.SeekRequest)
// 	}
// 	evt := entity.WrapEvent(pbobj, pb.MessageType_SEEK_REQUESTS, "")
// 	return evt.Serialize()
// }

// func (h *Hub) gameRefresher(ctx context.Context, realm Realm, username string) ([]byte, error) {
// 	// Assume the realm is a game ID. We can expand this later.
// 	entGame, err := h.gameStore.Get(ctx, string(realm))
// 	if err != nil {
// 		return nil, err
// 	}
// 	evt := entity.WrapEvent(entGame.HistoryRefresherEvent(),
// 		pb.MessageType_GAME_HISTORY_REFRESHER, entGame.GameID())
// 	return evt.Serialize()
// }
