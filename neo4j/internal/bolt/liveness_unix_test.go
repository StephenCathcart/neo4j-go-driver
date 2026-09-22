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
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// peekSocket passes no MSG_DONTWAIT, which is only sound because the runtime
// puts every socket in non-blocking mode. If that ever stops being true the
// peek would block a borrow until traffic arrived, so it is worth asserting.
// It also keeps the peek usable on platforms whose syscall package has
// MSG_PEEK but not MSG_DONTWAIT.
func TestPeekDoesNotBlockOnAQuietSocket(t *testing.T) {
	client, server := livenessTcpPair(t)
	defer client.Close()
	defer server.Close()

	start := time.Now()
	n, err := peekSocket(client)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected a would-block error on a quiet socket, got n=%d", n)
	}
	if elapsed > 50*time.Millisecond {
		t.Fatalf("the peek blocked for %v", elapsed)
	}
}

// The peek is chosen over the deadline fallback because a deadline parks the
// goroutine on a timer, so its latency follows the scheduler rather than the
// socket. This guards the choice: on a busy machine the gap is large.
func TestPeekBeatsTheDeadlineFallbackUnderLoad(t *testing.T) {
	client, server := livenessTcpPair(t)
	defer client.Close()
	defer server.Close()
	conn := bufferedConnection(client, DefaultReadBufferSize).(*socketConnection)

	var stop atomic.Bool
	var busy sync.WaitGroup
	for i := 0; i < runtime.NumCPU()*2; i++ {
		busy.Add(1)
		go func() {
			defer busy.Done()
			m := make(map[int][]byte, 128)
			n := 0
			for !stop.Load() {
				n++
				m[n%128] = make([]byte, 256)
				if n%4096 == 0 {
					runtime.Gosched()
				}
			}
		}()
	}
	defer func() { stop.Store(true); busy.Wait() }()
	time.Sleep(100 * time.Millisecond)

	mean := func(probe func() bool) time.Duration {
		const iters = 2000
		start := time.Now()
		for i := 0; i < iters; i++ {
			if !probe() {
				t.Fatal("probe reported an open connection dead")
			}
		}
		return time.Since(start) / iters
	}

	peek := mean(conn.peerAliveSocket)
	deadline := mean(conn.peerAliveByDeadline)
	t.Logf("under load: peek %v, deadline fallback %v", peek.Round(time.Microsecond), deadline.Round(time.Microsecond))
	if peek >= deadline {
		t.Fatalf("expected the peek to be cheaper than the deadline fallback, got peek=%v deadline=%v", peek, deadline)
	}
}
