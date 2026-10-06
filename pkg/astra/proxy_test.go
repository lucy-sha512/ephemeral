package astra

import (
	"context"
	"io"
	"net"
	"testing"
)

func TestProxyForwardsTCPStream(t *testing.T) {
	remoteListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("create remote listener: %v", err)
	}
	defer remoteListener.Close()

	go func() {
		connection, err := remoteListener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()

		_, _ = io.Copy(connection, connection)
	}()

	remoteAddress := remoteListener.Addr().(*net.TCPAddr)

	proxy := NewProxy(nil, nil)
	defer proxy.Stop()

	err = proxy.Run(
		context.Background(),
		[]PeerRoute{
			{
				RemotePlayerID: Player0,
				RemoteHost:     "127.0.0.1",
				RemotePort:     int32(remoteAddress.Port),
				LocalPort:      0,
			},
		},
	)
	if err != nil {
		t.Fatalf("run ASTRA relay: %v", err)
	}

	address, exists := proxy.ListenerAddress(0)
	if !exists {
		t.Fatal("ASTRA relay listener was not registered")
	}

	connection, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatalf("connect to ASTRA relay: %v", err)
	}
	defer connection.Close()

	message := []byte("astra-relay-test")
	if _, err := connection.Write(message); err != nil {
		t.Fatalf("write through ASTRA relay: %v", err)
	}

	response := make([]byte, len(message))
	if _, err := io.ReadFull(connection, response); err != nil {
		t.Fatalf("read through ASTRA relay: %v", err)
	}

	if string(response) != string(message) {
		t.Fatalf(
			"relay response = %q; want %q",
			string(response),
			string(message),
		)
	}
}

func TestProxyRejectsDuplicateLocalPorts(t *testing.T) {
	proxy := NewProxy(nil, nil)

	err := proxy.Run(
		context.Background(),
		[]PeerRoute{
			{
				RemotePlayerID: Player0,
				RemoteHost:     "player0.example.org",
				RemotePort:     30000,
				LocalPort:      Player0Port,
			},
			{
				RemotePlayerID: Player0,
				RemoteHost:     "player0.example.org",
				RemotePort:     30001,
				LocalPort:      Player0Port,
			},
		},
	)

	if err == nil {
		t.Fatal("expected duplicate local-port validation error")
	}
}
