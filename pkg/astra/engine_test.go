package astra

import (
	"context"
	"reflect"
	"testing"

	"github.com/carbynestack/ephemeral/pkg/types"
)

type recordingExecutor struct {
	command   []string
	directory string
	stdout    []byte
	stderr    []byte
	err       error
}

func (e *recordingExecutor) CallCMD(
	_ context.Context,
	command []string,
	directory string,
) ([]byte, []byte, error) {
	e.command = command
	e.directory = directory

	return e.stdout, e.stderr, e.err
}

type recordingRelay struct {
	routes    []PeerRoute
	runCalls  int
	stopCalls int
	err       error
}

func (r *recordingRelay) Run(
	_ context.Context,
	routes []PeerRoute,
) error {
	r.runCalls++
	r.routes = routes

	return r.err
}

func (r *recordingRelay) Stop() {
	r.stopCalls++
}

func TestEngineActivatesPlayerOneThroughRelay(t *testing.T) {
	executor := &recordingExecutor{
		stdout: []byte("Result: 126"),
	}
	relay := &recordingRelay{}

	config := &types.ASTRAEngineTypedConfig{
		PlayerID:    Player1,
		PlayerCount: OnlinePlayerCount,
	}

	engine, err := NewEngine(
		nil,
		executor,
		config,
		OnlineProcessConfig{
			BinaryPath:       "/opt/mp-spdz/astra-party.x",
			WorkingDirectory: "/workspace",
			ProgramName:      "astra_mul",
			InputFilePrefix:  "Player-Data/astra-mul-input",
		},
		relay,
	)
	if err != nil {
		t.Fatalf("NewEngine() returned error: %v", err)
	}

	routes := []PeerRoute{
		{
			RemotePlayerID: Player0,
			RemoteHost:     "player0.example.org",
			RemotePort:     30000,
			LocalPort:      Player0Port,
		},
	}

	output, err := engine.Activate(&ASTRACtxConfig{
		Activation: &ASTRAActivation{
			GameID: "33a6b2cd-c28f-40c4-a1ed-d87ad3a94dbd",
		},
		Config:  config,
		Routes:  routes,
		Context: context.Background(),
	})
	if err != nil {
		t.Fatalf("Activate() returned error: %v", err)
	}

	if string(output) != "Result: 126" {
		t.Fatalf("output = %q; want %q", string(output), "Result: 126")
	}

	if relay.runCalls != 1 {
		t.Fatalf("relay Run() calls = %d; want 1", relay.runCalls)
	}

	if relay.stopCalls != 1 {
		t.Fatalf("relay Stop() calls = %d; want 1", relay.stopCalls)
	}

	if !reflect.DeepEqual(relay.routes, routes) {
		t.Fatalf("relay routes = %#v; want %#v", relay.routes, routes)
	}

	wantCommand := []string{
		"/opt/mp-spdz/astra-party.x 1 astra_mul " +
			"-IF Player-Data/astra-mul-input -pn 5000 -h localhost",
	}

	if !reflect.DeepEqual(executor.command, wantCommand) {
		t.Fatalf(
			"command = %#v; want %#v",
			executor.command,
			wantCommand,
		)
	}

	if executor.directory != "/workspace" {
		t.Fatalf(
			"directory = %q; want %q",
			executor.directory,
			"/workspace",
		)
	}
}

func TestEngineActivatesPlayerZeroWithoutRelay(t *testing.T) {
	executor := &recordingExecutor{
		stdout: []byte("Result: 126"),
	}

	config := &types.ASTRAEngineTypedConfig{
		PlayerID:    Player0,
		PlayerCount: OnlinePlayerCount,
	}

	engine, err := NewEngine(
		nil,
		executor,
		config,
		OnlineProcessConfig{
			BinaryPath:       "/opt/mp-spdz/astra-party.x",
			WorkingDirectory: "/workspace",
			ProgramName:      "astra_mul",
			InputFilePrefix:  "Player-Data/astra-mul-input",
		},
		nil,
	)
	if err != nil {
		t.Fatalf("NewEngine() returned error: %v", err)
	}

	_, err = engine.Activate(&ASTRACtxConfig{
		Activation: &ASTRAActivation{
			GameID: "33a6b2cd-c28f-40c4-a1ed-d87ad3a94dbd",
		},
		Config:  config,
		Context: context.Background(),
	})
	if err != nil {
		t.Fatalf("Activate() returned error: %v", err)
	}

	if executor.directory != "/workspace" {
		t.Fatalf(
			"directory = %q; want %q",
			executor.directory,
			"/workspace",
		)
	}
}
