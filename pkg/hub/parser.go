package sockets

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/dgrijalva/jwt-go"
	"github.com/domino14/liwords/pkg/entity"
	pb "github.com/domino14/liwords/rpc/api/proto"
	"github.com/rs/zerolog/log"
	"google.golang.org/protobuf/proto"
)

const (
	ipcTimeout = 2 * time.Second
)

func extendTopic(c *Client, topic string) string {
	// The publish topic should encode the user ID and the login status.
	// This is so we don't have to wastefully unmarshal and remarshal here,
	// and also because NATS plays nicely with hierarchical subject names.
	first := ""
	second := ""
	if !c.authenticated {
		first = "anon"
	} else {
		first = "auth"
	}
	second = c.userID

	return topic + "." + first + "." + second
}

func (h *Hub) parseAndExecuteMessage(ctx context.Context, msg []byte, c *Client) error {
	// All socket messages are encoded entity.Events.
	// (or they better be)

	switch pb.MessageType(msg[0]) {
	case pb.MessageType_TOKEN_SOCKET_LOGIN:
		ew, err := entity.EventFromByteArray(msg)
		if err != nil {
			return err
		}
		evt, ok := ew.Event.(*pb.TokenSocketLogin)
		if !ok {
			return errors.New("unexpected socket login typing error")
		}
		return h.socketLogin(c, evt)

	case pb.MessageType_SEEK_REQUEST:
		log.Debug().Msg("publishing seek request to NATS")
		err := h.pubsub.natsconn.Publish(extendTopic(c, "ipc.pb.seekRequest"), msg[1:])
		if err != nil {
			return err
		}

	case pb.MessageType_GAME_ACCEPTED_EVENT:
		err := h.pubsub.natsconn.Publish(extendTopic(c, "ipc.pb.gameAccepted"), msg[1:])
		if err != nil {
			return err
		}

	case pb.MessageType_CLIENT_GAMEPLAY_EVENT:
		err := h.pubsub.natsconn.Publish(extendTopic(c, "ipc.pb.gameplayEvent"), msg[1:])
		if err != nil {
			return err
		}

		/* client gameplay event
		evt, ok := ew.Event.(*pb.ClientGameplayEvent)
		if !ok {
			// This really shouldn't happen
			return errors.New("cge unexpected typing error")
		}
		err := gameplay.PlayMove(ctx, h.gameStore, sender, evt)
		if err != nil {
			return err
		}
		*/

		// seek request accepted:
	// 	sg, err := h.soughtGameStore.Get(ctx, evt.RequestId)
	// 	if err != nil {
	// 		return err
	// 	}
	// 	requester := sg.SeekRequest.User.Username
	// 	if requester == sender {
	// 		log.Info().Str("sender", sender).Msg("canceling seek")
	// 		err := gameplay.CancelSoughtGame(ctx, h.soughtGameStore, evt.RequestId)
	// 		if err != nil {
	// 			return err
	// 		}
	// 		// broadcast a seek deletion.
	// 		err = h.DeleteSeek(evt.RequestId)
	// 		if err != nil {
	// 			return err
	// 		}
	// 		return err
	// 	}

	// 	players := []*macondopb.PlayerInfo{
	// 		{Nickname: sender, RealName: sender},
	// 		{Nickname: requester, RealName: requester},
	// 	}
	// 	log.Debug().Interface("seekreq", sg.SeekRequest).Msg("seek-request-accepted")

	// 	g, err := gameplay.InstantiateNewGame(ctx, h.gameStore, h.config,
	// 		players, sg.SeekRequest.GameRequest)
	// 	if err != nil {
	// 		return err
	// 	}
	// 	// Broadcast a seek delete event, and send both parties a game redirect.
	// 	h.soughtGameStore.Delete(ctx, evt.RequestId)
	// 	err = h.DeleteSeek(evt.RequestId)
	// 	if err != nil {
	// 		return err
	// 	}
	// 	// This event will result in a redirect.
	// 	ngevt := entity.WrapEvent(&pb.NewGameEvent{
	// 		GameId: g.GameID(),
	// 	}, pb.MessageType_NEW_GAME_EVENT, "")

	// 	h.sendToClient(sender, ngevt)
	// 	h.sendToClient(requester, ngevt)
	// 	// Create a new realm to put these players in.
	// 	realm := h.addNewRealm(g.GameID())
	// 	h.addToRealm(realm, sender)
	// 	h.addToRealm(realm, requester)
	// 	log.Info().Str("newgameid", g.History().Uid).
	// 		Str("sender", sender).
	// 		Str("requester", requester).
	// 		Str("onturn", g.NickOnTurn()).Msg("game-accepted")

	// 	// Now, reset the timer and register the event change hook.
	// 	err = gameplay.StartGame(ctx, h.gameStore, h.eventChan, g.GameID())
	// 	if err != nil {
	// 		return err
	// 	}

	// case pb.MessageType_CLIENT_GAMEPLAY_EVENT:
	// 	evt, ok := ew.Event.(*pb.ClientGameplayEvent)
	// 	if !ok {
	// 		// This really shouldn't happen
	// 		return errors.New("cge unexpected typing error")
	// 	}
	// 	err := gameplay.PlayMove(ctx, h.gameStore, sender, evt)
	// 	if err != nil {
	// 		return err
	// 	}

	case pb.MessageType_JOIN_PATH:
		ew, err := entity.EventFromByteArray(msg)
		if err != nil {
			return err
		}
		evt, ok := ew.Event.(*pb.JoinPath)
		if !ok {
			// This really shouldn't happen
			return errors.New("rr unexpected typing error")
		}
		return registerRealm(c, evt, h)

	case pb.MessageType_UNJOIN_REALM:
		h.removeFromRealm(c)

	// case pb.MessageType_TIMED_OUT:
	// 	evt, ok := ew.Event.(*pb.TimedOut)
	// 	if !ok {
	// 		return errors.New("to unexpected typing error")
	// 	}
	// 	// Verify the timeout.
	// 	err := gameplay.TimedOut(ctx, h.gameStore, sender, evt.Username, evt.GameId)
	// 	if err != nil {
	// 		return err
	// 	}

	default:
		return fmt.Errorf("message type %v not yet handled", msg[0])

	}

	return nil
}

func (h *Hub) socketLogin(c *Client, evt *pb.TokenSocketLogin) error {
	tokenString := evt.Token

	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		// Don't forget to validate the alg is what you expect:
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("Unexpected signing method: %v", token.Header["alg"])
		}

		// hmacSampleSecret is a []byte containing your secret, e.g. []byte("my_secret_key")
		return []byte(os.Getenv("SECRET_KEY")), nil
	})
	if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
		// XXX: CHEKC THAT IT HASN'T EXPIRED. I think Valid ^^ might be enough but we need a test.

		// fmt.Println(claims["foo"], claims["nbf"])
		c.authenticated = true
		c.username = claims["unn"].(string)
		h.realmMutex.Lock()
		// Delete the old user ID from the map
		delete(h.clientsByUserID, c.userID)
		c.userID = claims["uid"].(string)
		byUser := h.clientsByUserID[c.userID]
		if byUser == nil {
			h.clientsByUserID[c.userID] = make(map[*Client]bool)
		}
		// Add the new user ID to the map.
		h.clientsByUserID[c.userID][c] = true
		h.realmMutex.Unlock()
		log.Debug().Str("username", c.username).Str("userID", c.userID).Msg("authenticated socket connection")
		if c.realm != NullRealm {
			log.Debug().Str("realm", string(c.realm)).Str("username", c.username).
				Msg("client already in realm, sending init info")
			err = h.sendRealmInitInfo(string(c.realm), c.userID)
		}
	}
	if err != nil {
		log.Err(err).Msg("socket-login-failure")
	}
	return err
}

func registerRealm(c *Client, evt *pb.JoinPath, h *Hub) error {
	// There are a variety of possible realms that a person joining a game
	// can be in. We should not trust the user to send the right realm
	// (for example they can send a TV mode realm if they're a player
	// in the game or vice versa). The backend should determine the right realm
	// and assign it accordingly.
	log.Debug().Str("path", evt.Path).Msg("register-realm-path")
	var realm string
	if evt.Path == "/" {
		// This is the lobby; no need to request a realm.
		h.addToRealm(LobbyRealm, c)
		realm = string(LobbyRealm)
	} else {
		// First, create a request and send to the IPC api:
		rrr := &pb.RegisterRealmRequest{}
		rrr.Realm = evt.Path
		rrr.UserId = c.userID
		data, err := proto.Marshal(rrr)
		if err != nil {
			return err
		}
		resp, err := h.pubsub.natsconn.Request("ipc.request.registerRealm", data, ipcTimeout)
		if err != nil {
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
		if Realm(realm) != NullRealm {
			// Only add to the realm that the API says to add to.
			h.addToRealm(Realm(realm), c)
		}
	}
	// Meow, depending on the realm, request that the API publish
	// initial information pertaining to this realm. For example,
	// lobby visitors will want to see a list of sought games,
	// or newcomers to a game realm will want to see the history
	// of the game so far.
	return h.sendRealmInitInfo(realm, c.userID)
	// The API will publish the initial realm information to this user's channel.
	// (user.userID - see pubsub.go)
}

func (h *Hub) sendRealmInitInfo(realm string, userID string) error {
	req := &pb.InitRealmInfo{
		Realm:  realm,
		UserId: userID,
	}
	data, err := proto.Marshal(req)
	if err != nil {
		return err
	}
	log.Debug().Interface("initRealmInfo", req).Msg("req-init-realm-info")

	return h.pubsub.natsconn.Publish("ipc.pb.initRealmInfo", data)
}
