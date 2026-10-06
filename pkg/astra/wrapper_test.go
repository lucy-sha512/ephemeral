package astra

import (
	"testing"

	pb "github.com/carbynestack/ephemeral/pkg/discovery/transport/proto"
)

func TestResolveRoutes(t *testing.T) {
	event := &pb.Event{
		Players: []*pb.Player{
			{
				Id:   DiscoveryPlayerIDOffset + Player1,
				Ip:   "player1.example.org",
				Port: 30001,
			},
			{
				Id:   DiscoveryPlayerIDOffset + Player0,
				Ip:   "player0.example.org",
				Port: 30000,
			},
		},
	}

	t.Run("player zero owns the listener and needs no relay", func(t *testing.T) {
		routes, err := ResolveRoutes(Player0, event)
		if err != nil {
			t.Fatalf("ResolveRoutes() returned error: %v", err)
		}
		if len(routes) != 0 {
			t.Fatalf("expected no routes for Player 0; got %#v", routes)
		}
	})

	t.Run("player one receives a local relay to player zero", func(t *testing.T) {
		routes, err := ResolveRoutes(Player1, event)
		if err != nil {
			t.Fatalf("ResolveRoutes() returned error: %v", err)
		}

		if len(routes) != 1 {
			t.Fatalf("expected one route; got %#v", routes)
		}

		route := routes[0]
		if route.RemotePlayerID != Player0 {
			t.Fatalf("remote player ID = %d; want %d", route.RemotePlayerID, Player0)
		}
		if route.RemoteHost != "player0.example.org" {
			t.Fatalf(
				"remote host = %q; want %q",
				route.RemoteHost,
				"player0.example.org",
			)
		}
		if route.RemotePort != 30000 {
			t.Fatalf("remote port = %d; want 30000", route.RemotePort)
		}
		if route.LocalPort != Player0Port {
			t.Fatalf(
				"local port = %d; want %d",
				route.LocalPort,
				Player0Port,
			)
		}
	})

	t.Run("rejects a missing participant", func(t *testing.T) {
		_, err := ResolveRoutes(
			Player1,
			&pb.Event{
				Players: []*pb.Player{
					{
						Id:   DiscoveryPlayerIDOffset + Player0,
						Ip:   "player0.example.org",
						Port: 30000,
					},
				},
			},
		)
		if err == nil {
			t.Fatal("expected an error for a missing Player 1")
		}
	})
}
