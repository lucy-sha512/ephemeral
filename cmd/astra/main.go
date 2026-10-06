// ASTRA service entry point

package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/carbynestack/ephemeral/pkg/amphora"
	"github.com/carbynestack/ephemeral/pkg/astra"
	"github.com/carbynestack/ephemeral/pkg/castor"
	l "github.com/carbynestack/ephemeral/pkg/logger"
	"github.com/carbynestack/ephemeral/pkg/opa"
	"github.com/carbynestack/ephemeral/pkg/types"
	"github.com/carbynestack/ephemeral/pkg/utils"

	"go.uber.org/zap"
)

const (
	defaultConfigPath = "/etc/config/config.json"
	defaultTLSPath    = "/etc/tls"
)

// ServiceConfig is the complete ASTRA service configuration.
type ServiceConfig struct {
	astra.ASTRAEngineConfig

	OnlineProcess astra.OnlineProcessConfig `json:"onlineProcess"`
}

func main() {
	configPath := flag.String(
		"config",
		defaultConfigPath,
		"path to the ASTRA runtime JSON configuration",
	)

	defaultPort := os.Getenv("PORT")
	if defaultPort == "" {
		defaultPort = "8080"
	}

	port := flag.String(
		"port",
		defaultPort,
		"HTTP port for the ASTRA service",
	)

	flag.Parse()

	logger, err := l.NewDevelopmentLogger()
	if err != nil {
		panic(err)
	}

	config, err := ParseConfig(*configPath)
	if err != nil {
		panic(err)
	}

	typedConfig, processConfig, err := InitTypedConfig(config, logger)
	if err != nil {
		panic(err)
	}

	relay := astra.NewProxy(logger, typedConfig.TlsConfig)

	engine, err := astra.NewEngine(
		logger,
		utils.NewCommander(),
		typedConfig,
		processConfig,
		relay,
	)
	if err != nil {
		panic(err)
	}

	server, err := astra.NewServer(
		typedConfig.AuthUserIdField,
		engine,
		typedConfig,
		logger,
	)
	if err != nil {
		panic(err)
	}

	logger.Infow(
		"Starting ASTRA execution service",
		"port", *port,
		"playerID", typedConfig.PlayerID,
		"playerCount", typedConfig.PlayerCount,
		"ringBits", typedConfig.RingBits,
		"program", processConfig.ProgramName,
	)

	if err := http.ListenAndServe(":"+*port, server.Handler()); err != nil {
		panic(err)
	}
}

// ParseConfig reads one strict ASTRA service configuration JSON object.
func ParseConfig(path string) (*ServiceConfig, error) {
	data, err := utils.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read ASTRA configuration: %w", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var config ServiceConfig
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("decode ASTRA configuration: %w", err)
	}

	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New(
				"ASTRA configuration must contain exactly one JSON object",
			)
		}

		return nil, fmt.Errorf(
			"invalid trailing ASTRA configuration content: %w",
			err,
		)
	}

	return &config, nil
}

// InitTypedConfig validates ASTRA-specific constraints and creates typed
// durations, service clients, optional Istio mTLS client configuration, and
// native ASTRA process configuration.
func InitTypedConfig(
	config *ServiceConfig,
	logger *zap.SugaredLogger,
) (
	*astra.ASTRAEngineTypedConfig,
	astra.OnlineProcessConfig,// pkg/astra/engine.go
config *ASTRAEngineTypedConfig
	error,
) {
	if config == nil {
		return nil, astra.OnlineProcessConfig{},
			errors.New("ASTRA configuration is nil")
	}

	if config.RingBits != 64 {
		return nil, astra.OnlineProcessConfig{}, fmt.Errorf(
			"ASTRA ringBits must be 64; got %d",
			config.RingBits,
		)
	}

	if config.PlayerCount != astra.OnlinePlayerCount {
		return nil, astra.OnlineProcessConfig{}, fmt.Errorf(
			"ASTRA playerCount must be %d; got %d",
			astra.OnlinePlayerCount,
			config.PlayerCount,
		)
	}

	if config.PlayerID != astra.Player0 &&
		config.PlayerID != astra.Player1 {
		return nil, astra.OnlineProcessConfig{}, fmt.Errorf(
			"ASTRA playerID must be 0 or 1; got %d",
			config.PlayerID,
		)
	}

	if config.RuntimePort < 1 || config.RuntimePort > 65535 {
	return nil, fmt.Errorf(
		"ASTRA runtime port must be between 1 and 65535; got %d",
		config.RuntimePort,
	)
	}
	

	retrySleep, err := time.ParseDuration(config.RetrySleep)
	if err != nil || retrySleep <= 0 {
		return nil, astra.OnlineProcessConfig{}, fmt.Errorf(
			"invalid retrySleep %q",
			config.RetrySleep,
		)
	}

	networkTimeout, err := time.ParseDuration(
		config.NetworkEstablishTimeout,
	)
	if err != nil || networkTimeout <= 0 {
		return nil, astra.OnlineProcessConfig{}, fmt.Errorf(
			"invalid networkEstablishTimeout %q",
			config.NetworkEstablishTimeout,
		)
	}

	stateTimeout, err := time.ParseDuration(config.StateTimeout)
	if err != nil || stateTimeout <= 0 {
		return nil, astra.OnlineProcessConfig{}, fmt.Errorf(
			"invalid stateTimeout %q",
			config.StateTimeout,
		)
	}

	computationTimeout, err := time.ParseDuration(
		config.ComputationTimeout,
	)
	if err != nil || computationTimeout <= 0 {
		return nil, astra.OnlineProcessConfig{}, fmt.Errorf(
			"invalid computationTimeout %q",
			config.ComputationTimeout,
		)
	}

	discoveryTimeout, err := time.ParseDuration(
		config.DiscoveryConfig.ConnectTimeout,
	)
	if err != nil || discoveryTimeout <= 0 {
		return nil, astra.OnlineProcessConfig{}, fmt.Errorf(
			"invalid discovery connectTimeout %q",
			config.DiscoveryConfig.ConnectTimeout,
		)
	}

	if config.DiscoveryConfig.Host == "" ||
		config.DiscoveryConfig.Port == "" {
		return nil, astra.OnlineProcessConfig{},
			errors.New("ASTRA Discovery host and port are required")
	}

	opaClient, err := opa.NewClient(
		logger,
		config.OpaConfig.Endpoint,
		config.OpaConfig.PolicyPackage,
	)
	if err != nil {
		return nil, astra.OnlineProcessConfig{}, err
	}

	amphoraURL := url.URL{
		Scheme: config.AmphoraConfig.Scheme,
		Host:   config.AmphoraConfig.Host,
		Path:   config.AmphoraConfig.Path,
	}
	amphoraClient, err := amphora.NewClient(amphoraURL)
	if err != nil {
		return nil, astra.OnlineProcessConfig{}, err
	}

	castorURL := url.URL{
		Scheme: config.CastorConfig.Scheme,
		Host:   config.CastorConfig.Host,
		Path:   config.CastorConfig.Path,
	}
	castorClient, err := castor.NewClient(castorURL)
	if err != nil {
		return nil, astra.OnlineProcessConfig{}, err
	}

	var tlsConfig *tls.Config
	if config.TlsEnabled {
		tlsConfig, err = utils.CreateTLSConfig(defaultTLSPath)
		if err != nil {
			return nil, astra.OnlineProcessConfig{}, err
		}
	}

	processConfig := config.OnlineProcess
	if processConfig.BinaryPath == "" ||
		processConfig.WorkingDirectory == "" ||
		processConfig.ProgramName == "" ||
		processConfig.InputFilePrefix == "" {
		return nil, astra.OnlineProcessConfig{},
			errors.New("ASTRA onlineProcess configuration is incomplete")
	}

	return &astra.ASTRAEngineTypedConfig{
	ProgramIdentifier:       config.ProgramIdentifier,
	AuthUserIdField:         config.AuthUserIdField,
	RingBits:                config.RingBits,
	RetrySleep:              retrySleep,
	NetworkEstablishTimeout: networkTimeout,
	StateTimeout:            stateTimeout,
	ComputationTimeout:      computationTimeout,
	PlayerID:                config.PlayerID,
	PlayerCount:             config.PlayerCount,
	RuntimePort:             config.RuntimePort,
	FrontendURL:             config.FrontendURL,
	PrepFolder:              config.PrepFolder,
	OpaClient:               opaClient,
	AmphoraClient:           amphoraClient,
	CastorClient:            castorClient,
	DiscoveryConfig: types.DiscoveryClientTypedConfig{
		Host:           config.DiscoveryConfig.Host,
		Port:           config.DiscoveryConfig.Port,
		ConnectTimeout: discoveryTimeout,
	},
	TlsConfig: tlsConfig,
}, processConfig, nil
}
