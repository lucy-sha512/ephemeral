// Copyright (c) 2026
//
// SPDX-License-Identifier: Apache-2.0

package astra

import (
	"errors"
	"fmt"
	"strings"

	pb "github.com/carbynestack/ephemeral/pkg/discovery/transport/proto"

	"go.uber.org/zap"
)

// ActivationFunc starts one local ASTRA execution after Discovery has supplied
// the complete online participant set.
type ActivationFunc func(*ASTRACtxConfig) ([]byte, error)

// Wrapper bridges a Discovery Playing event to the local ASTRA engine.
type Wrapper struct {
	ctx      *ASTRACtxConfig
	activate ActivationFunc
	respCh   chan []byte
	errCh    chan error
	logger   *zap.SugaredLogger
}

// NewWrapper constructs an ASTRA Discovery-to-engine bridge.
func NewWrapper(
	ctx *ASTRACtxConfig,
	respCh chan []byte,
	errCh chan error,
	logger *zap.SugaredLogger,
	activate ActivationFunc,
) *Wrapper {

	return &Wrapper{
		ctx:      ctx,
		activate: activate,
		respCh:   respCh,
		errCh:    errCh,
		logger:   logger,
	}
}

// Execute handles the Discovery Playing event. Player 0 receives no relay
// route because it owns the ASTRA listener. Player 1 receives one local relay
// route to Player 0's Discovery endpoint.
func (w *Wrapper) Execute(event *pb.Event) error {
	if w.ctx == nil {
		return errors.New("ASTRA execution context is nil")
	}
	if w.ctx.Config == nil {
		return errors.New("ASTRA typed configuration is nil")
	}
	if w.activate == nil {
		return errors.New("ASTRA activation function is nil")
	}

	routes, err := ResolveRoutes(
		w.ctx.Config.PlayerID,
		w.ctx.Config.RuntimePort,
		event,
	)	

	w.ctx.Routes = routes
	w.ctx.ErrCh = w.errCh

	w.logger.Infow(
		"Resolved ASTRA online routes",
		"gameID", w.ctx.Activation.GameID,
		"playerID", w.ctx.Config.PlayerID,
		"routes", routes,
	)

	response, err := w.activate(w.ctx)
	if err != nil {
		w.errCh <- err
		return err
	}

	w.respCh <- response
	return nil
}

// ResolveRoutes converts a Discovery participant list into local ASTRA routes.
func ResolveRoutes(
	localPlayerID int32,
	localRuntimePort int32,
	event *pb.Event,
) ([]PeerRoute, error) {
	if event == nil {
		return nil, errors.New("ASTRA Discovery event is nil")
	}

	if localRuntimePort < 1 || localRuntimePort > 65535 {
	return nil, fmt.Errorf(
		"ASTRA local runtime port must be between 1 and 65535; got %d",
		localRuntimePort,
	)
	}

	LocalPort: localRuntimePort,

	if localPlayerID != Player0 && localPlayerID != Player1 {
		return nil, fmt.Errorf(
			"ASTRA local player ID must be 0 or 1; got %d",
			localPlayerID,
		)
	}

	if len(event.Players) != int(OnlinePlayerCount) {
		return nil, fmt.Errorf(
			"ASTRA online execution requires %d Discovery players; got %d",
			OnlinePlayerCount,
			len(event.Players),
		)
	}

	players := make(map[int32]*pb.Player, OnlinePlayerCount)

	for _, player := range event.Players {
		if player == nil {
			return nil, errors.New("ASTRA Discovery event contains a nil player")
		}

		playerID := player.Id - DiscoveryPlayerIDOffset
		if playerID != Player0 && playerID != Player1 {
			return nil, fmt.Errorf(
				"ASTRA Discovery player ID %d maps to unsupported ID %d",
				player.Id,
				playerID,
			)
		}

		if _, exists := players[playerID]; exists {
			return nil, fmt.Errorf(
				"ASTRA Discovery event contains duplicate player ID %d",
				playerID,
			)
		}

		if strings.TrimSpace(player.Ip) == "" {
			return nil, fmt.Errorf(
				"ASTRA Discovery player %d has an empty frontend address",
				playerID,
			)
		}

		if player.Port < 1 || player.Port > 65535 {
			return nil, fmt.Errorf(
				"ASTRA Discovery player %d has invalid port %d",
				playerID,
				player.Port,
			)
		}

		players[playerID] = player
	}

	if _, exists := players[Player0]; !exists {
		return nil, errors.New("ASTRA Discovery event is missing Player 0")
	}
	if _, exists := players[Player1]; !exists {
		return nil, errors.New("ASTRA Discovery event is missing Player 1")
	}

	if localPlayerID == Player0 {
		return []PeerRoute{}, nil
	}

	player0 := players[Player0]

	return []PeerRoute{
		{
			RemotePlayerID: Player0,
			RemoteHost:     player0.Ip,
			RemotePort:     player0.Port,
			LocalPort:      Player0Port,
		},
	}, nil
}
