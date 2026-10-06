// Copyright (c) 2026
//
// SPDX-License-Identifier: Apache-2.0

package astra

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	d "github.com/carbynestack/ephemeral/pkg/discovery"
	"github.com/carbynestack/ephemeral/pkg/discovery/fsm"
	pb "github.com/carbynestack/ephemeral/pkg/discovery/transport/proto"
	"github.com/carbynestack/ephemeral/pkg/types"

	mb "github.com/vardius/message-bus"
	"go.uber.org/zap"
)

const rawEventsTopic = "astraRawEvents"

// PlayerParams identifies one ASTRA online participant to Discovery.
type PlayerParams struct {
	GameID    string
	Pod       string
	PlayerID  int32
	Players   int32
	Frontend  string
	TopicName string
}

// DiscoveryParticipant defines the ASTRA-side player contract used later by
// the ASTRA Forwarder and gRPC transport client.
type DiscoveryParticipant interface {
	Init()
	Stop()
	History() *fsm.History
	Bus() mb.MessageBus
	PublishEvent(string, string, *pb.Event)
}

// Player coordinates one ASTRA participant through Discovery's FSM.
type Player struct {
	name       string
	fsm        *fsm.FSM
	messageBus mb.MessageBus
	engine     ASTRAMPCEngine
	params     *PlayerParams
	callbacker *Callbacker
	errCh      chan error
	logger     *zap.SugaredLogger
}

// NewPlayer constructs the ASTRA-specific Discovery FSM participant.
func NewPlayer(
	ctx context.Context,
	bus mb.MessageBus,
	stateTimeout time.Duration,
	computationTimeout time.Duration,
	engine ASTRAMPCEngine,
	params *PlayerParams,
	errCh chan error,
	logger *zap.SugaredLogger,
) (*Player, error) {
	if engine == nil {
		return nil, errors.New("ASTRA MPC engine is nil")
	}
	if params == nil {
		return nil, errors.New("ASTRA player parameters are nil")
	}
	if logger == nil {
		logger = zap.NewNop().Sugar()
	}

	callbacker := NewCallbacker(bus, params, errCh, logger)

	callbacks := []*fsm.Callback{
		fsm.AfterEnter(types.Registering).Do(
			callbacker.sendPlayerReady(),
		),
		fsm.AfterEnter(types.WaitTCPCheck).Do(
			callbacker.acceptParticipantEndpoints(),
		),
		fsm.AfterEnter(types.Playing).Do(
			callbacker.playing(params.TopicName, engine),
		),
		fsm.AfterEnter(types.PlayerFinishedWithError).Do(
			callbacker.finishWithError(params.TopicName),
		),
		fsm.AfterEnter(types.PlayerFinishedWithSuccess).Do(
			callbacker.finishWithSuccess(params.TopicName),
		),
		fsm.AfterEnter(types.PlayerDone).Do(callbacker.done()),
		fsm.WhenStateTimeout().Do(
			callbacker.finishWithError(params.TopicName),
		),
	}

	transitions := []*fsm.Transition{
		fsm.WhenIn(types.Init).
			GotEvent(types.Register).
			GoTo(types.Registering),

		fsm.WhenIn(types.Registering).
			GotEvent(types.PlayersReady).
			GoTo(types.WaitTCPCheck),

		fsm.WhenIn(types.WaitTCPCheck).
			GotEvent(types.TCPCheckSuccessAll).
			GoTo(types.Playing).
			WithTimeout(computationTimeout),

		fsm.WhenIn(types.Playing).
			GotEvent(types.PlayerFinishedWithSuccess).
			GoTo(types.PlayerFinishedWithSuccess),

		fsm.WhenIn(types.WaitTCPCheck).
			GotEvent(types.PlayingError).
			GoTo(types.PlayerFinishedWithError),

		fsm.WhenIn(types.Playing).
			GotEvent(types.PlayingError).
			GoTo(types.PlayerFinishedWithError),

		fsm.WhenIn(types.PlayerFinishedWithSuccess).
			GotEvent(types.GameSuccess).
			GoTo(types.PlayerDone),

		fsm.WhenInAnyState().
			GotEvent(types.GameError).
			GoTo(types.PlayerFinishedWithError),

		fsm.WhenInAnyState().
			GotEvent(types.PlayerDone).
			GoTo(types.PlayerDone),

		fsm.WhenInAnyState().
			GotEvent(types.StateTimeoutError).
			GoTo(types.PlayerFinishedWithError),
	}

	initializedCallbacks, initializedTransitions :=
		fsm.InitCallbacksAndTransitions(callbacks, transitions)

	machine, err := fsm.NewFSM(
		ctx,
		types.Init,
		initializedTransitions,
		initializedCallbacks,
		stateTimeout,
		logger,
	)
	if err != nil {
		return nil, err
	}

	callbacker.publisher.Fsm = machine

	if err := bus.Subscribe(rawEventsTopic, func(event interface{}) {
		discoveryEvent := event.(*pb.Event)

		callbacker.publisher.PublishWithBody(
			discoveryEvent.Name,
			params.TopicName,
			discoveryEvent,
		)
	}); err != nil {
		return nil, fmt.Errorf("subscribe ASTRA raw-event topic: %w", err)
	}

	if err := bus.Subscribe(params.TopicName, func(event interface{}) {
		machine.Write(event.(*fsm.Event))
	}); err != nil {
		return nil, fmt.Errorf("subscribe ASTRA player topic: %w", err)
	}

	return &Player{
		name:       params.TopicName,
		fsm:        machine,
		messageBus: bus,
		engine:     engine,
		params:     params,
		callbacker: callbacker,
		errCh:      errCh,
		logger:     logger,
	}, nil
}

// Init starts the FSM and initiates ASTRA player registration.
func (p *Player) Init() {
	go p.fsm.Run(p.errCh)

	p.callbacker.sendEvent(
		types.Register,
		p.name,
		struct{}{},
	)
}

// Stop closes this ASTRA player's local message-bus subscriptions.
func (p *Player) Stop() {
	p.messageBus.Close(rawEventsTopic)
	p.messageBus.Close(p.params.TopicName)
}

// History returns the ASTRA player's state-transition history.
func (p *Player) History() *fsm.History {
	return p.fsm.History()
}

// Bus returns the message bus used by the ASTRA Discovery forwarder.
func (p *Player) Bus() mb.MessageBus {
	return p.messageBus
}

// PublishEvent delivers a Discovery event to the ASTRA player's FSM.
func (p *Player) PublishEvent(
	name string,
	topic string,
	event *pb.Event,
) {
	p.callbacker.publisher.PublishWithBody(name, topic, event)
}

// NewTopicFromPlayerID creates the ASTRA player topic name.
func NewTopicFromPlayerID(playerID int32) string {
	return strconv.Itoa(int(playerID))
}

// Callbacker translates ASTRA FSM transitions into Discovery events.
type Callbacker struct {
	publisher *d.Publisher
	params    *PlayerParams
	errCh     chan error
	logger    *zap.SugaredLogger
}

// NewCallbacker creates callbacks for one ASTRA Discovery player.
func NewCallbacker(
	bus mb.MessageBus,
	params *PlayerParams,
	errCh chan error,
	logger *zap.SugaredLogger,
) *Callbacker {
	return &Callbacker{
		publisher: d.NewPublisher(bus),
		params:    params,
		errCh:     errCh,
		logger:    logger,
	}
}

func (c *Callbacker) sendPlayerReady() func(interface{}) error {
	return func(event interface{}) error {
		c.sendEvent(types.PlayerReady, types.DiscoveryTopic, event)
		return nil
	}
}

func (c *Callbacker) playing(
	topic string,
	engine ASTRAMPCEngine,
) func(interface{}) error {
	return func(event interface{}) error {
		fsmEvent := event.(*fsm.Event)

		if err := engine.Execute(fsmEvent.Meta.TransportMsg); err != nil {
			c.logger.Errorw(
				"ASTRA execution failed",
				"gameID", c.params.GameID,
				"error", err,
			)
			c.sendEvent(types.PlayingError, topic, event)
			return nil
		}

		c.sendEvent(types.PlayerFinishedWithSuccess, topic, event)
		return nil
	}
}

func (c *Callbacker) finishWithError(
	topic string,
) func(interface{}) error {
	return func(event interface{}) error {
		c.sendEvent(types.GameFinishedWithError, types.DiscoveryTopic, event)
		c.sendEvent(types.PlayerDone, topic, event)

		fsmEvent := event.(*fsm.Event)
		message := fmt.Sprintf("ASTRA game failed with event %s", fsmEvent.Name)

		if fsmEvent.Meta != nil &&
			fsmEvent.Meta.FSM != nil &&
			fsmEvent.Meta.FSM.History() != nil {
			message = fmt.Sprintf(
				"%s; states: %s",
				message,
				strings.Join(
					fsmEvent.Meta.FSM.History().GetStates(),
					" -> ",
				),
			)
		}

		select {
		case c.errCh <- errors.New(message):
		default:
		}

		return nil
	}
}

func (c *Callbacker) finishWithSuccess(
	topic string,
) func(interface{}) error {
	return func(event interface{}) error {
		c.sendEvent(
			types.GameFinishedWithSuccess,
			types.DiscoveryTopic,
			event,
		)
		// Wait for Discovery's collective GameSuccess before stopping.
		return nil
	}
}

func (c *Callbacker) done() func(interface{}) error {
	return func(event interface{}) error {
		c.publisher.Fsm.Stop()
		c.sendEvent(types.PlayerDone, types.ServiceEventsTopic, event)
		return nil
	}
}

func (c *Callbacker) sendEvent(
	name string,
	topic string,
	body interface{},
) {
	event := &pb.Event{
		GameID: c.params.GameID,
		Name:   name,
		Players: []*pb.Player{
			{
				Id:      c.params.PlayerID + DiscoveryPlayerIDOffset,
				Players: c.params.Players,
				Pod:     c.params.Pod,
				Ip:      c.params.Frontend,
			},
		},
	}

	c.publisher.PublishWithBody(name, topic, event, c.params.GameID)

	c.logger.Debugw(
		"Sending ASTRA Discovery event",
		"event", event,
		"topic", topic,
	)
}

// acceptParticipantEndpoints acknowledges validated coordination metadata.
// The legacy TCPCheckSuccess event is a barrier acknowledgment here;
// no synthetic connection is opened to the native ASTRA socket.
func (c *Callbacker) acceptParticipantEndpoints() func(interface{}) error {
	return func(event interface{}) error {
		ev := event.(*fsm.Event)
		if ev.Meta == nil || ev.Meta.TransportMsg == nil {
			c.sendEvent(types.TCPCheckFailure, types.DiscoveryTopic, event)
			c.sendEvent(types.PlayingError, c.params.TopicName, event)
			return nil
		}
		if _, err := ResolveRoutes(c.params.PlayerID, Player0Port, ev.Meta.TransportMsg); err != nil {
			c.logger.Errorw("Invalid ASTRA participant endpoints", "error", err)
			c.sendEvent(types.TCPCheckFailure, types.DiscoveryTopic, event)
			c.sendEvent(types.PlayingError, c.params.TopicName, event)
			return nil
		}
		c.logger.Infow("Acknowledging ASTRA Discovery barrier without a TCP probe", "gameID", c.params.GameID)
		c.sendEvent(types.TCPCheckSuccess, types.DiscoveryTopic, event)
		return nil
	}
}
