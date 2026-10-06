// Copyright (c) 2026
//
// SPDX-License-Identifier: Apache-2.0

package astra

import (
	"context"
	"errors"
	"fmt"

	"github.com/carbynestack/ephemeral/pkg/discovery/fsm"
	c "github.com/carbynestack/ephemeral/pkg/discovery/transport/client"
	pb "github.com/carbynestack/ephemeral/pkg/discovery/transport/proto"
	"github.com/carbynestack/ephemeral/pkg/types"

	mb "github.com/vardius/message-bus"
	"go.uber.org/zap"
)

const messageBusSize = 10000

// Wires are the event and error channels between the ASTRA FSM and the
// Discovery gRPC transport.
type Wires struct {
	In  chan *pb.Event
	Out chan *pb.Event
	Err chan error
}

// Forwarder moves events between one ASTRA player FSM and Discovery.
type Forwarder struct {
	ctx    context.Context
	player DiscoveryParticipant
	inCh   chan *pb.Event
	outCh  chan *pb.Event
	topic  string
	logger *zap.SugaredLogger
}

// NewForwarder constructs an ASTRA Discovery event forwarder.
func NewForwarder(
	ctx context.Context,
	player DiscoveryParticipant,
	inCh chan *pb.Event,
	outCh chan *pb.Event,
	topic string,
	logger *zap.SugaredLogger,
) *Forwarder {
	if logger == nil {
		logger = zap.NewNop().Sugar()
	}

	return &Forwarder{
		ctx:    ctx,
		player: player,
		inCh:   inCh,
		outCh:  outCh,
		topic:  topic,
		logger: logger,
	}
}

// Run forwards outgoing FSM events to Discovery and delivers incoming
// Discovery events to the ASTRA player's local FSM topic.
func (f *Forwarder) Run() error {
	if f.ctx == nil {
		return errors.New("ASTRA forwarder context is nil")
	}
	if f.player == nil {
		return errors.New("ASTRA forwarder player is nil")
	}

	if err := f.player.Bus().Subscribe(
		types.DiscoveryTopic,
		func(event interface{}) {
			fsmEvent := event.(*fsm.Event)
			f.outCh <- fsmEvent.Meta.TransportMsg
		},
	); err != nil {
		return fmt.Errorf("subscribe ASTRA Discovery topic: %w", err)
	}

	for {
		select {
		case event := <-f.inCh:
			f.player.PublishEvent(event.Name, f.topic, event)

		case <-f.ctx.Done():
			f.logger.Info(
				"Stopping ASTRA Discovery forwarder after context cancellation",
			)
			f.player.Bus().Close(types.DiscoveryTopic)
			f.player.Stop()
			return nil
		}
	}
}

// PlayerWithIO combines the ASTRA player FSM, forwarder, gRPC client, and
// event channels for one computation activation.
type PlayerWithIO struct {
	Forwarder *Forwarder
	Player    *Player
	Wires     *Wires
	Client    c.TransportClient
}

// NewPlayerWithIO constructs an ASTRA participant connected to Discovery.
func NewPlayerWithIO(
	ctx *ASTRACtxConfig,
	pod string,
	engine ASTRAMPCEngine,
	logger *zap.SugaredLogger,
) (*PlayerWithIO, error) {
	if ctx == nil {
		return nil, errors.New("ASTRA context is nil")
	}
	if ctx.Config == nil {
		return nil, errors.New("ASTRA typed configuration is nil")
	}
	if ctx.Activation == nil {
		return nil, errors.New("ASTRA activation is nil")
	}
	if ctx.Context == nil {
		return nil, errors.New("ASTRA Go context is nil")
	}

	bus := mb.New(messageBusSize)
	topic := NewTopicFromPlayerID(ctx.Config.PlayerID)

	params := &PlayerParams{
		GameID:    ctx.Activation.GameID,
		Pod:       pod,
		PlayerID:  ctx.Config.PlayerID,
		Players:   ctx.Config.PlayerCount,
		Frontend:  ctx.Config.FrontendURL,
		TopicName: topic,
	}

	player, err := NewPlayer(
		ctx.Context,
		bus,
		ctx.Config.StateTimeout,
		ctx.Config.ComputationTimeout,
		engine,
		params,
		ctx.ErrCh,
		logger,
	)
	if err != nil {
		return nil, fmt.Errorf("create ASTRA player: %w", err)
	}

	wires := &Wires{
		In:  make(chan *pb.Event, 1),
		Out: make(chan *pb.Event, 1),
		Err: ctx.ErrCh,
	}

	forwarder := NewForwarder(
		ctx.Context,
		player,
		wires.In,
		wires.Out,
		topic,
		logger,
	)

	client, err := NewTransportClient(ctx, wires, logger)
	if err != nil {
		return nil, err
	}

	return &PlayerWithIO{
		Forwarder: forwarder,
		Player:    player,
		Wires:     wires,
		Client:    client,
	}, nil
}

// Start opens the Discovery gRPC transport and begins ASTRA registration.
func (p *PlayerWithIO) Start() {
	go func() {
		if err := p.Forwarder.Run(); err != nil {
			select {
			case p.Wires.Err <- err:
			default:
			}
		}
	}()

	connection, err := p.Client.Connect()
	if err != nil {
		p.Wires.Err <- err
		return
	}

	discoveryClient := pb.NewDiscoveryClient(connection)
	go p.Client.Run(discoveryClient)

	p.Player.Init()
}

// History returns the ASTRA player's Discovery FSM history.
func (p *PlayerWithIO) History() *fsm.History {
	return p.Player.History()
}

// NewTransportClient creates ASTRA's gRPC Discovery transport client.
func NewTransportClient(
	ctx *ASTRACtxConfig,
	wires *Wires,
	logger *zap.SugaredLogger,
) (*c.Client, error) {
	config := &c.TransportClientConfig{
		In:             wires.In,
		Out:            wires.Out,
		ErrCh:          wires.Err,
		Host:           ctx.Config.DiscoveryConfig.Host,
		Port:           ctx.Config.DiscoveryConfig.Port,
		EventScope:     types.EventScopeSelf,
		ConnID:         ctx.Activation.GameID,
		ConnectTimeout: ctx.Config.DiscoveryConfig.ConnectTimeout,
		Logger:         logger,
		Context:        ctx.Context,
	}

	client, err := c.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("create ASTRA Discovery transport client: %w", err)
	}

	return client, nil
}
