package main

import (
	"github.com/carbynestack/ephemeral/pkg/amphora"
	"github.com/carbynestack/ephemeral/pkg/castor"
	"github.com/carbynestack/ephemeral/pkg/opa"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestInitASTRATypedConfig(t *testing.T) {
	t.Setenv("EPHEMERAL_PROGRAM_IDENTIFIER", "ephemeral-astra-experiment")
	t.Setenv("EPHEMERAL_OPA_POLICY_PACKAGE", "carbynestack.def")

	configPath := filepath.Join(
		"..", "..", "experiments", "astra", "config.json",
	)
	raw, err := ParseASTRAConfig(configPath)
	if err != nil {
		t.Fatalf("ParseASTRAConfig failed: %v", err)
	}
	if raw.TlsEnabled {
		t.Fatal("this test expects tlsEnabled: false")
	}

	typed, err := InitASTRATypedConfig(raw, zap.NewNop().Sugar())
	if err != nil {
		t.Fatalf("InitASTRATypedConfig failed: %v", err)
	}
	if typed == nil {
		t.Fatal("initializer returned a nil configuration")
	}

	t.Logf("Returned ASTRATypedConfig:\n%+v", *typed)

	check := func(name string, got, want interface{}) {
		t.Helper()
		if got != want {
			t.Errorf("%s: got %v, want %v", name, got, want)
		} else {
			t.Logf("%s = %v", name, got)
		}
	}

	check("ProgramIdentifier",
		typed.ProgramIdentifier,
		"ephemeral-astra-experiment")
	check("AuthUserIdField", typed.AuthUserIdField, "email")
	check("RingBits", typed.RingBits, 64)
	check("PlayerID", typed.PlayerID, int32(0))
	check("PlayerCount", typed.PlayerCount, int32(2))
	check("FrontendURL", typed.FrontendURL, "player0.example.org")
	check("PrepFolder", typed.PrepFolder, "Player-Data")
	check("Discovery.Host",
		typed.DiscoveryConfig.Host, "discovery.example.org")
	check("Discovery.Port", typed.DiscoveryConfig.Port, "8080")

	check("RetrySleep",
		typed.RetrySleep, 50*time.Millisecond)
	check("NetworkEstablishTimeout",
		typed.NetworkEstablishTimeout, 60*time.Second)
	check("StateTimeout",
		typed.StateTimeout, 60*time.Second)
	check("ComputationTimeout",
		typed.ComputationTimeout, 600*time.Second)
	check("Discovery.ConnectTimeout",
		typed.DiscoveryConfig.ConnectTimeout, 60*time.Second)

	opaClient, ok := typed.OpaClient.(*opa.Client)
	if !ok || opaClient == nil {
		t.Fatalf("expected initialized *opa.Client; got %T", typed.OpaClient)
	}
	check("OPA.URL",
		opaClient.URL.String(), "http://opa.example.org:8081/")
	check("OPA.PolicyPackage",
		opaClient.PolicyPackage, "carbynestack.def")

	amphoraClient, ok := typed.AmphoraClient.(*amphora.Client)
	if !ok || amphoraClient == nil {
		t.Fatalf(
			"expected initialized *amphora.Client; got %T",
			typed.AmphoraClient,
		)
	}
	check("Amphora.URL",
		amphoraClient.URL.String(), "http://amphora.example.org:10000/")

	castorClient, ok := typed.CastorClient.(*castor.Client)
	if !ok || castorClient == nil {
		t.Fatalf(
			"expected initialized *castor.Client; got %T",
			typed.CastorClient,
		)
	}
	check("Castor.URL",
		castorClient.URL.String(), "http://castor.example.org:10100/")

	if typed.TlsConfig != nil {
		t.Error("expected nil TlsConfig because Tls enabled is false")
	}
	t.Logf("TLSConfig = %v", typed.TlsConfig)

}
