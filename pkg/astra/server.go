// Copyright (c) 2026
//
// SPDX-License-Identifier: Apache-2.0

package astra

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"strings"

	"github.com/carbynestack/ephemeral/pkg/types"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

type requestContextKey string

const astraContextKey requestContextKey = "astra-context"

// Server exposes the ASTRA computation activation endpoint.
type Server struct {
	authUserIDField string
	engine          *Engine
	config          ASTRAEngineTypedConfig
	logger          *zap.SugaredLogger
}

// NewServer creates an ASTRA-only HTTP service.
func NewServer(
	authUserIDField string,
	engine *Engine,
	config ASTRAEngineTypedConfig,
	logger *zap.SugaredLogger,
) (*Server, error) {
	if engine == nil {
		return nil, errors.New("ASTRA engine is nil")
	}
	if config == nil {
		return nil, errors.New("ASTRA typed configuration is nil")
	}
	if logger == nil {
		logger = zap.NewNop().Sugar()
	}

	return &Server{
		authUserIDField: authUserIDField,
		engine:          engine,
		config:          config,
		logger:          logger,
	}, nil
}

// Handler returns the ASTRA HTTP filter chain.
func (s *Server) Handler() http.Handler {
	activationHandler := http.HandlerFunc(s.ActivationHandler)

	return s.MethodFilter(s.RequestFilter(activationHandler))
}

// MethodFilter accepts only JSON POST computation requests.
func (s *Server) MethodFilter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.Method != http.MethodPost {
			writeError(
				writer,
				http.StatusMethodNotAllowed,
				"POST requests must trigger an ASTRA computation",
			)
			return
		}

		if !hasJSONContentType(request) {
			writeError(
				writer,
				http.StatusUnsupportedMediaType,
				"application/json content type must be provided",
			)
			return
		}

		next.ServeHTTP(writer, request)
	})
}

// RequestFilter authenticates the caller and builds one ASTRA request context.
func (s *Server) RequestFilter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		authorizedUser, err := getUserFromAuthHeader(
			request.Header.Get("Authorization"),
			s.authUserIDField,
		)
		if err != nil {
			s.logger.Errorw(
				"Unauthorized ASTRA request",
				"error", err,
			)
			writeError(writer, http.StatusUnauthorized, "unauthorized request")
			return
		}

		if request.Body == nil {
			writeError(writer, http.StatusBadRequest, "request body is nil")
			return
		}
		defer request.Body.Close()

		var activation ASTRAActivation
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()

		if err := decoder.Decode(&activation); err != nil {
			writeError(
				writer,
				http.StatusBadRequest,
				fmt.Sprintf("error decoding ASTRA request body: %s", err),
			)
			return
		}

		var extra interface{}
		if err := decoder.Decode(&extra); err != io.EOF {
			writeError(
				writer,
				http.StatusBadRequest,
				"ASTRA request body must contain exactly one JSON object",
			)
			return
		}

		if _, err := uuid.Parse(activation.GameID); err != nil {
			writeError(
				writer,
				http.StatusBadRequest,
				fmt.Sprintf(
					"gameID %q is not a valid UUID",
					activation.GameID,
				),
			)
			return
		}

		astraContext := &ASTRACtxConfig{
			AuthorizedUser: authorizedUser,
			Activation:     &activation,
			Config:         s.config,
		}

		ctx := context.WithValue(
			request.Context(),
			astraContextKey,
			astraContext,
		)

		next.ServeHTTP(writer, request.WithContext(ctx))
	})
}

// ActivationHandler starts Discovery participation and waits for the ASTRA
// engine result, Discovery error, execution error, or timeout.
func (s *Server) ActivationHandler(
	writer http.ResponseWriter,
	request *http.Request,
) {
	astraContext, ok := request.Context().Value(
		astraContextKey,
	).(*ASTRACtxConfig)
	if !ok || astraContext == nil {
		writeError(
			writer,
			http.StatusBadRequest,
			"ASTRA request context is missing",
		)
		return
	}

	executionTimeout := s.config.StateTimeout*3 +
		s.config.ComputationTimeout

	executionContext, cancel := context.WithTimeout(
		request.Context(),
		executionTimeout,
	)
	defer cancel()

	astraContext.Context = executionContext

	pod, err := os.Hostname()
	if err != nil {
		s.logger.Errorw(
			"Retrieve ASTRA pod name",
			"gameID", astraContext.Activation.GameID,
			"error", err,
		)
		writeError(
			writer,
			http.StatusInternalServerError,
			"unable to retrieve ASTRA pod name",
		)
		return
	}

	discoveryErrCh := make(chan error, 1)
	executionErrCh := make(chan error, 1)
	responseCh := make(chan []byte, 1)

	astraContext.ErrCh = discoveryErrCh

	wrapper := NewWrapper(
		astraContext,
		responseCh,
		executionErrCh,
		s.logger,
		s.engine.Activate,
	)

	player, err := NewPlayerWithIO(
		astraContext,
		pod,
		wrapper,
		s.logger,
	)
	if err != nil {
		s.logger.Errorw(
			"Create ASTRA Discovery player",
			"gameID", astraContext.Activation.GameID,
			"error", err,
		)
		writeError(
			writer,
			http.StatusInternalServerError,
			"unable to initialize ASTRA Discovery player",
		)
		return
	}

	player.Start()

	select {
	case response := <-responseCh:
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(response)

	case err := <-discoveryErrCh:
		s.logger.Errorw(
			"ASTRA Discovery error",
			"gameID", astraContext.Activation.GameID,
			"error", err,
		)
		writeError(
			writer,
			http.StatusInternalServerError,
			fmt.Sprintf("error while talking to Discovery: %s", err),
		)

	case err := <-executionErrCh:
		s.logger.Errorw(
			"ASTRA execution error",
			"gameID", astraContext.Activation.GameID,
			"error", err,
		)
		writeError(
			writer,
			http.StatusInternalServerError,
			fmt.Sprintf("error during ASTRA execution: %s", err),
		)

	case <-executionContext.Done():
		s.logger.Errorw(
			"ASTRA activation timed out",
			"gameID", astraContext.Activation.GameID,
			"error", executionContext.Err(),
			"fsmHistory", player.History(),
		)
		writeError(
			writer,
			http.StatusInternalServerError,
			"timeout during ASTRA activation procedure",
		)
	}
}

func hasJSONContentType(request *http.Request) bool {
	contentType := request.Header.Get("Content-Type")
	if contentType == "" {
		return false
	}

	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}

	return mediaType == "application/json"
}

func getUserFromAuthHeader(
	header string,
	idField string,
) (string, error) {
	token := strings.TrimPrefix(header, "Bearer ")
	if token == "" || token == header {
		return "", errors.New("Bearer token is missing")
	}

	jwtParts := strings.Split(token, ".")
	if len(jwtParts) != 3 {
		return "", errors.New("JWT must contain three sections")
	}

	claims, err := base64.URLEncoding.WithPadding(
		base64.NoPadding,
	).DecodeString(jwtParts[1])
	if err != nil {
		return "", fmt.Errorf("decode JWT claims: %w", err)
	}

	var claimsMap map[string]interface{}
	if err := json.Unmarshal(claims, &claimsMap); err != nil {
		return "", fmt.Errorf("decode JWT claims JSON: %w", err)
	}

	path := strings.Split(idField, ".")
	for _, part := range path[:len(path)-1] {
		nestedClaims, ok := claimsMap[part].(map[string]interface{})
		if !ok {
			return "", fmt.Errorf(
				"JWT field %q is not present",
				idField,
			)
		}
		claimsMap = nestedClaims
	}

	userID, ok := claimsMap[path[len(path)-1]].(string)
	if !ok || userID == "" {
		return "", fmt.Errorf(
			"JWT field %q is not a non-empty string",
			idField,
		)
	}

	return userID, nil
}

func writeError(
	writer http.ResponseWriter,
	status int,
	message string,
) {
	writer.WriteHeader(status)
	_, _ = writer.Write([]byte(message))
}
