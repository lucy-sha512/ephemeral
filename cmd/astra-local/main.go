package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	l "github.com/carbynestack/ephemeral/pkg/logger"
	"github.com/carbynestack/ephemeral/pkg/utils"
)

const defaultConfig = "./experiments/astra/local-online-config.json"

type LocalOnlineConfig struct {
	Mode             string              `json:"mode"`
	MPSpdz           MPSpdzConfig        `json:"mpSpdz"`
	Network          LocalNetworkConfig  `json:"network"`
	Workspace        WorkspaceConfig     `json:"workspace"`
	Players          []LocalPlayerConfig `json:"players"`
	ExecutionTimeout string              `json:"executionTimeout"`
	ExpectedOutput   string              `json:"expectedOutput"`
}

type MPSpdzConfig struct {
	BinaryPath                string `json:"binaryPath"`
	ProgramName               string `json:"programName"`
	ProgramsSourceDirectory   string `json:"programsSourceDirectory"`
	PlayerDataSourceDirectory string `json:"playerDataSourceDirectory"`
	InputFilePrefix           string `json:"inputFilePrefix"`
	RingBits                  int    `json:"ringBits"`
	OnlinePlayerCount         int    `json:"onlinePlayerCount"`
	PreprocessingPlayerCount  int    `json:"preprocessingPlayerCount"`
	ThreadCount               int    `json:"threadCount"`
}

type LocalNetworkConfig struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type WorkspaceConfig struct {
	Root     string `json:"root"`
	Recreate bool   `json:"recreate"`
}

type LocalPlayerConfig struct {
	OnlinePlayerID             int      `json:"onlinePlayerID"`
	WorkspaceName              string   `json:"workspaceName"`
	RequiredPreprocessingFiles []string `json:"requiredPreprocessingFiles"`
}

type LocalASTRAHarness struct {
	config *LocalOnlineConfig
	logger *zap.SugaredLogger
}

type PreparedPlayer struct {
	PlayerID  int
	Workspace string
	Binary    string
}

type PlayerExecution struct {
	PlayerID  int
	Workspace string
	Command   []string
	Output    string
	Err       error
}

type runningPlayer struct {
	player  PreparedPlayer
	command *exec.Cmd
	output  bytes.Buffer
}

func NewLocalASTRAHarness(
	config *LocalOnlineConfig,
	logger *zap.SugaredLogger,
) *LocalASTRAHarness {
	return &LocalASTRAHarness{
		config: config,
		logger: logger,
	}
}

func main() {
	configPath := flag.String(
		"config",
		defaultConfig,
		"path to the local ASTRA online-launch JSON configuration",
	)
	flag.Parse()

	logger, err := l.NewDevelopmentLogger()
	if err != nil {
		panic(err)
	}
	defer func() {
		_ = logger.Sync()
	}()

	config, err := ParseLocalOnlineConfig(*configPath)
	if err != nil {
		panic(err)
	}

	if err := ValidateLocalOnlineConfig(config); err != nil {
		panic(err)
	}

	logger.Infow(
		"ASTRA local online configuration validated",
		"program", config.MPSpdz.ProgramName,
		"binary", config.MPSpdz.BinaryPath,
		"onlinePlayerCount", config.MPSpdz.OnlinePlayerCount,
		"preprocessingPlayerCount", config.MPSpdz.PreprocessingPlayerCount,
		"host", config.Network.Host,
		"port", config.Network.Port,
		"workspaceRoot", config.Workspace.Root,
	)

	for _, player := range config.Players {
		logger.Infow(
			"ASTRA local player fixture validated",
			"playerID", player.OnlinePlayerID,
			"workspace",
			filepath.Join(config.Workspace.Root, player.WorkspaceName),
			"requiredPreprocessingFiles",
			player.RequiredPreprocessingFiles,
		)
	}

	// 2.

	harness := NewLocalASTRAHarness(config, logger)

	preparedPlayers, err := harness.PrepareWorkspace()
	if err != nil {
		panic(err)
	}

	for _, player := range preparedPlayers {
		logger.Infow(
			"ASTRA local player workspace prepared",
			"playerID", player.PlayerID,
			"workspace", player.Workspace,
			"binary", player.Binary,
		)
	}

	executions, err := harness.RunOnline(preparedPlayers)
	if err != nil {
		panic(err)
	}

	for _, execution := range executions {
		logger.Infow(
			"ASTRA local online player completed",
			"playerID", execution.PlayerID,
			"workspace", execution.Workspace,
			"command", execution.Command,
			"output", execution.Output,
		)
	}
}

func ParseLocalOnlineConfig(path string) (*LocalOnlineConfig, error) {
	data, err := utils.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read local ASTRA configuration: %w", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var config LocalOnlineConfig
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("decode local ASTRA configuration: %w", err)
	}

	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New(
				"local ASTRA configuration must contain exactly one JSON object",
			)
		}

		return nil, fmt.Errorf(
			"invalid trailing local ASTRA configuration content: %w",
			err,
		)
	}

	return &config, nil
}

func ValidateLocalOnlineConfig(config *LocalOnlineConfig) error {
	if config == nil {
		return errors.New("local ASTRA configuration is nil")
	}

	if config.Mode != "local-online-fixture" {
		return fmt.Errorf(
			"mode must be %q; got %q",
			"local-online-fixture",
			config.Mode,
		)
	}

	if config.MPSpdz.RingBits != 64 {
		return fmt.Errorf(
			"ringBits must be 64 for this ASTRA test; got %d",
			config.MPSpdz.RingBits,
		)
	}

	if config.MPSpdz.OnlinePlayerCount != 2 {
		return fmt.Errorf(
			"onlinePlayerCount must be 2 for MP-SPDZ ASTRA; got %d",
			config.MPSpdz.OnlinePlayerCount,
		)
	}

	if config.MPSpdz.PreprocessingPlayerCount != 3 {
		return fmt.Errorf(
			"preprocessingPlayerCount must be 3 for MP-SPDZ ASTRA; got %d",
			config.MPSpdz.PreprocessingPlayerCount,
		)
	}

	if config.MPSpdz.ThreadCount != 1 {
		return fmt.Errorf(
			"threadCount must be 1 for the initial ASTRA test; got %d",
			config.MPSpdz.ThreadCount,
		)
	}

	if err := validateExecutable(
		"mpSpdz.binaryPath",
		config.MPSpdz.BinaryPath,
	); err != nil {
		return err
	}

	if err := validateDirectory(
		"mpSpdz.programsSourceDirectory",
		config.MPSpdz.ProgramsSourceDirectory,
	); err != nil {
		return err
	}

	if err := validateDirectory(
		"mpSpdz.playerDataSourceDirectory",
		config.MPSpdz.PlayerDataSourceDirectory,
	); err != nil {
		return err
	}

	if err := validateProgramArtifacts(config.MPSpdz); err != nil {
		return err
	}

	if strings.TrimSpace(config.Network.Host) == "" {
		return errors.New("network.host must not be empty")
	}

	if config.Network.Port < 1 || config.Network.Port > 65535 {
		return fmt.Errorf(
			"network.port must be between 1 and 65535; got %d",
			config.Network.Port,
		)
	}

	if !filepath.IsAbs(config.Workspace.Root) {
		return fmt.Errorf(
			"workspace.root must be an absolute path; got %q",
			config.Workspace.Root,
		)
	}

	if len(config.Players) != config.MPSpdz.OnlinePlayerCount {
		return fmt.Errorf(
			"players must contain %d entries; got %d",
			config.MPSpdz.OnlinePlayerCount,
			len(config.Players),
		)
	}

	seenPlayerIDs := make(map[int]bool)

	for _, player := range config.Players {
		if player.OnlinePlayerID < 0 ||
			player.OnlinePlayerID >= config.MPSpdz.OnlinePlayerCount {
			return fmt.Errorf(
				"invalid onlinePlayerID %d",
				player.OnlinePlayerID,
			)
		}

		if seenPlayerIDs[player.OnlinePlayerID] {
			return fmt.Errorf(
				"duplicate onlinePlayerID %d",
				player.OnlinePlayerID,
			)
		}
		seenPlayerIDs[player.OnlinePlayerID] = true

		if err := validateWorkspaceName(player.WorkspaceName); err != nil {
			return fmt.Errorf(
				"player %d: %w",
				player.OnlinePlayerID,
				err,
			)
		}

		if len(player.RequiredPreprocessingFiles) == 0 {
			return fmt.Errorf(
				"player %d has no required preprocessing files",
				player.OnlinePlayerID,
			)
		}

		for _, file := range player.RequiredPreprocessingFiles {
			if err := validatePreprocessingFile(
				config.MPSpdz.PlayerDataSourceDirectory,
				file,
			); err != nil {
				return fmt.Errorf(
					"player %d: %w",
					player.OnlinePlayerID,
					err,
				)
			}
		}
	}

	if strings.TrimSpace(config.ExpectedOutput) == "" {
		return errors.New("expectedOutput must not be empty")
	}

	executionTimeout, err := time.ParseDuration(config.ExecutionTimeout)
	if err != nil {
		return fmt.Errorf(
			"executionTimeout must be a Go duration: %w",
			err,
		)
	}
	if executionTimeout <= 0 {
		return fmt.Errorf(
			"executionTimeout must be positive; got %q",
			config.ExecutionTimeout,
		)
	}

	return nil
}

func validateExecutable(name string, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}

	if info.IsDir() {
		return fmt.Errorf("%s must be a file; got directory %q", name, path)
	}

	if info.Mode()&0111 == 0 {
		return fmt.Errorf("%s is not executable: %q", name, path)
	}

	return nil
}

func validateDirectory(name string, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}

	if !info.IsDir() {
		return fmt.Errorf("%s must be a directory; got %q", name, path)
	}

	return nil
}

func validateRegularFile(name string, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if info.IsDir() {
		return fmt.Errorf("%s must be a file, got directory %q", name, path)
	}
	return nil
}

func validateProgramArtifacts(config MPSpdzConfig) error {
	if strings.TrimSpace(config.ProgramName) == "" {
		return errors.New("mpSpdz.programName must not be empty")
	}

	bytecode := filepath.Join(
		config.ProgramsSourceDirectory,
		"Bytecode",
		config.ProgramName+"-0.bc",
	)
	if err := validateRegularFile("compiled program bytecode", bytecode); err != nil {
		return err
	}

	schedule := filepath.Join(
		config.ProgramsSourceDirectory,
		"Schedules",
		config.ProgramName+".sch",
	)
	if _, err := os.Stat(schedule); err != nil {
		return fmt.Errorf("compiled program schedule: %w", err)
	}

	inputBaseName := filepath.Base(config.InputFilePrefix)
	for playerID := 0; playerID < config.OnlinePlayerCount; playerID++ {
		inputFile := filepath.Join(
			config.PlayerDataSourceDirectory,
			fmt.Sprintf("%s-P%d-0", inputBaseName, playerID),
		)

		if _, err := os.Stat(inputFile); err != nil {
			return fmt.Errorf(
				"ASTRA input file for player %d: %w",
				playerID,
				err,
			)
		}
	}

	for playerID := 0; playerID < config.PreprocessingPlayerCount; playerID++ {
		certificate := filepath.Join(
			config.PlayerDataSourceDirectory,
			fmt.Sprintf("P%d.pem", playerID),
		)

		if _, err := os.Stat(certificate); err != nil {
			return fmt.Errorf(
				"ASTRA certificate for player %d: %w",
				playerID,
				err,
			)
		}
	}

	return nil
}

func validateWorkspaceName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("workspaceName must not be empty")
	}

	if filepath.Base(name) != name || name == "." {
		return fmt.Errorf(
			"workspaceName must be one directory name; got %q",
			name,
		)
	}

	return nil
}

func validatePreprocessingFile(
	playerDataDirectory string,
	relativeFile string,
) error {
	if filepath.IsAbs(relativeFile) {
		return fmt.Errorf(
			"preprocessing file must be relative; got %q",
			relativeFile,
		)
	}

	path := filepath.Join(playerDataDirectory, relativeFile)
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf(
			"required preprocessing file %q: %w",
			relativeFile,
			err,
		)
	}

	return nil
}

func (h *LocalASTRAHarness) PrepareWorkspace() ([]PreparedPlayer, error) {
	root := h.config.Workspace.Root

	if !h.config.Workspace.Recreate {
		return nil, errors.New(
			"workspace.recreate must be true for the local ASTRA fixture",
		)
	}

	if err := os.RemoveAll(root); err != nil {
		return nil, fmt.Errorf(
			"remove previous local ASTRA workspace %q: %w",
			root,
			err,
		)
	}

	if err := os.MkdirAll(root, 0750); err != nil {
		return nil, fmt.Errorf(
			"create local ASTRA workspace root %q: %w",
			root,
			err,
		)
	}

	prepared := make([]PreparedPlayer, 0, len(h.config.Players))

	for _, player := range h.config.Players {
		workspace := filepath.Join(root, player.WorkspaceName)

		if err := h.preparePlayerWorkspace(workspace, player); err != nil {
			return nil, fmt.Errorf(
				"prepare player %d workspace: %w",
				player.OnlinePlayerID,
				err,
			)
		}

		prepared = append(prepared, PreparedPlayer{
			PlayerID:  player.OnlinePlayerID,
			Workspace: workspace,
			Binary: filepath.Join(
				workspace,
				filepath.Base(h.config.MPSpdz.BinaryPath),
			),
		})
	}

	return prepared, nil
}

func (h *LocalASTRAHarness) preparePlayerWorkspace(
	workspace string,
	player LocalPlayerConfig,
) error {
	if err := os.MkdirAll(workspace, 0750); err != nil {
		return fmt.Errorf("create workspace %q: %w", workspace, err)
	}

	stagedBinary := filepath.Join(
		workspace,
		filepath.Base(h.config.MPSpdz.BinaryPath),
	)
	if err := copyFile(
		h.config.MPSpdz.BinaryPath,
		stagedBinary,
	); err != nil {
		return fmt.Errorf("stage astra-party binary: %w", err)
	}

	stagedPrograms := filepath.Join(workspace, "Programs")
	if err := copyTree(
		h.config.MPSpdz.ProgramsSourceDirectory,
		stagedPrograms,
	); err != nil {
		return fmt.Errorf("stage MP-SPDZ program artifacts: %w", err)
	}

	stagedPlayerData := filepath.Join(workspace, "Player-Data")
	if err := copyTree(
		h.config.MPSpdz.PlayerDataSourceDirectory,
		stagedPlayerData,
	); err != nil {
		return fmt.Errorf("stage ASTRA Player-Data: %w", err)
	}

	if err := validatePreparedPlayerWorkspace(
		h.config,
		player,
		workspace,
	); err != nil {
		return err
	}

	return nil
}

func validatePreparedPlayerWorkspace(
	config *LocalOnlineConfig,
	player LocalPlayerConfig,
	workspace string,
) error {
	binary := filepath.Join(
		workspace,
		filepath.Base(config.MPSpdz.BinaryPath),
	)
	if err := validateExecutable("staged astra-party binary", binary); err != nil {
		return err
	}

	bytecode := filepath.Join(
		workspace,
		"Programs",
		"Bytecode",
		config.MPSpdz.ProgramName+"-0.bc",
	)
	if err := validateRegularFile("staged ASTRA bytecode", bytecode); err != nil {
		return err
	}

	schedule := filepath.Join(
		workspace,
		"Programs",
		"Schedules",
		config.MPSpdz.ProgramName+".sch",
	)
	if err := validateRegularFile("staged ASTRA schedule", schedule); err != nil {
		return err
	}

	inputBaseName := filepath.Base(config.MPSpdz.InputFilePrefix)
	inputFile := filepath.Join(
		workspace,
		"Player-Data",
		fmt.Sprintf("%s-P%d-0", inputBaseName, player.OnlinePlayerID),
	)
	if err := validateRegularFile("staged ASTRA input", inputFile); err != nil {
		return err
	}

	for _, relativeFile := range player.RequiredPreprocessingFiles {
		preprocessingFile := filepath.Join(
			workspace,
			"Player-Data",
			relativeFile,
		)
		if err := validateRegularFile(
			"staged ASTRA preprocessing file",
			preprocessingFile,
		); err != nil {
			return err
		}
	}

	return nil
}

func copyTree(source string, destination string) error {
	sourceInfo, err := os.Lstat(source)
	if err != nil {
		return fmt.Errorf("inspect source tree %q: %w", source, err)
	}
	if !sourceInfo.IsDir() {
		return fmt.Errorf("source tree must be a directory: %q", source)
	}

	return filepath.WalkDir(
		source,
		func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}

			relativePath, err := filepath.Rel(source, path)
			if err != nil {
				return fmt.Errorf(
					"derive path relative to %q: %w",
					source,
					err,
				)
			}

			target := filepath.Join(destination, relativePath)
			info, err := os.Lstat(path)
			if err != nil {
				return fmt.Errorf("inspect %q: %w", path, err)
			}

			switch {
			case info.Mode()&os.ModeSymlink != 0:
				linkTarget, err := os.Readlink(path)
				if err != nil {
					return fmt.Errorf("read symbolic link %q: %w", path, err)
				}
				return os.Symlink(linkTarget, target)

			case info.IsDir():
				if err := os.MkdirAll(target, info.Mode().Perm()); err != nil {
					return fmt.Errorf("create directory %q: %w", target, err)
				}
				return os.Chmod(target, info.Mode().Perm())

			case info.Mode().IsRegular():
				return copyFile(path, target)

			default:
				return fmt.Errorf(
					"unsupported file type for %q",
					path,
				)
			}
		},
	)
}

func copyFile(source string, destination string) error {
	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("inspect source file %q: %w", source, err)
	}

	if !info.Mode().IsRegular() {
		return fmt.Errorf("source is not a regular file: %q", source)
	}

	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open source file %q: %w", source, err)
	}
	defer input.Close()

	if err := os.MkdirAll(filepath.Dir(destination), 0750); err != nil {
		return fmt.Errorf(
			"create parent directory for %q: %w",
			destination,
			err,
		)
	}

	output, err := os.OpenFile(
		destination,
		os.O_WRONLY|os.O_CREATE|os.O_TRUNC,
		info.Mode().Perm(),
	)
	if err != nil {
		return fmt.Errorf("create destination file %q: %w", destination, err)
	}

	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()

	if copyErr != nil {
		return fmt.Errorf(
			"copy %q to %q: %w",
			source,
			destination,
			copyErr,
		)
	}
	if closeErr != nil {
		return fmt.Errorf("close destination file %q: %w", destination, closeErr)
	}

	return os.Chmod(destination, info.Mode().Perm())
}

func (h *LocalASTRAHarness) RunOnline(
	players []PreparedPlayer,
) ([]PlayerExecution, error) {
	timeout, err := time.ParseDuration(h.config.ExecutionTimeout)
	if err != nil {
		return nil, fmt.Errorf("parse execution timeout: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	running := make([]*runningPlayer, 0, len(players))

	for _, player := range players {
		args := []string{
			strconv.Itoa(player.PlayerID),
			h.config.MPSpdz.ProgramName,
			"-IF",
			h.config.MPSpdz.InputFilePrefix,
			"-pn",
			strconv.Itoa(h.config.Network.Port),
			"-h",
			h.config.Network.Host,
		}

		process := &runningPlayer{
			player: player,
		}
		process.command = exec.CommandContext(
			ctx,
			player.Binary,
			args...,
		)
		process.command.Dir = player.Workspace
		process.command.Stdout = &process.output
		process.command.Stderr = &process.output

		if err := process.command.Start(); err != nil {
			cancel()

			for _, started := range running {
				_ = started.command.Wait()
			}

			return nil, fmt.Errorf(
				"start ASTRA player %d: %w",
				player.PlayerID,
				err,
			)
		}

		running = append(running, process)
	}

	results := make(chan PlayerExecution, len(running))

	for _, process := range running {
		go func(process *runningPlayer) {
			err := process.command.Wait()

			results <- PlayerExecution{
				PlayerID:  process.player.PlayerID,
				Workspace: process.player.Workspace,
				Command:   process.command.Args,
				Output:    process.output.String(),
				Err:       err,
			}
		}(process)
	}

	executions := make([]PlayerExecution, 0, len(running))
	var executionErr error

	for range running {
		execution := <-results
		executions = append(executions, execution)

		if execution.Err != nil && executionErr == nil {
			executionErr = fmt.Errorf(
				"ASTRA player %d failed: %w",
				execution.PlayerID,
				execution.Err,
			)
			cancel()
		}
	}

	if ctx.Err() == context.DeadlineExceeded {
		return executions, fmt.Errorf(
			"ASTRA online execution exceeded %s",
			timeout,
		)
	}

	if executionErr != nil {
		return executions, executionErr
	}

	return executions, h.validateExpectedOutput(executions)
}

func (h *LocalASTRAHarness) validateExpectedOutput(
	executions []PlayerExecution,
) error {
	for _, execution := range executions {
		if execution.PlayerID != 0 {
			continue
		}

		if strings.Contains(
			execution.Output,
			h.config.ExpectedOutput,
		) {
			return nil
		}

		return fmt.Errorf(
			"player 0 output does not contain %q: %s",
			h.config.ExpectedOutput,
			execution.Output,
		)
	}

	return errors.New("ASTRA player 0 execution result is missing")
}
