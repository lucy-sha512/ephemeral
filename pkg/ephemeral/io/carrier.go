// Copyright (c) 2021-2023 - for information on the respective copyright owner
// see the NOTICE file and/or the repository https://github.com/carbynestack/ephemeral.
//
// SPDX-License-Identifier: Apache-2.0
package io

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/carbynestack/ephemeral/pkg/amphora"
	"go.uber.org/zap"
	"io"
	"io/ioutil"
	"net"
	"sync"
)

const maxSpecificationSize = 64 * 1024

// Result contains the response from SPDZ runtime computation.
type Result struct {
	Response []string `json:"response"`
}

var connectionInfo = "ConnectionInfo"

type ConnectionInfo struct {
	Host string
	Port string
}

// AbstractCarrier is the carriers interface.
type AbstractCarrier interface {
	Connect(context.Context, int32, string, string) error
	Close() error
	Send([]amphora.SecretShare) error
	Read(ResponseConverter, bool) (*Result, error)
}

// Carrier is a TCP client for TCP sockets.
type Carrier struct {
	Dialer     func(ctx context.Context, addr, port string) (net.Conn, error)
	Conn       net.Conn
	Packer     Packer
	connection *ConnectionInfo
	Logger     *zap.SugaredLogger
	mux        sync.Mutex
}

// Connect establishes a TCP connection to a socket on a given host and port.
func (c *Carrier) Connect(ctx context.Context, playerID int32, host string, port string) error {
	c.Logger.Debugf("Connecting to %s:%s", host, port)
	c.mux.Lock()
	defer c.mux.Unlock()
	if c.Conn != nil {
		c.Logger.Debugw("Cancel connection attempt as carrier already has an active connection", connectionInfo, c.connection)
		return nil
	}
	conn, err := c.Dialer(ctx, host, port)
	if err != nil {
		return err
	}
	c.connection = &ConnectionInfo{host, port}
	c.Conn = conn
	_, err = conn.Write(c.buildHeader(playerID))
	if err != nil {
		return err
	}
	// MP-SPDZ sends a length-prefixed domain specification immediately after
	// accepting every external client connection. It must be consumed before
	// input or output frames. The legacy implementation consumed an old
	// player-zero prime header only, leaving this frame in player one's result
	// stream.
	err = c.readSpecification()
	if err != nil {
		return err
	}
	return nil
}

// readSpecification consumes MP-SPDZ's length-prefixed external-client
// specification frame. The specification is sent once per connection and
// describes the field/domain; it is not an MPC program result.
func (c Carrier) readSpecification() error {
	const sizeLength = 4
	lengthData := make([]byte, sizeLength)
	if _, err := io.ReadFull(c.Conn, lengthData); err != nil {
		return fmt.Errorf("reading MP-SPDZ specification length: %w", err)
	}

	size := binary.LittleEndian.Uint32(lengthData)
	if size == 0 || size > maxSpecificationSize {
		return fmt.Errorf("invalid MP-SPDZ specification size: %d", size)
	}

	specification := make([]byte, size)
	if _, err := io.ReadFull(c.Conn, specification); err != nil {
		return fmt.Errorf("reading MP-SPDZ specification: %w", err)
	}
	return nil
}

// Close closes the underlying TCP connection.
func (c *Carrier) Close() error {
	c.Logger.Debugw("Closing connection", connectionInfo, c.connection)
	c.mux.Lock()
	defer c.mux.Unlock()
	var err error
	if c.connection != nil {
		err = c.Conn.Close()
		c.Logger.Debug("Carrier connection closed")
	}
	c.connection = nil
	c.Conn = nil
	return err
}

// Send transmits Amphora secret shares to a TCP socket opened by an MPC runtime.
func (c *Carrier) Send(secret []amphora.SecretShare) error {
	input := []byte{}
	shares := []string{}
	for i := range secret {
		shares = append(shares, secret[i].Data)
	}
	err := c.Packer.Marshal(shares, &input)
	if err != nil {
		return err
	}
	_, err = c.Conn.Write(input)
	if err != nil {
		return err
	}
	c.Logger.Debugw("Secret data written to socket", connectionInfo, c.connection)
	return nil
}

// Returns a new Slice with the header appended
// The header consists of the clientId as string:
// - 1 Long (4 Byte) that contains the length of the string in bytes
// - Then come X Bytes for the String
func (c *Carrier) buildHeader(playerID int32) []byte {
	playerIDString := []byte(fmt.Sprintf("%d", playerID))
	lengthOfString := make([]byte, 4)
	binary.LittleEndian.PutUint32(lengthOfString, uint32(len(playerIDString)))
	return append(lengthOfString, playerIDString...)
}

// Read reads the response from the TCP connection and unmarshals it.
func (c *Carrier) Read(conv ResponseConverter, bulkObjects bool) (*Result, error) {
	resp := []byte{}
	resp, err := ioutil.ReadAll(c.Conn)
	if len(resp) == 0 {
		c.Logger.Errorw("Carrier read closed with empty response", connectionInfo, c.connection)
		return nil, errors.New("empty result from socket")
	}
	if err != nil {
		return nil, err
	}
	out, err := c.Packer.Unmarshal(&resp, conv, bulkObjects)
	if err != nil {
		return nil, err
	}
	return &Result{Response: out}, nil
}
