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
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"testing"
	"time"
)

func livenessTcpPair(t testing.TB) (client net.Conn, server net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() { c, _ := ln.Accept(); accepted <- c }()
	client, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return client, <-accepted
}

func livenessTlsPair(t testing.TB) (client *tls.Conn, server *tls.Conn) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		MinVersion:   tls.VersionTLS13,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan *tls.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			accepted <- nil
			return
		}
		server := c.(*tls.Conn)
		_ = server.Handshake()
		_, _ = server.Write([]byte("hi"))
		accepted <- server
	}()
	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	client = tls.Client(raw, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13})
	if err := client.Handshake(); err != nil {
		t.Fatal(err)
	}
	server = <-accepted
	if server == nil {
		t.Fatal("handshake never completed server side")
	}
	// Draining the greeting also absorbs the post-handshake records, leaving
	// the client idle at the application layer.
	var greeting [2]byte
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(client, greeting[:]); err != nil {
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Time{})
	return client, server
}

func awaitDead(t *testing.T, s *socketConnection) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !s.PeerAlive() {
			return
		}
	}
	t.Fatal("never noticed the peer closing")
}

func TestPeerAliveOnOpenConnection(t *testing.T) {
	client, server := livenessTcpPair(t)
	defer client.Close()
	defer server.Close()
	conn := bufferedConnection(client, DefaultReadBufferSize).(*socketConnection)
	for i := 0; i < 100; i++ {
		if !conn.PeerAlive() {
			t.Fatalf("probe %d called an open connection dead", i)
		}
	}
}

func TestPeerAliveOnClosedConnection(t *testing.T) {
	client, server := livenessTcpPair(t)
	defer client.Close()
	conn := bufferedConnection(client, DefaultReadBufferSize).(*socketConnection)
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	awaitDead(t, conn)
}

func TestPeerAliveLeavesTheStreamIntact(t *testing.T) {
	client, server := livenessTcpPair(t)
	defer client.Close()
	defer server.Close()
	conn := bufferedConnection(client, DefaultReadBufferSize).(*socketConnection)

	if !conn.PeerAlive() {
		t.Fatal("open connection reported dead")
	}
	if _, err := server.Write([]byte("PROTOCOLBYTES")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	_ = conn.PeerAlive()

	got := make([]byte, len("PROTOCOLBYTES"))
	if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "PROTOCOLBYTES" {
		t.Fatalf("stream damaged by the probe: got %q", got)
	}
}

func TestReadAfterRepeatedProbes(t *testing.T) {
	client, server := livenessTcpPair(t)
	defer client.Close()
	defer server.Close()
	conn := bufferedConnection(client, DefaultReadBufferSize).(*socketConnection)
	for i := 0; i < 20; i++ {
		if !conn.PeerAlive() {
			t.Fatal("open connection reported dead")
		}
	}
	if _, err := server.Write([]byte("AFTER")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 5)
	if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "AFTER" {
		t.Fatalf("got %q", got)
	}
}

// Discarding a connection over a NOOP chunk would cost a reconnect for nothing.
func TestPendingNoopIsNotDeath(t *testing.T) {
	client, server := livenessTcpPair(t)
	defer client.Close()
	defer server.Close()
	conn := bufferedConnection(client, DefaultReadBufferSize).(*socketConnection)

	if _, err := server.Write([]byte{0x00, 0x00}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if !conn.PeerAlive() {
		t.Fatal("a waiting NOOP chunk was treated as a dead connection")
	}

	go func() {
		_, _ = server.Write([]byte{0x00, 0x02, 0xCA, 0xFE, 0x00, 0x00})
	}()
	_, message, err := dechunkMessage(context.Background(), conn, make([]byte, 2), -1)
	if err != nil {
		t.Fatalf("dechunk after probe: %v", err)
	}
	if len(message) != 2 || message[0] != 0xCA || message[1] != 0xFE {
		t.Fatalf("message damaged by the probe: % x", message)
	}
}

// The server sends a TLS close_notify before the FIN, so a closed TLS
// connection arrives as an encrypted record rather than an end of stream.
func TestPeerAliveOverTls(ot *testing.T) {
	ot.Run("open", func(t *testing.T) {
		client, server := livenessTlsPair(t)
		defer client.Close()
		defer server.Close()
		conn := bufferedConnection(client, DefaultReadBufferSize).(*socketConnection)
		for i := 0; i < 50; i++ {
			if !conn.PeerAlive() {
				t.Fatalf("probe %d called an open TLS connection dead", i)
			}
		}
	})

	ot.Run("closed with close_notify", func(t *testing.T) {
		client, server := livenessTlsPair(t)
		defer client.Close()
		conn := bufferedConnection(client, DefaultReadBufferSize).(*socketConnection)
		_ = server.Close()
		awaitDead(t, conn)
	})

	ot.Run("closed without close_notify", func(t *testing.T) {
		client, server := livenessTlsPair(t)
		defer client.Close()
		conn := bufferedConnection(client, DefaultReadBufferSize).(*socketConnection)
		_ = server.NetConn().Close()
		awaitDead(t, conn)
	})
}

// The deadline path is the only one available on some platforms, so it is
// exercised directly rather than only where the build picks it.
func TestPeerAliveByDeadline(ot *testing.T) {
	ot.Run("open", func(t *testing.T) {
		client, server := livenessTcpPair(t)
		defer client.Close()
		defer server.Close()
		conn := bufferedConnection(client, DefaultReadBufferSize).(*socketConnection)
		if !conn.peerAliveByDeadline() {
			t.Fatal("open connection reported dead")
		}
	})

	ot.Run("closed", func(t *testing.T) {
		client, server := livenessTcpPair(t)
		defer client.Close()
		conn := bufferedConnection(client, DefaultReadBufferSize).(*socketConnection)
		if err := server.Close(); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if !conn.peerAliveByDeadline() {
				return
			}
		}
		t.Fatal("never noticed the peer closing")
	})

	ot.Run("keeps a waiting byte in the stream", func(t *testing.T) {
		client, server := livenessTcpPair(t)
		defer client.Close()
		defer server.Close()
		conn := bufferedConnection(client, DefaultReadBufferSize).(*socketConnection)
		if _, err := server.Write([]byte("Z")); err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
		if !conn.peerAliveByDeadline() {
			t.Fatal("a waiting byte was treated as a dead connection")
		}
		got := make([]byte, 1)
		if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadFull(conn, got); err != nil {
			t.Fatal(err)
		}
		if got[0] != 'Z' {
			t.Fatalf("probe consumed the byte, got %q", got)
		}
	})
}

func BenchmarkPeerAlive(b *testing.B) {
	client, server := livenessTcpPair(b)
	defer client.Close()
	defer server.Close()
	conn := bufferedConnection(client, DefaultReadBufferSize).(*socketConnection)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !conn.PeerAlive() {
			b.Fatal("expected alive")
		}
	}
}

func BenchmarkPeerAliveTls(b *testing.B) {
	client, server := livenessTlsPair(b)
	defer client.Close()
	defer server.Close()
	conn := bufferedConnection(client, DefaultReadBufferSize).(*socketConnection)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !conn.PeerAlive() {
			b.Fatal("expected alive")
		}
	}
}

// A NOOP chunk arriving over TLS looks the same to the raw socket as a
// close_notify, so the classification must not consume it or misread it.
func TestPendingNoopOverTlsIsNotDeath(t *testing.T) {
	client, server := livenessTlsPair(t)
	defer client.Close()
	defer server.Close()
	conn := bufferedConnection(client, DefaultReadBufferSize).(*socketConnection)

	if _, err := server.Write([]byte{0x00, 0x00}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if !conn.PeerAlive() {
		t.Fatal("a waiting NOOP chunk over TLS was treated as a dead connection")
	}
	got := make([]byte, 2)
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read after probe: %v", err)
	}
	if got[0] != 0x00 || got[1] != 0x00 {
		t.Fatalf("probe damaged the chunk: % x", got)
	}
}
