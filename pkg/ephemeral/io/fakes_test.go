// Copyright (c) 2021 - for information on the respective copyright owner
// see the NOTICE file and/or the repository https://github.com/carbynestack/ephemeral.
//
// SPDX-License-Identifier: Apache-2.0
package io

import (
	"errors"
	"io"
	"net"
	"time"
)

type FakeNetConnection struct {
	Closed   bool
	ReadData []byte
}

func (fnc *FakeNetConnection) Read(b []byte) (n int, err error) {
	if len(fnc.ReadData) == 0 {
		return 0, io.EOF
	}
	n = copy(b, fnc.ReadData)
	fnc.ReadData = fnc.ReadData[n:]
	return n, nil
}
func (fnc *FakeNetConnection) Write(b []byte) (n int, err error) {
	return len(b), nil
}
func (fnc *FakeNetConnection) Close() error {
	fnc.Closed = true
	return nil
}
func (fnc *FakeNetConnection) LocalAddr() net.Addr {
	return &FakeNetAddr{}
}
func (fnc *FakeNetConnection) RemoteAddr() net.Addr {
	return &FakeNetAddr{}
}
func (fnc *FakeNetConnection) SetDeadline(t time.Time) error {
	return nil
}
func (fnc *FakeNetConnection) SetReadDeadline(t time.Time) error {
	return nil
}
func (fnc *FakeNetConnection) SetWriteDeadline(t time.Time) error {
	return nil
}

type FakeNetAddr struct {
}

func (fna *FakeNetAddr) Network() string {
	return ""
}
func (fna *FakeNetAddr) String() string {
	return ""
}

type FakePacker struct {
	MarshalResponse   []byte
	UnmarshalResponse []string
}

func (fp *FakePacker) Marshal(input []string, out *[]byte) error {
	*out = fp.MarshalResponse
	return nil
}
func (fp *FakePacker) Unmarshal(in *[]byte, r ResponseConverter, bulk bool) ([]string, error) {
	return fp.UnmarshalResponse, nil
}

type FakeBrokenPacker struct {
	Response []byte
}

func (fp *FakeBrokenPacker) Marshal(input []string, out *[]byte) error {
	return errors.New("some error")
}
func (fp *FakeBrokenPacker) Unmarshal(in *[]byte, r ResponseConverter, bulk bool) ([]string, error) {
	return nil, errors.New("some error")
}
