// Package sockets handles the socket communication.
// This particular file sanitizes messages. Messages going via sockets
// must be "sanitized" to avoid giving information to opponents about player racks.
package sockets

import (
	"strconv"

	macondopb "github.com/domino14/macondo/gen/api/proto/macondo"
	"google.golang.org/protobuf/proto"
)

func sanitizeHistory(his *macondopb.GameHistory, requester string) *macondopb.GameHistory {

	if his.PlayState == macondopb.PlayState_GAME_OVER {
		// If the game is over, then everyone can get the history.
		return his
	}
	players := his.Players
	found := false
	pidx := -1
	for idx, p := range players {
		if p.Nickname == requester {
			found = true
			pidx = idx
			break
		}
	}
	if !found {
		// no need to sanitize history, the person requesting it is an observer.
		return his
	}

	cloned := proto.Clone(his).(*macondopb.GameHistory)
	for _, t := range cloned.Turns {
		for _, e := range t.Events {
			if e.GetNickname() == requester {
				// It's ok to send the requester info about their own rack.
				break
			}
			// Otherwise, edit the event.
			e.Rack = ""
			if len(e.Exchanged) > 0 {
				e.Exchanged = strconv.Itoa(len(e.Exchanged))
			}
		}
	}
	if len(cloned.LastKnownRacks) > 1 { // this should always be the case
		// Clear the last known rack for this user.
		cloned.LastKnownRacks[1-pidx] = ""
	}
	return cloned
}
