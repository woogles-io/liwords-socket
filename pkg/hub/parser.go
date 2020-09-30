package sockets

import (
	"context"
	"fmt"
	"time"

	pb "github.com/domino14/liwords/rpc/api/proto/realtime"
	"github.com/rs/zerolog/log"
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

	return topic + "." + first + "." + second + "." + c.connID
}

func (h *Hub) parseAndExecuteMessage(ctx context.Context, msg []byte, c *Client) error {
	// All socket messages are encoded entity.Events.
	// (or they better be)

	// The type byte is [2] ([0] and [1] are length of the packet)
	switch pb.MessageType(msg[2]) {

	case pb.MessageType_SEEK_REQUEST:
		log.Debug().Msg("publishing seek request to NATS")
		err := h.pubsub.natsconn.Publish(extendTopic(c, "ipc.pb.seekRequest"), msg[3:])
		if err != nil {
			return err
		}

	case pb.MessageType_MATCH_REQUEST:
		log.Debug().Msg("publishing match request to NATS")
		err := h.pubsub.natsconn.Publish(extendTopic(c, "ipc.pb.matchRequest"), msg[3:])
		if err != nil {
			return err
		}

	case pb.MessageType_DECLINE_MATCH_REQUEST:
		log.Debug().Msg("publishing decline match request to NATS")
		err := h.pubsub.natsconn.Publish(extendTopic(c, "ipc.pb.declineMatchRequest"), msg[3:])
		if err != nil {
			return err
		}

	case pb.MessageType_SOUGHT_GAME_PROCESS_EVENT:
		log.Debug().Msg("publishing sought game process to NATS")

		err := h.pubsub.natsconn.Publish(extendTopic(c, "ipc.pb.soughtGameProcess"), msg[3:])
		if err != nil {
			return err
		}

	case pb.MessageType_CLIENT_GAMEPLAY_EVENT:
		err := h.pubsub.natsconn.Publish(extendTopic(c, "ipc.pb.gameplayEvent"), msg[3:])
		if err != nil {
			return err
		}

	case pb.MessageType_CHAT_MESSAGE:
		err := h.pubsub.natsconn.Publish(extendTopic(c, "ipc.pb.chat"), msg[3:])
		if err != nil {
			return err
		}

	case pb.MessageType_TIMED_OUT:
		err := h.pubsub.natsconn.Publish(extendTopic(c, "ipc.pb.timedOut"), msg[3:])
		if err != nil {
			return err
		}

	default:
		return fmt.Errorf("message type %v not yet handled", msg[2])
	}

	return nil
}
