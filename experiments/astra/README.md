# ASTRA configuration checkpoint

The experiment loads ASTRA JSON configuration, validates the current online
runtime settings, converts timing strings, and constructs OPA, Amphora, and
Castor clients. It exits after initialization; it does not construct an engine,
start an HTTP server, contact those services, or execute MP-SPDZ.

## Implemented

- `pkg/types/types.go`: raw `ASTRAEngineConfig` and converted
  `ASTRAEngineTypedConfig`, reusing existing service configuration and client types.
- `cmd/astra-config/main.go`: strict JSON parsing, configuration validation,
  environment overrides, duration conversion, client construction, and optional
  TLS configuration loading.
- `cmd/astra-config/main_test.go`: checks the returned configuration, timing
  values, client endpoints, and disabled TLS using the local JSON fixture.
- `config.json` and `configmap.yaml`: equivalent experimental configurations.
  The ConfigMap is a fixture, not an applied deployment or chart override.

## Run

From the repository root:

```sh
CGO_ENABLED=0 GOCACHE=/tmp/ephemeral-astra-go-cache go test ./pkg/types ./cmd/astra-config -run '^TestInitASTRATypedConfig$' -count=1 -v

CGO_ENABLED=0 GOCACHE=/tmp/ephemeral-astra-go-cache go run ./cmd/astra-config -config ./experiments/astra/config.json
```

CGO is disabled because the development environment has no C compiler.
The test checks initialization only; it does not validate a Kubernetes mount,
service connectivity, preprocessing compatibility, or MPC execution.

## Scope and assumptions

The initial experiment restricts the arithmetic ring to 64 bits. This is an
experiment restriction, not a claim that ASTRA supports only that width.

MP-SPDZ v0.4.3 uses two online evaluator processes for its semi-honest ASTRA
implementation: online process IDs 0 and 1 correspond to the paper's evaluators
P1 and P2. Preprocessing uses three parties. Programs requiring revealed values
to flow back to preprocessing require both phases to run concurrently; the
initial stored-preprocessing experiment must select a program that can run with
preprocessing completed in advance.

Compatible preprocessing in Castor and compatible input shares in Amphora remain
assumptions. Their formats and delivery paths are not implemented or tested here.
Service addresses are placeholders and TLS is disabled in both fixtures.

## Resume point

The target is an ASTRA version of Carbyne Stack that can build and run without
the SPDZ implementation. Common infrastructure may be reused after checking its
dependencies; ASTRA must not depend on declarations in `spdz.go`.

The following changes have been discussed but are not implemented in this commit:

1. Define ASTRA-owned runtime paths, program name, and timeout constants.
2. Move the protocol-neutral `MPCEngine` interface out of `spdz.go` into a common
   file, retaining exactly one declaration in the package.
3. Add `ASTRAEngine`, its constructor, and construction tests, then connect the
   constructor to the experimental startup command.
4. Trace and adapt the wrapper, execution context, feeder, and proxy. Existing
   `CtxConfig.Spdz`, SPDZ-typed constructors, and `SPDZPacker` cannot serve as
   independent ASTRA implementations.
5. Define ASTRA preprocessing preparation and streaming from the actual runtime
   contract. Do not copy `castor.TupleType`, `TupleStreamerFactory`, or the
   SPDZ-protocol-keyed path map into ASTRA without replacing their SPDZ assumptions.

Application input/output ports must be verified against the ASTRA program and
feeder. The existing SPDZ base port 10000 is not an ASTRA protocol requirement
and is separate from MPC peer communication ports.
