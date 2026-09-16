package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/carbynestack/ephemeral/pkg/amphora"
	"github.com/carbynestack/ephemeral/pkg/castor"
	l "github.com/carbynestack/ephemeral/pkg/logger"
	"github.com/carbynestack/ephemeral/pkg/opa"
	"github.com/carbynestack/ephemeral/pkg/types"
	"github.com/carbynestack/ephemeral/pkg/utils"

	"go.uber.org/zap"
)

const (
	defaultConfig    = "/etc/config/config.json"
	defaultTlsConfig = "/etc/tls"

	// Restriction of our initial experiment, not all ASTRA deployments.
	experimentRingBits = 64
)

func main() {
	configPath := flag.String(
		"config",
		defaultConfig,
		"path to the runtime JSON configuration",
	)
	flag.Parse()

	logger, err := l.NewDevelopmentLogger()
	if err != nil {
		panic(err)
	}
	defer func() {
		_ = logger.Sync()
	}()

	rawConfig, err := ParseASTRAConfig(*configPath)
	if err != nil {
		panic(err)
	}

	config, err := InitASTRATypedConfig(rawConfig, logger)
	if err != nil {
		panic(err)
	}

	logger.Infow(
		"ASTRA configuration initialization completed",
		"ringBits", config.RingBits,
		"playerID", config.PlayerID,
		"playerCount", config.PlayerCount,
		"computationTimeout", config.ComputationTimeout.String(),
	)

	// Current checkpoint ends here.
	// Engine construction and HTTP serving will be added later.
}

// ParseASTRAConfig reads JSON into the shared raw configuration type.
func ParseASTRAConfig(path string) (*types.ASTRAEngineConfig, error) {
	data, err := utils.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read ASTRA configuration: %w", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var conf *types.ASTRAEngineConfig
	if err := decoder.Decode(&conf); err != nil {
		return nil, fmt.Errorf("decode ASTRA configuration: %w", err)
	}
	if conf == nil {
		return nil, errors.New("ASTRA configuration must be a JSON object")
	}

	// Reject another JSON value after the configuration object.
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New(
				"ASTRA configuration must contain exactly one JSON object",
			)
		}
		return nil, fmt.Errorf(
			"invalid trailing configuration content: %w", err,
		)
	}

	return conf, nil
}

// InitASTRATypedConfig validates raw values, converts durations,
// constructs clients, and returns the complete typed configuration.
func InitASTRATypedConfig(
	conf *types.ASTRAEngineConfig,
	logger *zap.SugaredLogger,
) (*types.ASTRAEngineTypedConfig, error) {
	if conf == nil {
		return nil, errors.New("ASTRA configuration is nil")
	}

	if conf.RingBits != experimentRingBits {
		return nil, fmt.Errorf(
			"ringBits must be %d for this ASTRA experiment; got %d",
			experimentRingBits,
			conf.RingBits,
		)
	}

	// Online-process configuration for MP-SPDZ v0.4.3 astra-party.x.
	if conf.PlayerCount != 2 {
		return nil, fmt.Errorf(
			"playerCount must be 2 for the ASTRA online runtime; got %d",
			conf.PlayerCount,
		)
	}
	if conf.PlayerID < 0 || conf.PlayerID >= conf.PlayerCount {
		return nil, fmt.Errorf(
			"playerID must be 0 or 1; got %d",
			conf.PlayerID,
		)
	}

	retrySleep, err := parsePositiveDuration(
		"retrySleep", conf.RetrySleep,
	)
	if err != nil {
		return nil, err
	}

	networkEstablishTimeout, err := parsePositiveDuration(
		"networkEstablishTimeout", conf.NetworkEstablishTimeout,
	)
	if err != nil {
		return nil, err
	}

	stateTimeout, err := parsePositiveDuration(
		"stateTimeout", conf.StateTimeout,
	)
	if err != nil {
		return nil, err
	}

	computationTimeout, err := parsePositiveDuration(
		"computationTimeout", conf.ComputationTimeout,
	)
	if err != nil {
		return nil, err
	}

	connectTimeout, err := parsePositiveDuration(
		"discoveryConfig.connectTimeout",
		conf.DiscoveryConfig.ConnectTimeout,
	)
	if err != nil {
		return nil, err
	}

	programIdentifier, present := os.LookupEnv(
		"EPHEMERAL_PROGRAM_IDENTIFIER",
	)
	if !present {
		programIdentifier = conf.ProgramIdentifier
	}
	if programIdentifier == "" {
		return nil, errors.New("programIdentifier must not be empty")
	}

	policyPackage, present := os.LookupEnv(
		"EPHEMERAL_OPA_POLICY_PACKAGE",
	)
	if !present {
		policyPackage = conf.OpaConfig.PolicyPackage
	}

	if conf.DiscoveryConfig.Host == "" {
		return nil, errors.New("discoveryConfig.host must not be empty")
	}

	port, err := strconv.Atoi(conf.DiscoveryConfig.Port)
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New(
			"discoveryConfig.port must be an integer between 1 and 65535",
		)
	}

	opaClient, err := opa.NewClient(
		logger,
		conf.OpaConfig.Endpoint,
		policyPackage,
	)
	if err != nil {
		return nil, fmt.Errorf("initialize OPA client: %w", err)
	}

	amphoraClient, err := amphora.NewClient(url.URL{
		Host:   conf.AmphoraConfig.Host,
		Scheme: conf.AmphoraConfig.Scheme,
		Path:   conf.AmphoraConfig.Path,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize Amphora client: %w", err)
	}

	castorClient, err := castor.NewClient(url.URL{
		Host:   conf.CastorConfig.Host,
		Scheme: conf.CastorConfig.Scheme,
		Path:   conf.CastorConfig.Path,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize Castor client: %w", err)
	}

	var tlsConfig *tls.Config
	if conf.TlsEnabled {
		tlsConfig, err = utils.CreateTLSConfig(defaultTlsConfig)
		if err != nil {
			return nil, fmt.Errorf("initialize TLS configuration: %w", err)
		}
	}

	return &types.ASTRAEngineTypedConfig{
		ProgramIdentifier:       programIdentifier,
		AuthUserIdField:         conf.AuthUserIdField,
		RingBits:                conf.RingBits,
		RetrySleep:              retrySleep,
		NetworkEstablishTimeout: networkEstablishTimeout,
		StateTimeout:            stateTimeout,
		ComputationTimeout:      computationTimeout,
		PlayerID:                conf.PlayerID,
		PlayerCount:             conf.PlayerCount,
		FrontendURL:             conf.FrontendURL,
		PrepFolder:              conf.PrepFolder,
		OpaClient:               opaClient,
		AmphoraClient:           amphoraClient,
		CastorClient:            castorClient,
		DiscoveryConfig: types.DiscoveryClientTypedConfig{
			Host:           conf.DiscoveryConfig.Host,
			Port:           conf.DiscoveryConfig.Port,
			ConnectTimeout: connectTimeout,
		},
		TlsConfig: tlsConfig,
	}, nil
}

// parsePositiveDuration converts a duration string and rejects
// zero or negative values, matching our ASTRA experiment's validation.
func parsePositiveDuration(name, value string) (time.Duration, error) {
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	if duration <= 0 {
		return 0, fmt.Errorf("%s must be greater than zero", name)
	}
	return duration, nil
}
