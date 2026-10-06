// Copyright (c) 2026
//
// SPDX-License-Identifier: Apache-2.0

package astra

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/carbynestack/ephemeral/pkg/types"

	"go.uber.org/zap"
)

// CommandExecutor executes an ASTRA runtime command.
type CommandExecutor interface {
	CallCMD(
		context.Context,
		[]string,
		string,
	) ([]byte, []byte, error)
}

// Relay manages ASTRA's local connection relay for Player 1.
type Relay interface {
	Run(context.Context, []PeerRoute) error
	Stop()
}

// OnlineProcessConfig identifies the native MP-SPDZ ASTRA runtime and its
// prepared working directory.
type OnlineProcessConfig struct {
	BinaryPath       string `json:"binaryPath"`
	WorkingDirectory string `json:"workingDirectory"`
	ProgramName      string `json:"programName"`
	InputFilePrefix  string `json:"inputFilePrefix"`
}

// Engine launches the MP-SPDZ ASTRA online process.
type Engine struct {
	logger        *zap.SugaredLogger
	executor      CommandExecutor
	config        *ASTRAEngineTypedConfig
	processConfig OnlineProcessConfig
	relay         Relay
}

// NewEngine constructs an ASTRA online engine.
func NewEngine(
	logger *zap.SugaredLogger,
	executor CommandExecutor,
	config *ASTRAEngineTypedConfig,
	processConfig OnlineProcessConfig,
	relay Relay,
) (*Engine, error) {
	if logger == nil {
		logger = zap.NewNop().Sugar()
	}

	if executor == nil {
		return nil, errors.New("ASTRA command executor is nil")
	}

	if config == nil {
		return nil, errors.New("ASTRA typed configuration is nil")
	}

	if config.PlayerCount != OnlinePlayerCount {
		return nil, fmt.Errorf(
			"ASTRA online player count must be %d; got %d",
			OnlinePlayerCount,
			config.PlayerCount,
		)
	}

	if config.PlayerID != Player0 && config.PlayerID != Player1 {
		return nil, fmt.Errorf(
			"ASTRA player ID must be 0 or 1; got %d",
			config.PlayerID,
		)
	}

	if strings.TrimSpace(processConfig.BinaryPath) == "" {
		return nil, errors.New("ASTRA binary path is empty")
	}

	if strings.TrimSpace(processConfig.WorkingDirectory) == "" {
		return nil, errors.New("ASTRA working directory is empty")
	}

	if strings.TrimSpace(processConfig.ProgramName) == "" {
		return nil, errors.New("ASTRA program name is empty")
	}

	if strings.TrimSpace(processConfig.InputFilePrefix) == "" {
		return nil, errors.New("ASTRA input file prefix is empty")
	}

	return &Engine{
		logger:        logger,
		executor:      executor,
		config:        config,
		processConfig: processConfig,
		relay:         relay,
	}, nil
}

// Activate starts the local ASTRA online process.
//
// Player 0 listens directly on Player0Port. Player 1 first starts its local
// relay on Player0Port, then connects to that local relay through
// astra-party.x.
func (e *Engine) Activate(ctx *ASTRACtxConfig) ([]byte, error) {
	if ctx == nil {
		return nil, errors.New("ASTRA execution context is nil")
	}

	if ctx.Activation == nil {
		return nil, errors.New("ASTRA activation is nil")
	}

	if ctx.Context == nil {
		return nil, errors.New("ASTRA execution context has no Go context")
	}

	if ctx.Config == nil {
		return nil, errors.New("ASTRA execution context has no typed configuration")
	}

	if ctx.Config.PlayerID != e.config.PlayerID {
		return nil, fmt.Errorf(
			"ASTRA context player ID %d does not match engine player ID %d",
			ctx.Config.PlayerID,
			e.config.PlayerID,
		)
	}

	if e.config.PlayerID == Player0 && len(ctx.Routes) != 0 {
		return nil, errors.New(
			"ASTRA Player 0 must not have outbound relay routes",
		)
	}

	if e.config.PlayerID == Player1 {
		if len(ctx.Routes) != 1 {
			return nil, fmt.Errorf(
				"ASTRA Player 1 requires exactly one relay route; got %d",
				len(ctx.Routes),
			)
		}

		if e.relay == nil {
			return nil, errors.New("ASTRA Player 1 relay is nil")
		}

		if err := e.relay.Run(ctx.Context, ctx.Routes); err != nil {
			return nil, fmt.Errorf("start ASTRA Player 1 relay: %w", err)
		}
		defer e.relay.Stop()
	}

	command := e.onlineCommand()

	e.logger.Infow(
		"Starting MP-SPDZ ASTRA online process",
		"playerID", e.config.PlayerID,
		"gameID", ctx.Activation.GameID,
		"command", command,
		"workingDirectory", e.processConfig.WorkingDirectory,
	)

	stdout, stderr, err := e.executor.CallCMD(
		ctx.Context,
		[]string{command},
		e.processConfig.WorkingDirectory,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"ASTRA online process failed: %w; stdout: %s; stderr: %s",
			err,
			string(stdout),
			string(stderr),
		)
	}

	return stdout, nil
}

func (e *Engine) onlineCommand() string {
	return fmt.Sprintf(
		"%s %d %s -IF %s -pn %d -h localhost",
		e.processConfig.BinaryPath,
		e.config.PlayerID,
		e.processConfig.ProgramName,
		e.processConfig.InputFilePrefix,
		e.config.RuntimePort,
	)
}
