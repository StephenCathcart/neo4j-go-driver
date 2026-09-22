//go:build unix

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

package bolt

import (
	"crypto/tls"
	"errors"
	"net"
	"syscall"
)

// peerAliveSocket answers from the socket itself, which costs one syscall and
// never waits. Only a socket with something waiting on it needs classifying,
// and that is the rare case.
func (s *socketConnection) peerAliveSocket() bool {
	underlying := s.conn
	if tlsConn, ok := underlying.(*tls.Conn); ok {
		underlying = tlsConn.NetConn()
	}
	switch n, err := peekSocket(underlying); {
	case errors.Is(err, syscall.EWOULDBLOCK):
		return true // open and quiet, the common case
	case err != nil:
		return false
	case n == 0:
		return false // the peer closed
	}
	// Something is waiting and the raw socket cannot say what: a NOOP chunk,
	// or under TLS a close_notify or a post-handshake record. Only the stream
	// can tell them apart, and reading through it consumes nothing.
	return s.peerAliveByDeadline()
}

// peekSocket reports how many bytes are waiting on the socket without taking
// them out of it. 0 means the peer closed; EWOULDBLOCK means nothing is
// waiting. MSG_DONTWAIT is deliberately not passed: the runtime already puts
// every socket in non-blocking mode, and the flag is missing from the syscall
// package on some platforms.
func peekSocket(conn net.Conn) (int, error) {
	syscallConn, ok := conn.(syscall.Conn)
	if !ok {
		return 0, errors.New("connection has no raw socket")
	}
	rawConn, err := syscallConn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var n int
	var readErr error
	if ctrlErr := rawConn.Read(func(fd uintptr) bool {
		var b [1]byte
		n, _, readErr = syscall.Recvfrom(int(fd), b[:], syscall.MSG_PEEK)
		return true // one attempt; returning false would wait for readiness
	}); ctrlErr != nil {
		return 0, ctrlErr
	}
	if readErr != nil {
		return 0, readErr
	}
	return n, nil
}
