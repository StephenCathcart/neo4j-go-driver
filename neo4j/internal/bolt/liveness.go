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
	"errors"
	"io"
	"os"
	"time"
)

// peerCheckTimeout bounds the classifying read. Below a microsecond the
// deadline expires before the read is attempted and nothing is detected.
const peerCheckTimeout = 10 * time.Microsecond

type peerAliveChecker interface {
	PeerAlive() bool
}

func peerAlive(conn io.ReadWriteCloser) bool {
	c, ok := conn.(peerAliveChecker)
	if !ok {
		return true
	}
	return c.PeerAlive()
}

// PeerAlive reports whether the socket can still be handed out. Bytes waiting
// are not a death: the server sends NOOP chunks and dechunkMessage skips them.
func (s *socketConnection) PeerAlive() bool {
	if s.reader != nil && s.reader.Buffered() > 0 {
		return true
	}
	return s.peerAliveSocket()
}

// peerAliveByDeadline classifies the connection by reading through the stream.
// Peek fills the buffered reader without consuming, so a NOOP chunk stays
// queued for dechunkMessage, and under TLS a close_notify is decoded into EOF
// rather than being mistaken for traffic.
func (s *socketConnection) peerAliveByDeadline() bool {
	if s.reader == nil {
		// Nowhere to park a byte that arrives during the check, so say nothing
		// rather than take the byte out of the stream.
		return true
	}
	if err := s.conn.SetReadDeadline(time.Now().Add(peerCheckTimeout)); err != nil {
		return false
	}
	defer func() { _ = s.conn.SetReadDeadline(time.Time{}) }()

	_, err := s.reader.Peek(1)
	return err == nil || errors.Is(err, os.ErrDeadlineExceeded)
}

func (b *bolt3) IsPeerAlive() bool { return peerAlive(b.conn) }
func (b *bolt4) IsPeerAlive() bool { return peerAlive(b.conn) }
func (b *bolt5) IsPeerAlive() bool { return peerAlive(b.conn) }
func (b *bolt6) IsPeerAlive() bool { return peerAlive(b.conn) }
