// Copyright (c) 2026
//
// SPDX-License-Identifier: Apache-2.0

// Package astra contains the Carbyne Stack adapter for the MP-SPDZ
// implementation of the ASTRA protocol.
package astra

import (
	"context"

	pb "github.com/carbynestack/ephemeral/pkg/discovery/transport/proto"
	
)

const (
	// OnlinePlayerCount is fixed by the current MP-SPDZ ASTRA online machine.
	OnlinePlayerCount int32 = 2

	// PreprocessingPlayerCount is fixed by astra-prep-party.x.
	PreprocessingPlayerCount int32 = 3

	// Player0 accepts the native ASTRA online connection.
	Player0 int32 = 0

	// Player1 connects to Player0 through its local relay.
	Player1 int32 = 1

	// DiscoveryPlayerIDOffset is the current Discovery wire-format offset.
	DiscoveryPlayerIDOffset int32 = 100

	// NetworkBasePort matches the port basis used by Discovery and the
	// Network Controller for an MPC player.
	NetworkBasePort int32 = 5000

	// Player0Port is the native ASTRA listener port. In Player 1's pod, the
	// ASTRA proxy binds the same local port and relays it to Player 0.
	Player0Port int32 = NetworkBasePort + Player0
)

// ASTRAActivation is the HTTP computation request accepted by the ASTRA
// execution service. Its JSON contract initially matches the existing
// Ephemeral request format; ASTRA input interpretation is implemented later.
type ASTRAActivation struct {
	AmphoraParams []string          `json:"amphoraParams"`
	SecretParams  []string          `json:"secretParams"`
	GameID        string            `json:"gameID"`
	Code          string            `json:"code"`
	Output        ASTRAOutputConfig `json:"output"`
}

// ASTRAOutputConfig defines the requested treatment of ASTRA output.
type ASTRAOutputConfig struct {
	Type string `json:"type"`
}

// PeerRoute describes one ASTRA local relay.
//
// RemoteHost and RemotePort come from the Player 0 entry in the Discovery
// Playing event. LocalPort is bound inside Player 1's Ephemeral pod.
type PeerRoute struct {
	RemotePlayerID int32
	RemoteHost     string
	RemotePort     int32
	LocalPort      int32
}

// ASTRACtxConfig contains per-request execution state. Each HTTP activation
// owns one instance; it must never be shared across concurrent games.
type ASTRACtxConfig struct {
	AuthorizedUser string
	Activation     *ASTRAActivation
	Config         *ASTRAEngineTypedConfig
	Routes         []PeerRoute
	ErrCh          chan error
	Context        context.Context
}

// ASTRAMPCEngine is the ASTRA-specific Discovery callback contract.
// ASTRAWrapper implements it and starts the ASTRA engine after Discovery
// delivers the complete participant set.
type ASTRAMPCEngine interface {
	Execute(*pb.Event) error
}




