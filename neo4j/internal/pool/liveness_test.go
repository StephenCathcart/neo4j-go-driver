//go:build internal_neo4j_go_driver_time_mock

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

package pool

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neo4j/neo4j-go-driver/v6/neo4j/config"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j/internal/bolt"
	idb "github.com/neo4j/neo4j-go-driver/v6/neo4j/internal/db"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j/internal/homedb"
	. "github.com/neo4j/neo4j-go-driver/v6/neo4j/internal/testutil"
	itime "github.com/neo4j/neo4j-go-driver/v6/neo4j/internal/time"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j/log"
)

// countingConnect hands out fresh connections and records how many it made, so
// a test can tell reuse from a reconnect.
func countingConnect(made *int32, born time.Time) (Connect, *[]*ConnFake) {
	var mut sync.Mutex
	var all []*ConnFake
	return func(_ context.Context, name string, _ *idb.ReAuthToken, _ bolt.ConnectionErrorListener, _ log.BoltLogger) (idb.Connection, error) {
		atomic.AddInt32(made, 1)
		c := &ConnFake{Name: name, Alive: true, Birth: born}
		mut.Lock()
		all = append(all, c)
		mut.Unlock()
		return c, nil
	}, &all
}

func livenessPool(t *testing.T, connect Connect, size int) *Pool {
	t.Helper()
	conf := config.Config{MaxConnectionLifetime: time.Hour, MaxConnectionPoolSize: size}
	p := New(&conf, connect, logger, "pool id", &homedb.Cache{})
	t.Cleanup(func() { p.Close(ctx) })
	return p
}

func TestBorrowDiscardsAConnectionTheServerClosed(outer *testing.T) {
	servers := getServers([]string{"srv1"})

	outer.Run("a dead pooled connection is replaced, not handed out", func(t *testing.T) {
		itime.ForceFreezeTime()
		defer itime.ForceUnfreezeTime()
		var made int32
		connect, _ := countingConnect(&made, itime.Now())
		p := livenessPool(t, connect, 1)

		first, err := p.Borrow(ctx, servers, true, nil, DefaultConnectionLivenessCheckTimeout, reAuthToken)
		if err != nil {
			t.Fatal(err)
		}
		p.Return(ctx, first)
		if made != 1 {
			t.Fatalf("expected 1 connect, got %d", made)
		}

		// the server closes it while it sits in the pool
		first.(*ConnFake).KillPeer()

		second, err := p.Borrow(ctx, servers, true, nil, DefaultConnectionLivenessCheckTimeout, reAuthToken)
		if err != nil {
			t.Fatalf("borrow after the peer closed: %v", err)
		}
		if second == first {
			t.Fatal("handed out the connection the server had closed")
		}
		if made != 2 {
			t.Fatalf("expected a reconnect, connects = %d", made)
		}
		// the pool closes connections on another goroutine
		deadline := time.Now().Add(2 * time.Second)
		for first.(*ConnFake).CloseCount() == 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if first.(*ConnFake).CloseCount() == 0 {
			t.Fatal("the dead connection was not closed")
		}
		p.Return(ctx, second)
	})

	outer.Run("only the dead ones are discarded", func(t *testing.T) {
		itime.ForceFreezeTime()
		defer itime.ForceUnfreezeTime()
		var made int32
		connect, _ := countingConnect(&made, itime.Now())
		p := livenessPool(t, connect, 3)

		var borrowed []idb.Connection
		for i := 0; i < 3; i++ {
			c, err := p.Borrow(ctx, servers, true, nil, DefaultConnectionLivenessCheckTimeout, reAuthToken)
			if err != nil {
				t.Fatal(err)
			}
			borrowed = append(borrowed, c)
		}
		for _, c := range borrowed {
			p.Return(ctx, c)
		}
		if made != 3 {
			t.Fatalf("expected 3 connects, got %d", made)
		}

		// kill two of the three
		borrowed[0].(*ConnFake).KillPeer()
		borrowed[1].(*ConnFake).KillPeer()

		// three borrows should reuse the survivor and dial two replacements
		for i := 0; i < 3; i++ {
			c, err := p.Borrow(ctx, servers, true, nil, DefaultConnectionLivenessCheckTimeout, reAuthToken)
			if err != nil {
				t.Fatalf("borrow %d: %v", i, err)
			}
			if !c.IsPeerAlive() {
				t.Fatalf("borrow %d handed out a dead connection", i)
			}
			defer p.Return(ctx, c)
		}
		if made != 5 {
			t.Fatalf("expected 2 replacements (5 connects total), got %d", made)
		}
	})

	outer.Run("a pool of entirely dead connections still serves a borrow", func(t *testing.T) {
		itime.ForceFreezeTime()
		defer itime.ForceUnfreezeTime()
		var made int32
		connect, all := countingConnect(&made, itime.Now())
		p := livenessPool(t, connect, 4)

		var borrowed []idb.Connection
		for i := 0; i < 4; i++ {
			c, err := p.Borrow(ctx, servers, true, nil, DefaultConnectionLivenessCheckTimeout, reAuthToken)
			if err != nil {
				t.Fatal(err)
			}
			borrowed = append(borrowed, c)
		}
		for _, c := range borrowed {
			p.Return(ctx, c)
		}
		for _, c := range *all {
			c.KillPeer()
		}

		fresh, err := p.Borrow(ctx, servers, true, nil, DefaultConnectionLivenessCheckTimeout, reAuthToken)
		if err != nil {
			t.Fatalf("borrow with every pooled connection dead: %v", err)
		}
		if !fresh.IsPeerAlive() {
			t.Fatal("handed out a dead connection")
		}
		p.Return(ctx, fresh)
	})
}

// A connection that dies while sitting in the pool must never be handed out,
// however many goroutines are competing for it. Each worker kills its own
// connection before returning it, so the kill always lands on an idle
// connection and the assertion carries no inherent race. Run with -race.
func TestConcurrentBorrowNeverHandsOutAConnectionThatDiedWhileIdle(t *testing.T) {
	servers := getServers([]string{"srv1", "srv2"})
	var made int32
	connect, _ := countingConnect(&made, time.Now())
	conf := config.Config{MaxConnectionLifetime: time.Hour, MaxConnectionPoolSize: 8}
	p := New(&conf, connect, logger, "pool id", &homedb.Cache{})
	defer p.Close(ctx)

	const workers = 24
	const rounds = 200
	var wg sync.WaitGroup
	var deadHandouts, borrowErrors, killed int32

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				conn, err := p.Borrow(ctx, servers, true, nil, DefaultConnectionLivenessCheckTimeout, reAuthToken)
				if err != nil {
					atomic.AddInt32(&borrowErrors, 1)
					return
				}
				if !conn.IsPeerAlive() {
					atomic.AddInt32(&deadHandouts, 1)
				}
				// kill roughly a third of them while we still hold them, so the
				// kill is in place before the connection becomes idle again
				if (seed+r)%3 == 0 {
					conn.(*ConnFake).KillPeer()
					atomic.AddInt32(&killed, 1)
				}
				p.Return(ctx, conn)
			}
		}(i)
	}
	wg.Wait()

	if borrowErrors != 0 {
		t.Errorf("%d borrows failed", borrowErrors)
	}
	if deadHandouts != 0 {
		t.Errorf("%d borrows were handed a connection that had died while idle", deadHandouts)
	}
	t.Logf("%d borrows across %d goroutines, %d connections killed while idle, %d dialled",
		workers*rounds, workers, killed, made)
}
