package astra

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"testing"
	"time"

	discovery "github.com/carbynestack/ephemeral/pkg/discovery"
	pb "github.com/carbynestack/ephemeral/pkg/discovery/transport/proto"
	transportserver "github.com/carbynestack/ephemeral/pkg/discovery/transport/server"
	"github.com/carbynestack/ephemeral/pkg/types"

	mb "github.com/vardius/message-bus"
	"go.uber.org/zap"
)

type routeObservation struct {
	playerID int32
	routes   []PeerRoute
	output   string
}

func TestDiscoveryCoordinatesASTRAPlayers(t *testing.T) {
	developmentLogger, err := zap.NewDevelopment()
	if err != nil {
		t.Fatalf("create development logger: %v", err)
	}
	defer func() {
		_ = developmentLogger.Sync()
	}()

	logger := developmentLogger.Sugar()
	discoveryPort := reserveLocalPort(t)

	bus := mb.New(10000)
	transportIn := make(chan *pb.Event, 10)
	transportOut := make(chan *pb.Event, 10)
	transportErr := make(chan error, 10)

	transport := transportserver.NewTransportServer(
		&transportserver.TransportConfig{
			In:     transportIn,
			Out:    transportOut,
			ErrCh:  transportErr,
			Port:   discoveryPort,
			Logger: logger,
		},
	)

	networker := &discovery.FakeNetworker{
		FreePorts: []int32{31000, 31001},
	}

	service := discovery.NewServiceNG(
		bus,
		discovery.NewPublisher(bus),
		5*time.Second,
		5*time.Second,
		transport,
		networker,
		"127.0.0.1",
		logger,
		types.ModeMaster,
		&discovery.FakeDClient{},
		int(OnlinePlayerCount),
	)
	defer service.Stop()

	go func() {
		_ = service.Start()
	}()

	if err := service.WaitUntilReady(5 * time.Second); err != nil {
		t.Fatalf("wait for Discovery service: %v", err)
	}

	executionContext, cancel := context.WithCancel(context.Background())
	defer cancel()

	gameID := "33a6b2cd-c28f-40c4-a1ed-d87ad3a94dbd"
	observations := make(chan routeObservation, OnlinePlayerCount)

	for _, playerID := range []int32{Player0, Player1} {
		config := testASTRAConfig(playerID, discoveryPort)

		astraContext := &ASTRACtxConfig{
			Activation: &ASTRAActivation{
				GameID: gameID,
			},
			Config:  config,
			ErrCh:   make(chan error, 4),
			Context: executionContext,
		}

		wrapper := NewWrapper(
			astraContext,
			make(chan []byte, 1),
			make(chan error, 1),
			logger,
			func(ctx *ASTRACtxConfig) ([]byte, error) {
				routes := append([]PeerRoute(nil), ctx.Routes...)

				t.Logf(
					"ASTRA Player %d activated: routes=%+v; output=%q",
					ctx.Config.PlayerID,
					routes,
					"ASTRA Discovery coordination complete",
				)

				observations <- routeObservation{
					playerID: ctx.Config.PlayerID,
					routes:   routes,
				}

				return []byte("ASTRA Discovery coordination complete"), nil
			},
		)

		player, err := NewPlayerWithIO(
			astraContext,
			fmt.Sprintf("astra-player-%d", playerID),
			wrapper,
			logger,
		)
		if err != nil {
			t.Fatalf(
				"create ASTRA player %d: %v",
				playerID,
				err,
			)
		}

		player.Start()
	}

	resolvedRoutes := make(map[int32][]PeerRoute)

	for received := 0; received < int(OnlinePlayerCount); received++ {
		select {
		case observation := <-observations:
			resolvedRoutes[observation.playerID] = observation.routes

		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for ASTRA Discovery coordination")
		}
	}

	player0Routes, exists := resolvedRoutes[Player0]
	if !exists {
		t.Fatal("Player 0 did not receive a Discovery execution event")
	}
	if len(player0Routes) != 0 {
		t.Fatalf(
			"Player 0 routes = %#v; want no outbound routes",
			player0Routes,
		)
	}

	player1Routes, exists := resolvedRoutes[Player1]
	if !exists {
		t.Fatal("Player 1 did not receive a Discovery execution event")
	}
	if len(player1Routes) != 1 {
		t.Fatalf(
			"Player 1 routes = %#v; want one route",
			player1Routes,
		)
	}

	route := player1Routes[0]

	if route.RemotePlayerID != Player0 {
		t.Fatalf(
			"Player 1 remote player ID = %d; want %d",
			route.RemotePlayerID,
			Player0,
		)
	}
	if route.RemoteHost != "127.0.0.1" {
		t.Fatalf(
			"Player 1 remote host = %q; want %q",
			route.RemoteHost,
			"127.0.0.1",
		)
	}
	if route.RemotePort != 31000 {
		t.Fatalf(
			"Player 1 remote port = %d; want 31000",
			route.RemotePort,
		)
	}
	if route.LocalPort != Player0Port {
		t.Fatalf(
			"Player 1 local relay port = %d; want %d",
			route.LocalPort,
			Player0Port,
		)
	}
}

func testASTRAConfig(
	playerID int32,
	discoveryPort string,
) *ASTRAEngineTypedConfig {
	return &ASTRAEngineTypedConfig{
		RingBits:           64,
		PlayerID:           playerID,
		PlayerCount:        OnlinePlayerCount,
		RuntimePort:        Player0Port,
		FrontendURL:        "127.0.0.1",
		StateTimeout:       5 * time.Second,
		ComputationTimeout: 5 * time.Second,
		DiscoveryConfig: types.DiscoveryClientTypedConfig{
			Host:           "127.0.0.1",
			Port:           discoveryPort,
			ConnectTimeout: 5 * time.Second,
		},
	}
}

func reserveLocalPort(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve local Discovery port: %v", err)
	}
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port
	return strconv.Itoa(port)
}
