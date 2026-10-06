// Copyright (c) 2026
//
// SPDX-License-Identifier: Apache-2.0

package astra

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.uber.org/zap"
)

// CommandExecutor launches a runtime command and returns its captured output.
type CommandExecutor interface {
	CallCMD(
		context.Context,
		[]string,
		string,
	) ([]byte, []byte, error)
}

// Relay manages Player 1's local TCP listener and forwarding connections.
type Relay interface {
	Run(context.Context, []PeerRoute) error
	Stop()
}

// OnlineProcessConfig identifies the native executable, prepared workspace,
// compiled program, and input-file prefix.
type OnlineProcessConfig struct {
	BinaryPath       string `json:"binaryPath"`
	WorkingDirectory string `json:"workingDirectory"`
	ProgramName      string `json:"programName"`
	InputFilePrefix  string `json:"inputFilePrefix"`
}

// Engine manages one local MP-SPDZ ASTRA online process.
type Engine struct {
	logger        *zap.SugaredLogger
	executor      CommandExecutor
	config        *ASTRAEngineTypedConfig
	processConfig OnlineProcessConfig
	relay         Relay
}

// NewEngine validates the local player and process configuration.
// It does not start the relay or native process.
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

	if config.RuntimePort < 1 || config.RuntimePort > 65535 {
		return nil, fmt.Errorf(
			"ASTRA runtime port must be between 1 and 65535; got %d",
			config.RuntimePort,
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

// Activate executes the compiled ASTRA program in the prepared workspace.
//
// Player 0 listens directly on its configured RuntimePort.
// Player 1 starts a relay on its RuntimePort and connects the native process
// to that relay. Discovery supplies the relay's remote Player 0 endpoint.
//
// Activate returns captured stdout after the native process exits successfully.
func (e *Engine) Activate(ctx *ASTRACtxConfig) ([]byte, error) {
	if ctx == nil {
		return nil, errors.New("ASTRA execution context is nil")
	}

	if ctx.Activation == nil {
		return nil, errors.New("ASTRA activation is nil")
	}

	if ctx.Context == nil {
		return nil, errors.New(
			"ASTRA execution context has no Go context",
		)
	}

	if ctx.Config == nil {
		return nil, errors.New(
			"ASTRA execution context has no typed configuration",
		)
	}

	if ctx.Config.PlayerID != e.config.PlayerID {
		return nil, fmt.Errorf(
			"ASTRA context player ID %d does not match engine player ID %d",
			ctx.Config.PlayerID,
			e.config.PlayerID,
		)
	}

	if ctx.Config.RuntimePort != e.config.RuntimePort {
		return nil, fmt.Errorf(
			"ASTRA context runtime port %d does not match engine runtime port %d",
			ctx.Config.RuntimePort,
			e.config.RuntimePort,
		)
	}

	if err := ctx.Context.Err(); err != nil {
		return nil, fmt.Errorf(
			"ASTRA activation context is already closed: %w",
			err,
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

		if ctx.Routes[0].LocalPort != e.config.RuntimePort {
			return nil, fmt.Errorf(
				"ASTRA relay local port %d does not match runtime port %d",
				ctx.Routes[0].LocalPort,
				e.config.RuntimePort,
			)
		}

		if err := validateRoutes(ctx.Routes); err != nil {
			return nil, fmt.Errorf("invalid ASTRA relay route: %w", err)
		}

		if e.relay == nil {
			return nil, errors.New("ASTRA Player 1 relay is nil")
		}

		// Ensure cleanup also runs if relay startup partially succeeds.
		defer e.relay.Stop()

		if err := e.relay.Run(ctx.Context, ctx.Routes); err != nil {
			return nil, fmt.Errorf(
				"start ASTRA Player 1 relay: %w",
				err,
			)
		}
	}

	command := e.onlineCommand()

	e.logger.Infow(
		"Starting MP-SPDZ ASTRA online process",
		"playerID", e.config.PlayerID,
		"gameID", ctx.Activation.GameID,
		"runtimePort", e.config.RuntimePort,
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

	e.logger.Infow(
		"MP-SPDZ ASTRA online process completed",
		"playerID", e.config.PlayerID,
		"gameID", ctx.Activation.GameID,
		"stdout", string(stdout),
		"stderr", string(stderr),
	)

	return stdout, nil
}

// onlineCommand builds the shell command consumed by CommandExecutor.
func (e *Engine) onlineCommand() string {
	return fmt.Sprintf(
		"%s %d %s -IF %s -pn %d -h localhost",
		quoteASTRAShellArgument(e.processConfig.BinaryPath),
		e.config.PlayerID,
		quoteASTRAShellArgument(e.processConfig.ProgramName),
		quoteASTRAShellArgument(e.processConfig.InputFilePrefix),
		e.config.RuntimePort,
	)
}

// quoteASTRAShellArgument preserves paths and names as literal shell arguments.
// Simple arguments retain their existing representation for command tests.
func quoteASTRAShellArgument(value string) string {
	if value != "" && strings.IndexFunc(value, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			strings.ContainsRune("_./-", r))
	}) == -1 {
		return value
	}

	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
