//go:build windows

// Throwaway measurements for the DRIVERS-575 Windows spike. Not for the PR.

package bolt

import (
	"errors"
	"net"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func BenchmarkSpikeSleep10us(b *testing.B) {
	for i := 0; i < b.N; i++ {
		time.Sleep(10 * time.Microsecond)
	}
}

func BenchmarkSpikeDeadlineRead(b *testing.B) {
	client, _ := tcpPair(&testing.T{})
	c := socket(client)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !c.peerAliveByRead(peerProbeTimeout) {
			b.Fatal("reported closed")
		}
	}
}

func BenchmarkSpikePeekOnly(b *testing.B) {
	client, _ := tcpPair(&testing.T{})
	sc := client.(syscall.Conn)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := peek(sc); !errors.Is(err, errNothingWaiting) {
			b.Fatal(err)
		}
	}
}

// SIO_TCP_INFO alternative: read-only state query, no mode toggle.
const sioTcpInfo uint32 = 0xd8000027

func tcpState(t testing.TB, conn net.Conn, outSize uint32) (state uint32, err error) {
	rc, err := conn.(syscall.Conn).SyscallConn()
	if err != nil {
		return 0, err
	}
	var ioctlErr error
	err = rc.Read(func(fd uintptr) bool {
		var version uint32
		out := make([]byte, outSize)
		var ret uint32
		ioctlErr = syscall.WSAIoctl(syscall.Handle(fd), sioTcpInfo, (*byte)(unsafe.Pointer(&version)), 4, &out[0], outSize, &ret, nil, 0)
		state = *(*uint32)(unsafe.Pointer(&out[0]))
		return true
	})
	if err != nil {
		return 0, err
	}
	return state, ioctlErr
}

func TestSpikeTcpInfo(t *testing.T) {
	for _, size := range []uint32{88, 256} {
		client, server := tcpPair(t)
		s, err := tcpState(t, client, size)
		t.Logf("out %d: open: state=%d err=%v", size, s, err)
		server.Close()
		time.Sleep(50 * time.Millisecond)
		s, err = tcpState(t, client, size)
		t.Logf("out %d: peer closed (FIN): state=%d err=%v", size, s, err)
	}
	client, server := tcpPair(t)
	server.(*net.TCPConn).SetLinger(0)
	server.Close()
	time.Sleep(50 * time.Millisecond)
	s, err := tcpState(t, client, 88)
	t.Logf("peer reset (RST): state=%d err=%v", s, err)
	n, perr := peek(client.(syscall.Conn))
	t.Logf("peek after RST: n=%d err=%v", n, perr)
	client, _ = tcpPair(t)
	client.Close()
	s, err = tcpState(t, client, 88)
	t.Logf("closed locally: state=%d err=%v", s, err)
}

func BenchmarkSpikeTcpInfo(b *testing.B) {
	client, _ := tcpPair(&testing.T{})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if s, err := tcpState(b, client, 88); err != nil || s != 4 {
			b.Fatalf("state=%d err=%v", s, err)
		}
	}
}

// The mode toggle must not disturb Go's overlapped reads: a read deadline still fires.
func TestSpikeDeadlineAfterPeek(t *testing.T) {
	client, server := tcpPair(t)
	c := socket(client)
	for i := 0; i < 3; i++ {
		if !c.peerAlive() {
			t.Fatal("reported closed")
		}
	}
	start := time.Now()
	c.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	_, err := c.reader.Peek(1)
	t.Logf("read with 20ms deadline after peeks: err=%v after %v", err, time.Since(start))
	if err == nil {
		t.Fatal("expected a deadline error")
	}
	c.SetReadDeadline(time.Time{})
	server.Write([]byte("after"))
	assertReads(t, c, "after")
}
