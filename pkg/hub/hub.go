package sockets

import (
	"context"
	"errors"

	// "github.com/domino14/liwords/pkg/config"
	// "github.com/domino14/liwords/pkg/entity"
	// "github.com/domino14/liwords/pkg/gameplay"
	// pb "github.com/domino14/liwords/rpc/api/proto"
	"github.com/rs/zerolog/log"
)

// A Realm is basically a set of clients. It can be thought of as a game room,
// or perhaps a lobby.
type Realm string

const LobbyRealm Realm = "lobby"

type RealmMessage struct {
	realm Realm
	msg   []byte
}

// Hub maintains the set of active clients and broadcasts messages to the
// clients.
type Hub struct {
	// Registered clients.
	clients           map[*Client]map[Realm]bool
	clientsByUsername map[string]map[*Client]bool

	// Inbound messages from the clients.
	// broadcast chan []byte

	// Register requests from the clients.
	register chan *Client

	// Unregister requests from clients.
	unregister chan *Client

	// Topics (i.e. realms)

	// gameStore       gameplay.GameStore
	// soughtGameStore gameplay.SoughtGameStore
	// config          *config.Config

	// realmMutex sync.Mutex
	// Each realm has a list of clients in it.
	realms map[Realm]map[*Client]bool

	// broadcastRealm chan RealmMessage

	// eventChan chan *entity.EventWrapper
}

func NewHub(gameStore gameplay.GameStore, soughtGameStore gameplay.SoughtGameStore,
	cfg *config.Config) *Hub {
	return &Hub{
		// broadcast:         make(chan []byte),
		// broadcastRealm:    make(chan RealmMessage),
		register:          make(chan *Client),
		unregister:        make(chan *Client),
		clients:           make(map[*Client]map[Realm]bool),
		clientsByUsername: make(map[string]map[*Client]bool),
		realms:            make(map[Realm]map[*Client]bool),
		// eventChan should be buffered to keep the game logic itself
		// as fast as possible.
		// eventChan:       make(chan *entity.EventWrapper, 50),
		// gameStore:       gameStore,
		// soughtGameStore: soughtGameStore,
		// config:          cfg,
	}
}

func (h *Hub) removeClient(c *Client) error {
	// no need to protect with mutex, only called from
	// single-threaded Run
	log.Debug().Str("client", c.username).Msg("removing client")
	close(c.send)

	for realm := range h.clients[c] {
		delete(h.realms[realm], c)
		if len(h.realms[realm] == 0) {
			delete(h.realms, realm)
			err := h.subconn.Unsubscribe(t)
			if err != nil {
				return err
			}
		}
	}

	delete(h.clients, c)
	if (len(h.clientsByUsername[c.username])) == 1 {
		delete(h.clientsByUsername, c.username)
		return nil
	}
	// Otherwise, delete just one of the sockets.
	log.Debug().Msg("multiple-connections-del-one")
	delete(h.clientsByUsername[c.username], c)
	return nil
}

func (h *Hub) addClient(client *Client, realm Realm) error {
	// no need to protect with mutex, only called from
	// single-threaded Run
	h.clients[client] = make(map[Realm]bool)
	h.clients[client][realm] = true
	byUser := h.clientsByUsername[client.username]

	if byUser == nil {
		h.clientsByUsername[client.username] = make(map[*Client]bool)
	} else {
		h.clientsByUsername[client.username][client] = true
	}

	, ok :=h.realms[realm]
	if !ok {
		h.realms[realm] = make(map[*Client]bool)
		err := h.subconn.Subscribe(realm)
		if err != nil {
			return err
		}
	}
	h.realms[realm][client] = true

	return nil
}

func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			// Add every client to the "global" realm no matter what.
			// We can use this for notifications like private messages, etc.
			err := h.addClient(client, Realm("global"))
			if err != nil {
				log.Err(err).Msg("error-adding-client")
			}

		case client := <-h.unregister:
			if _, ok := h.clients[client]; ok {
				err := h.removeClient(client)
				if err != nil {
					log.Err(err).Msg("error-adding-client")
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

		// case message := <-h.broadcastRealm:
		// 	log.Debug().Str("realm", string(message.realm)).
		// 		Msg("sending broadcast message to realm")
		// 	for client := range h.realms[message.realm] {
		// 		select {
		// 		case client.send <- message.msg:
		// 		default:
		// 			h.removeClient(client)
		// 		}
		// 	}
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
func (h *Hub) addNewRealm(id string) Realm {
	h.realmMutex.Lock()
	defer h.realmMutex.Unlock()
	realm := Realm(id)
	h.realms[realm] = make(map[*Client]bool)
	return realm
}

// Note that these functions are not meant to be used with more than 1
// single connection!
// XXX: We must rewrite these, probably using something like Redis.
// we'd need the concept of a _connection_ dictionary instead of it
// being indexed by username, but still have it easily be searched by
// username?
func (h *Hub) addToRealm(realm Realm, clientUsername string) {
	clients := h.clientsByUsername[clientUsername]

	h.realmMutex.Lock()
	defer h.realmMutex.Unlock()
	// log.Debug().Msgf("h.reamls[realm] %v (%v) %v", h.realms, realm, h.realms[realm])
	if h.realms[realm] == nil {
		h.realms[realm] = make(map[*Client]bool)
	}
	for _, c := range clients {
		h.realms[realm][c] = true
		c.realms[realm] = true
	}
}

func (h *Hub) removeFromRealm(realm Realm, clientUsername string) {
	clients := h.clientsByUsername[clientUsername]
	h.realmMutex.Lock()
	defer h.realmMutex.Unlock()
	for _, c := range clients {
		delete(h.realms[realm], c)
	}

}

func (h *Hub) deleteRealm(realm Realm) {
	// XXX: maybe disallow if users in realm.
	h.realmMutex.Lock()
	defer h.realmMutex.Unlock()
	delete(h.realms, realm)
}

func (h *Hub) sendToRealm(realm Realm, w *entity.EventWrapper) error {
	bytes, err := w.Serialize()
	if err != nil {
		return err
	}
	log.Debug().Interface("evt", w).
		Str("realm", string(realm)).
		Int("inrealm", len(h.realms[realm])).
		Msg("sending to realm")

	if len(h.realms[realm]) == 0 {
		return errors.New("realm is empty")
	}
	h.broadcastRealm <- RealmMessage{realm: realm, msg: bytes}
	log.Debug().Msg("returning nil")
	return nil
}

func (h *Hub) broadcastEvent(w *entity.EventWrapper) error {
	bts, err := w.Serialize()
	if err != nil {
		return err
	}
	h.broadcast <- bts
	return nil
}

func (h *Hub) NewSeekRequest(cs *pb.SeekRequest) error {
	evt := entity.WrapEvent(cs, pb.MessageType_SEEK_REQUEST, "")
	return h.broadcastEvent(evt)
}

func (h *Hub) DeleteSeek(id string) error {
	// essentially just send the same game accepted event back.
	evt := entity.WrapEvent(&pb.GameAcceptedEvent{RequestId: id}, pb.MessageType_GAME_ACCEPTED_EVENT, "")
	return h.broadcastEvent(evt)
}

func (h *Hub) sendToClient(username string, evt *entity.EventWrapper) error {
	clients := h.clientsByUsername[username]
	if clients == nil {
		return errors.New("client not in list")
	}
	bts, err := evt.Serialize()
	if err != nil {
		return err
	}
	for _, client := range clients {
		client.send <- bts
	}
	return nil
}

func (h *Hub) sendRealmData(ctx context.Context, realm Realm, username string) {
	// Send the client relevant data depending on its realm(s). This
	// should be called when the client joins a realm.
	clients := h.clientsByUsername[username]
	for _, client := range clients {
		if realm == Realm(LobbyRealm) {
			msg, err := h.openSeeks(ctx)
			if err != nil {
				log.Err(err).Msg("getting open seeks")
				return
			}
			client.send <- msg
		} else {
			// assume it's a game
			// c.send <- c.hub.gameRefresher(realm)
			msg, err := h.gameRefresher(ctx, realm, username)
			if err != nil {
				log.Err(err).Msg("getting game info")
				return
			}
			client.send <- msg
		}
	}

}

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
