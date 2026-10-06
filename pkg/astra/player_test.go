package astra

import (
	"context"
	"testing"
	"time"

	"github.com/carbynestack/ephemeral/pkg/discovery/fsm"

	pb "github.com/carbynestack/ephemeral/pkg/discovery/transport/proto"
	"github.com/carbynestack/ephemeral/pkg/types"

	mb "github.com/vardius/message-bus"
)

type idleEngine struct{}

func (idleEngine) Execute(*pb.Event) error {
	return nil
}

func TestPlayerInitPublishesPlayerReady(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bus := mb.New(10)
	events := make(chan *pb.Event, 1)

	err := bus.Subscribe(types.DiscoveryTopic, func(event interface{}) {
		fsmEvent := event.(interface {
			GetMeta() interface{}
		})
		_ = fsmEvent
	})
	if err != nil {
		t.Fatalf("subscribe to Discovery topic: %v", err)
	}

	// Publisher sends an FSM event through the message bus. Subscribe again
	// with the concrete type used by the Discovery publisher.
	bus.Close(types.DiscoveryTopic)

	err = bus.Subscribe(types.DiscoveryTopic, func(event interface{}) {
		published := event.(*fsm.Event)
		events <- published.Meta.TransportMsg
	})
	if err != nil {
		t.Fatalf("subscribe to Discovery topic: %v", err)
	}

	player, err := NewPlayer(
		ctx,
		bus,
		time.Second,
		time.Second,
		idleEngine{},
		&PlayerParams{
			GameID:    "33a6b2cd-c28f-40c4-a1ed-d87ad3a94dbd",
			Pod:       "astra-player-0",
			PlayerID:  Player0,
			Players:   OnlinePlayerCount,
			Frontend:  "player0.example.org",
			TopicName: NewTopicFromPlayerID(Player0),
		},
		make(chan error, 1),
		nil,
	)
	if err != nil {
		t.Fatalf("NewPlayer() returned error: %v", err)
	}
	defer player.Stop()

	player.Init()

	select {
	case event := <-events:
		if event.Name != types.PlayerReady {
			t.Fatalf(
				"Discovery event name = %q; want %q",
				event.Name,
				types.PlayerReady,
			)
		}

		if len(event.Players) != 1 {
			t.Fatalf(
				"player count = %d; want 1",
				len(event.Players),
			)
		}

		discoveryPlayer := event.Players[0]
		if discoveryPlayer.Id != DiscoveryPlayerIDOffset+Player0 {
			t.Fatalf(
				"Discovery player ID = %d; want %d",
				discoveryPlayer.Id,
				DiscoveryPlayerIDOffset+Player0,
			)
		}

		if discoveryPlayer.Players != OnlinePlayerCount {
			t.Fatalf(
				"online player count = %d; want %d",
				discoveryPlayer.Players,
				OnlinePlayerCount,
			)
		}

	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ASTRA PlayerReady event")
	}
}
