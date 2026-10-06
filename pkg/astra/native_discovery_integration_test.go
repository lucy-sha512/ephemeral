package astra

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	discovery "github.com/carbynestack/ephemeral/pkg/discovery"
	"github.com/carbynestack/ephemeral/pkg/discovery/fsm"
	pb "github.com/carbynestack/ephemeral/pkg/discovery/transport/proto"
	transportserver "github.com/carbynestack/ephemeral/pkg/discovery/transport/server"
	"github.com/carbynestack/ephemeral/pkg/types"
	"github.com/carbynestack/ephemeral/pkg/utils"
	"github.com/google/uuid"
	mb "github.com/vardius/message-bus"
	"go.uber.org/zap"
)

// localASTRANetworker publishes the actual localhost ports. Kubernetes routing
// is replaced by direct local sockets; Discovery and its gRPC transport are real.
type localASTRANetworker struct{ ports map[int32]int32 }

func (n *localASTRANetworker) CreateNetwork(player *pb.Player) (int32, error) {
	port, ok := n.ports[player.Id]
	if !ok {
		return 0, fmt.Errorf("no local port for Discovery player %d", player.Id)
	}
	return port, nil
}

func TestDiscoveryRunsNativeASTRAPlayers(t *testing.T) {
	root := os.Getenv("ASTRA_MP_SPDZ_ROOT")
	if root == "" {
		t.Skip("set ASTRA_MP_SPDZ_ROOT to run the native ASTRA fixture")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "astra-party.x")
	info, err := os.Stat(binary)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0111 == 0 {
		t.Fatalf("ASTRA binary is not executable: %s", binary)
	}

	loggerBase, err := zap.NewDevelopment()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = loggerBase.Sync() }()
	logger := loggerBase.Sugar()
	ports := reserveNativeASTRAPorts(t)
	workspaces := [2]string{}
	for id := 0; id < 2; id++ {
		workspaces[id] = t.TempDir()
		for _, directory := range []string{"Programs", "Player-Data"} {
			command := exec.Command("cp", "-a", "--", filepath.Join(root, directory), filepath.Join(workspaces[id], directory))
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("stage player %d %s: %v\n%s", id, directory, err, output)
			}
		}
		required := []string{
			"Programs/Schedules/astra_mul.sch",
			"Programs/Bytecode/astra_mul-0.bc",
			fmt.Sprintf("Player-Data/astra-mul-input-P%d-0", id),
			fmt.Sprintf("Player-Data/3-astra-R-64/Protocol-P%d-T0", id+1),
		}
		for _, file := range required {
			if _, err := os.Stat(filepath.Join(workspaces[id], file)); err != nil {
				t.Fatal(err)
			}
		}
	}
	discoveryPort := reserveLocalPort(t)
	bus := mb.New(10000)
	errorsCh := make(chan error, 16)
	transportErrors := make(chan error, 4)
	gameID := uuid.New().String()
	gameSuccess := make(chan struct{}, 1)
	gameDone := make(chan struct{}, 1)
	// These subscriptions observe collective completion rather than treating
	// native process exit alone as proof of Discovery lifecycle completion.
	if err := bus.Subscribe(types.DiscoveryTopic, func(value interface{}) {
		event := value.(*fsm.Event)
		if len(event.Meta.SrcTopics) == 0 || event.Meta.SrcTopics[0] != gameID {
			return
		}
		switch event.Name {
		case types.GameSuccess:
			gameSuccess <- struct{}{}
		case types.GameError:
			errorsCh <- fmt.Errorf("Discovery reported GameError for %s", gameID)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := bus.Subscribe(types.ServiceEventsTopic, func(value interface{}) {
		event := value.(*fsm.Event)
		if event.Name == types.GameDone && len(event.Meta.SrcTopics) > 0 && event.Meta.SrcTopics[0] == gameID {
			gameDone <- struct{}{}
		}
	}); err != nil {
		t.Fatal(err)
	}
	transport := transportserver.NewTransportServer(&transportserver.TransportConfig{
		In: make(chan *pb.Event, 16), Out: make(chan *pb.Event, 16),
		ErrCh: transportErrors, Port: discoveryPort, Logger: logger,
	})
	networker := &localASTRANetworker{ports: map[int32]int32{
		DiscoveryPlayerIDOffset + Player0: ports[0],
		DiscoveryPlayerIDOffset + Player1: ports[1],
	}}
	service := discovery.NewServiceNG(bus, discovery.NewPublisher(bus),
		10*time.Second, 30*time.Second, transport, networker,
		"127.0.0.1", logger, types.ModeMaster, &discovery.FakeDClient{}, int(OnlinePlayerCount))
	defer service.Stop()
	go func() {
		if err := service.Start(); err != nil {
			errorsCh <- err
		}
	}()
	if err := service.WaitUntilReady(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	responseChannels := [2]chan []byte{make(chan []byte, 1), make(chan []byte, 1)}
	players := [2]*PlayerWithIO{}
	playerDone := [2]chan struct{}{make(chan struct{}, 1), make(chan struct{}, 1)}
	for id := 0; id < 2; id++ {
		config := testASTRAConfig(int32(id), discoveryPort)
		config.RuntimePort = ports[id]
		config.StateTimeout = 10 * time.Second
		config.ComputationTimeout = 30 * time.Second
		activation := &ASTRACtxConfig{
			Activation: &ASTRAActivation{GameID: gameID},
			Config:     config, ErrCh: errorsCh, Context: ctx,
		}
		engine, err := NewEngine(logger, utils.NewCommander(), config, OnlineProcessConfig{
			BinaryPath: binary, WorkingDirectory: workspaces[id],
			ProgramName: "astra_mul", InputFilePrefix: "Player-Data/astra-mul-input",
		}, NewProxy(logger, nil))
		if err != nil {
			t.Fatal(err)
		}
		wrapper := NewWrapper(activation, responseChannels[id], errorsCh, logger, engine.Activate)
		players[id], err = NewPlayerWithIO(activation, fmt.Sprintf("astra-local-%d", id), wrapper, logger)
		if err != nil {
			t.Fatal(err)
		}
		doneChannel := playerDone[id]
		if err := players[id].Player.Bus().Subscribe(types.ServiceEventsTopic, func(value interface{}) {
			event := value.(*fsm.Event)
			if event.Name == types.PlayerDone {
				doneChannel <- struct{}{}
			}
		}); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("Discovery=%s; Player 0 listener=%d; Player 1 relay=%d", discoveryPort, ports[0], ports[1])
	for _, player := range players {
		player.Start()
	}
	outputs := [2][]byte{}
	for completed := 0; completed < 2; completed++ {
		select {
		case output := <-responseChannels[0]:
			outputs[0] = output
			responseChannels[0] = nil
			t.Logf("Player 0 native output:\n%s", output)
		case output := <-responseChannels[1]:
			outputs[1] = output
			responseChannels[1] = nil
			t.Logf("Player 1 native output:\n%s", output)
		case err := <-errorsCh:
			t.Fatalf("ASTRA execution failed: %v", err)
		case err := <-transportErrors:
			t.Fatalf("Discovery transport failed: %v", err)
		case <-ctx.Done():
			t.Fatal("timed out waiting for native ASTRA output")
		}
	}
	actualResult := ""
	for _, line := range strings.Split(string(outputs[0]), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Result:") {
			actualResult = line
			break
		}
	}
	// The expected value is only an assertion. Displayed output is read from
	// the real astra-party.x response, never manufactured by the callback.
	if actualResult != "Result: 126" {
		t.Fatalf("native public result = %q; want Result: 126", actualResult)
	}
	for _, event := range []struct {
		name    string
		channel <-chan struct{}
	}{
		{"GameSuccess", gameSuccess}, {"GameDone", gameDone},
		{"Player 0 done", playerDone[0]},
		{"Player 1 done", playerDone[1]},
	} {
		select {
		case <-event.channel:
			t.Logf("Discovery confirmed %s", event.name)
		case err := <-errorsCh:
			t.Fatalf("Discovery completion failed: %v", err)
		case <-ctx.Done():
			t.Fatalf("timed out waiting for Discovery %s", event.name)
		}
	}
	t.Logf("Public MPC result: %s", actualResult)
}

func reserveNativeASTRAPorts(t *testing.T) [2]int32 {
	t.Helper()
	ports := [2]int32{}
	for id := range ports {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		ports[id] = int32(listener.Addr().(*net.TCPAddr).Port)
	}
	return ports
}
