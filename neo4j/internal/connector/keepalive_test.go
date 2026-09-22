//go:build darwin || linux

/*
 * Copyright (c) "Neo4j"
 * Neo4j Sweden AB [https://neo4j.com]
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     https://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package connector

import (
	"context"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/neo4j/neo4j-go-driver/v6/neo4j/config"
)

type discardLog struct{}

func (discardLog) Error(string, string, error)           {}
func (discardLog) Warnf(string, string, string, ...any)  {}
func (discardLog) Infof(string, string, string, ...any)  {}
func (discardLog) Debugf(string, string, string, ...any) {}

func socketOption(t *testing.T, conn net.Conn, level, option int) int {
	t.Helper()
	raw, err := conn.(*net.TCPConn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var value int
	var optErr error
	if ctrlErr := raw.Control(func(fd uintptr) {
		value, optErr = syscall.GetsockoptInt(int(fd), level, option)
	}); ctrlErr != nil {
		t.Fatal(ctrlErr)
	}
	if optErr != nil {
		t.Fatal(optErr)
	}
	return value
}

// The timings are logged rather than asserted: they come from net's own
// defaults and are free to move between Go releases.
func TestKeepAliveFollowsSocketKeepaliveConfig(ot *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		ot.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
		}
	}()

	for _, testCase := range []struct {
		name      string
		keepalive bool
	}{
		{"enabled", true},
		{"disabled", false},
	} {
		ot.Run(testCase.name, func(t *testing.T) {
			connector := Connector{
				Config:  &config.Config{SocketKeepalive: testCase.keepalive},
				Network: "tcp",
				Log:     discardLog{},
			}
			conn, err := connector.createConnection(context.Background(), listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()

			enabled := socketOption(t, conn, syscall.SOL_SOCKET, syscall.SO_KEEPALIVE) != 0
			if enabled != testCase.keepalive {
				t.Fatalf("SO_KEEPALIVE is %v, want %v", enabled, testCase.keepalive)
			}
			idle := socketOption(t, conn, syscall.IPPROTO_TCP, keepAliveIdleOption)
			interval := socketOption(t, conn, syscall.IPPROTO_TCP, syscall.TCP_KEEPINTVL)
			count := socketOption(t, conn, syscall.IPPROTO_TCP, syscall.TCP_KEEPCNT)
			t.Logf("idle=%ds interval=%ds count=%d, so a dead peer is noticed after about %v idle",
				idle, interval, count,
				time.Duration(idle)*time.Second+time.Duration(interval*count)*time.Second)
		})
	}
}
