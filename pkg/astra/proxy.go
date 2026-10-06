// Copyright (c) 2026
//
// SPDX-License-Identifier: Apache-2.0

package astra

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.uber.org/zap"
)

// Proxy provides ASTRA's local TCP relay. Player 1 connects to LocalPort;
// the relay opens an outer connection to Player 0's Istio endpoint and
// forwards the native ASTRA byte stream unchanged.
type Proxy struct {
	logger    *zap.SugaredLogger
	tlsConfig *tls.Config

	mutex     sync.Mutex
	listeners map[int32]net.Listener
	conns     map[net.Conn]struct{}
}

// NewProxy creates an ASTRA-only TCP relay.
func NewProxy(
	logger *zap.SugaredLogger,
	tlsConfig *tls.Config,
) *Proxy {
	if logger == nil {
		logger = zap.NewNop().Sugar()
	}

	return &Proxy{
		logger:    logger,
		tlsConfig: tlsConfig,
		listeners: make(map[int32]net.Listener),
		conns:     make(map[net.Conn]struct{}),
	}
}

// Run binds one local listener for every route. Player 0 supplies no route
// because it directly owns the ASTRA listener on Player0Port.
func (p *Proxy) Run(
	ctx context.Context,
	routes []PeerRoute,
) error {
	if err := validateRoutes(routes); err != nil {
		return err
	}

	for _, route := range routes {
		listener, err := net.Listen(
			"tcp",
			":"+strconv.Itoa(int(route.LocalPort)),
		)
		if err != nil {
			p.Stop()
			return fmt.Errorf(
				"listen on ASTRA local port %d: %w",
				route.LocalPort,
				err,
			)
		}

		p.mutex.Lock()
		p.listeners[route.LocalPort] = listener
		p.mutex.Unlock()

		go p.serve(ctx, listener, route)

		p.logger.Infow(
			"Started ASTRA local relay",
			"localPort", route.LocalPort,
			"remotePlayerID", route.RemotePlayerID,
			"remoteHost", route.RemoteHost,
			"remotePort", route.RemotePort,
		)
	}

	return nil
}

// Stop closes all local listeners and active relay connections.
func (p *Proxy) Stop() {
	p.mutex.Lock()

	listeners := make([]net.Listener, 0, len(p.listeners))
	for _, listener := range p.listeners {
		listeners = append(listeners, listener)
	}
	p.listeners = make(map[int32]net.Listener)

	connections := make([]net.Conn, 0, len(p.conns))
	for connection := range p.conns {
		connections = append(connections, connection)
	}
	p.conns = make(map[net.Conn]struct{})

	p.mutex.Unlock()

	for _, listener := range listeners {
		_ = listener.Close()
	}

	for _, connection := range connections {
		_ = connection.Close()
	}
}

// ListenerAddress returns the bound address for a local relay port.
func (p *Proxy) ListenerAddress(localPort int32) (string, bool) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	listener, exists := p.listeners[localPort]
	if !exists {
		return "", false
	}

	return listener.Addr().String(), true
}

func (p *Proxy) serve(
	ctx context.Context,
	listener net.Listener,
	route PeerRoute,
) {
	for {
		connection, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return
			}

			p.logger.Errorw(
				"ASTRA local relay accept failed",
				"localPort", route.LocalPort,
				"error", err,
			)
			continue
		}

		p.trackConnection(connection)

		go func(connection net.Conn) {
			defer p.untrackConnection(connection)
			defer connection.Close()

			p.forward(ctx, connection, route)
		}(connection)
	}
}

func (p *Proxy) forward(
	ctx context.Context,
	localConnection net.Conn,
	route PeerRoute,
) {
	remoteConnection, err := p.dial(ctx, route)
	if err != nil {
		p.logger.Errorw(
			"ASTRA relay could not connect to remote player",
			"remotePlayerID", route.RemotePlayerID,
			"remoteHost", route.RemoteHost,
			"remotePort", route.RemotePort,
			"error", err,
		)
		return
	}
	defer remoteConnection.Close()

	p.trackConnection(remoteConnection)
	defer p.untrackConnection(remoteConnection)

	copyDone := make(chan struct{}, 2)

	go func() {
		_, _ = io.Copy(remoteConnection, localConnection)
		copyDone <- struct{}{}
	}()

	go func() {
		_, _ = io.Copy(localConnection, remoteConnection)
		copyDone <- struct{}{}
	}()

	<-copyDone
}

func (p *Proxy) dialOnce(
	ctx context.Context,
	route PeerRoute,
) (net.Conn, error) {
	address := net.JoinHostPort(
		route.RemoteHost,
		strconv.Itoa(int(route.RemotePort)),
	)

	if p.tlsConfig == nil {
		dialer := &net.Dialer{}
		return dialer.DialContext(ctx, "tcp", address)
	}

	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{},
		Config:    p.tlsConfig,
	}

	return dialer.DialContext(ctx, "tcp", address)
}

func (p *Proxy) trackConnection(connection net.Conn) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	p.conns[connection] = struct{}{}
}

func (p *Proxy) untrackConnection(connection net.Conn) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	delete(p.conns, connection)
}

func validateRoutes(routes []PeerRoute) error {
	localPorts := make(map[int32]struct{}, len(routes))

	for _, route := range routes {
		if route.RemotePlayerID != Player0 {
			return fmt.Errorf(
				"ASTRA relay only supports Player 0 as remote peer; got %d",
				route.RemotePlayerID,
			)
		}

		if strings.TrimSpace(route.RemoteHost) == "" {
			return errors.New("ASTRA relay remote host is empty")
		}

		if route.RemotePort < 1 || route.RemotePort > 65535 {
			return fmt.Errorf(
				"ASTRA relay remote port is invalid: %d",
				route.RemotePort,
			)
		}

		if route.LocalPort < 0 || route.LocalPort > 65535 {
			return fmt.Errorf(
				"ASTRA relay local port is invalid: %d",
				route.LocalPort,
			)
		}

		if _, exists := localPorts[route.LocalPort]; exists {
			return fmt.Errorf(
				"ASTRA relay local port %d is duplicated",
				route.LocalPort,
			)
		}

		localPorts[route.LocalPort] = struct{}{}
	}

	return nil
}

// dial retries only refused connections, retaining the actual successful
// peer connection instead of opening a disposable readiness probe.
func (p *Proxy) dial(ctx context.Context, route PeerRoute) (net.Conn, error) {
	for {
		connection, err := p.dialOnce(ctx, route)
		if err == nil {
			return connection, nil
		}
		if !errors.Is(err, syscall.ECONNREFUSED) {
			return nil, err
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
