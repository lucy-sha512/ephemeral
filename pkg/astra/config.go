package astra

import (
	"crypto/tls"
	"time"

	"github.com/carbynestack/ephemeral/pkg/amphora"
	"github.com/carbynestack/ephemeral/pkg/castor"
	"github.com/carbynestack/ephemeral/pkg/opa"
	"github.com/carbynestack/ephemeral/pkg/types"
)

// ASTRAEngineConfig contains values decoded from runtime JSON.
type ASTRAEngineConfig struct {
	ProgramIdentifier string `json:"programIdentifier"`
	AuthUserIdField   string `json:"authUserIdField"`
	RingBits          int    `json:"ringBits"`

	RetrySleep              string `json:"retrySleep"`
	NetworkEstablishTimeout string `json:"networkEstablishTimeout"`
	StateTimeout            string `json:"stateTimeout"`
	ComputationTimeout      string `json:"computationTimeout"`

	PlayerID    int32  `json:"playerID"`
	PlayerCount int32  `json:"playerCount"`
	RuntimePort int32  `json:"runtimePort"`
	FrontendURL string `json:"frontendURL"`
	PrepFolder  string `json:"prepFolder"`
	TlsEnabled  bool   `json:"tlsEnabled"`

	OpaConfig       types.OpaConfig             `json:"opaConfig"`
	AmphoraConfig   types.AmphoraConfig         `json:"amphoraConfig"`
	CastorConfig    types.CastorConfig          `json:"castorConfig"`
	DiscoveryConfig types.DiscoveryClientConfig `json:"discoveryConfig"`
}

// ASTRAEngineTypedConfig contains parsed values and constructed clients.
type ASTRAEngineTypedConfig struct {
	ProgramIdentifier string
	AuthUserIdField   string
	RingBits          int

	RetrySleep              time.Duration
	NetworkEstablishTimeout time.Duration
	StateTimeout            time.Duration
	ComputationTimeout      time.Duration

	PlayerID    int32
	PlayerCount int32
	RuntimePort int32
	FrontendURL string
	PrepFolder  string

	OpaClient       opa.AbstractClient
	AmphoraClient   amphora.AbstractClient
	CastorClient    castor.AbstractClient
	DiscoveryConfig types.DiscoveryClientTypedConfig
	TlsConfig       *tls.Config
}
