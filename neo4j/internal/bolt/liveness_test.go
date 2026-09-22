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
	"io"
	"net"
	"testing"
	"time"

	. "github.com/neo4j/neo4j-go-driver/v6/neo4j/internal/testutil"
)

func tcpPair(t *testing.T) (client, server net.Conn) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	AssertNoError(t, err)
	defer listener.Close()
	client, err = net.Dial("tcp", listener.Addr().String())
	AssertNoError(t, err)
	server, err = listener.Accept()
	AssertNoError(t, err)
	t.Cleanup(func() {
		client.Close()
		server.Close()
	})
	return client, server
}

func tlsPair(t *testing.T) (client, server *tls.Conn) {
	t.Helper()
	cert, err := tls.LoadX509KeyPair("../../auth/testdata/test_cert.pem", "../../auth/testdata/test_key.pem")
	AssertNoError(t, err)
	rawClient, rawServer := tcpPair(t)
	server = tls.Server(rawServer, &tls.Config{Certificates: []tls.Certificate{cert}})
	client = tls.Client(rawClient, &tls.Config{InsecureSkipVerify: true})
	handshake := make(chan error, 1)
	go func() { handshake <- server.Handshake() }()
	AssertNoError(t, client.Handshake())
	AssertNoError(t, <-handshake)
	return client, server
}

func socket(conn net.Conn) *socketConnection {
	return bufferedConnection(conn, DefaultReadBufferSize).(*socketConnection)
}

// awaitFalse polls: a close takes a moment to arrive.
func awaitFalse(t *testing.T, probe func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for probe() {
		if time.Now().After(deadline) {
			t.Fatal("peer still reported alive")
		}
		time.Sleep(time.Millisecond)
	}
}

func awaitBuffered(t *testing.T, c *socketConnection, probe func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for c.reader.Buffered() == 0 {
		AssertTrue(t, probe())
		if time.Now().After(deadline) {
			t.Fatal("waiting bytes never reached the read buffer")
		}
		time.Sleep(time.Millisecond)
	}
	AssertTrue(t, probe())
}

func assertReads(t *testing.T, c *socketConnection, want string) {
	t.Helper()
	got := make([]byte, len(want))
	read := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(c, got)
		read <- err
	}()
	select {
	case err := <-read:
		AssertNoError(t, err)
		AssertStringEqual(t, string(got), want)
	case <-time.After(2 * time.Second):
		t.Fatal("read blocked")
	}
}

func TestPeerAlive(outer *testing.T) {
	outer.Run("open and quiet", func(t *testing.T) {
		client, _ := tcpPair(t)
		start := time.Now()
		AssertTrue(t, socket(client).peerAlive())
		AssertTrue(t, time.Since(start) < time.Second)
	})

	outer.Run("closed by peer", func(t *testing.T) {
		client, server := tcpPair(t)
		AssertNoError(t, server.Close())
		awaitFalse(t, socket(client).peerAlive)
	})

	outer.Run("closed locally", func(t *testing.T) {
		client, _ := tcpPair(t)
		AssertNoError(t, client.Close())
		AssertFalse(t, socket(client).peerAlive())
	})

	outer.Run("waiting bytes stay in the stream", func(t *testing.T) {
		client, server := tcpPair(t)
		c := socket(client)
		AssertWriteSucceeds(t, server, []byte{0, 0})
		awaitBuffered(t, c, c.peerAlive)
		assertReads(t, c, "\x00\x00")
	})
}

func TestPeerAliveOverTls(outer *testing.T) {
	outer.Run("open and quiet", func(t *testing.T) {
		client, server := tlsPair(t)
		c := socket(client)
		AssertTrue(t, c.peerAlive())
		AssertWriteSucceeds(t, server, []byte("still usable"))
		assertReads(t, c, "still usable")
	})

	outer.Run("closed with close_notify", func(t *testing.T) {
		client, server := tlsPair(t)
		AssertNoError(t, server.Close())
		awaitFalse(t, socket(client).peerAlive)
	})

	outer.Run("closed without close_notify", func(t *testing.T) {
		client, server := tlsPair(t)
		AssertNoError(t, server.NetConn().Close())
		awaitFalse(t, socket(client).peerAlive)
	})

	outer.Run("waiting bytes stay in the stream", func(t *testing.T) {
		client, server := tlsPair(t)
		c := socket(client)
		AssertWriteSucceeds(t, server, []byte("record"))
		awaitBuffered(t, c, c.peerAlive)
		assertReads(t, c, "record")
	})
}

func TestPeerAliveByRead(outer *testing.T) {
	outer.Run("open and quiet leaves the connection usable", func(t *testing.T) {
		client, server := tcpPair(t)
		c := socket(client)
		AssertTrue(t, c.peerAliveByRead(peerProbeTimeout))
		AssertWriteSucceeds(t, server, []byte("after"))
		assertReads(t, c, "after")
	})

	outer.Run("closed by peer", func(t *testing.T) {
		client, server := tcpPair(t)
		c := socket(client)
		AssertNoError(t, server.Close())
		awaitFalse(t, func() bool { return c.peerAliveByRead(peerProbeTimeout) })
	})

	outer.Run("closed locally", func(t *testing.T) {
		client, _ := tcpPair(t)
		AssertNoError(t, client.Close())
		AssertFalse(t, socket(client).peerAliveByRead(peerProbeTimeout))
	})

	outer.Run("waiting bytes stay in the stream", func(t *testing.T) {
		client, server := tcpPair(t)
		c := socket(client)
		AssertWriteSucceeds(t, server, []byte{0, 0})
		awaitBuffered(t, c, func() bool { return c.peerAliveByRead(peerProbeTimeout) })
		assertReads(t, c, "\x00\x00")
	})

	outer.Run("skipped without a read buffer", func(t *testing.T) {
		client, server := tcpPair(t)
		c := bufferedConnection(client, 0).(*socketConnection)
		AssertNoError(t, server.Close())
		AssertTrue(t, c.peerAliveByRead(peerProbeTimeout))
	})
}

func TestIsPeerAlive(t *testing.T) {
	client, server := tcpPair(t)
	conn := NewBolt6("server", bufferedConnection(client, DefaultReadBufferSize), nil, logger, nil)
	AssertTrue(t, conn.IsPeerAlive())
	AssertNoError(t, server.Close())
	awaitFalse(t, conn.IsPeerAlive)
}

func TestPeerAliveWithoutRawSocket(outer *testing.T) {
	outer.Run("connection not wrapped", func(t *testing.T) {
		client, _ := net.Pipe()
		defer client.Close()
		AssertTrue(t, peerAlive(client))
	})

	outer.Run("wrapped connection without a file descriptor", func(t *testing.T) {
		client, _ := net.Pipe()
		defer client.Close()
		AssertTrue(t, socket(client).peerAlive())
	})
}
