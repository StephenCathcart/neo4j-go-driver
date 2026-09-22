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

package test_integration

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j/config"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j/test-integration/dbserver"
)

// The user agent identifies which connections to close, so the driver doing the
// closing is never caught by its own query.
const backgroundCloseUserAgent = "go-driver-background-close-test"

type backgroundCloseFixture struct {
	server dbserver.DbServer
	ctx    context.Context
}

func (f backgroundCloseFixture) driver(t *testing.T, userAgent string, poolSize int) neo4j.Driver {
	t.Helper()
	// URI rather than BoltURI: the latter hardcodes bolt:// and so cannot reach
	// a server that requires encryption, which is what these tests need.
	driver, err := neo4j.NewDriver(f.server.URI(), f.server.AuthToken(),
		func(c *config.Config) {
			c.MaxConnectionPoolSize = poolSize
			c.UserAgent = userAgent
		})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driver.Close(f.ctx) })
	return driver
}

// closeConnectionsOf asks the server to close every connection a given user
// agent holds, and reports how many it closed.
func (f backgroundCloseFixture) closeConnectionsOf(t *testing.T, closer neo4j.Driver, userAgent string) int {
	t.Helper()
	session := closer.NewSession(f.ctx, neo4j.SessionConfig{})
	defer func() { _ = session.Close(f.ctx) }()
	result, err := session.Run(f.ctx,
		`CALL dbms.listConnections() YIELD connectionId, userAgent
		 WHERE userAgent = $agent
		 CALL dbms.killConnection(connectionId) YIELD message
		 RETURN count(*) AS closed`,
		map[string]any{"agent": userAgent})
	if err != nil {
		t.Skipf("this server cannot close connections on request: %v", err)
	}
	record, err := result.Single(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	return int(record.Values[0].(int64))
}

// awaitConnectionsGone waits until the server no longer lists any connection
// for the user agent. A fixed sleep is not enough: killConnection returns
// before the socket is actually torn down, and until the close reaches the
// client the kernel does not know about it, so no borrow-time check can.
func (f backgroundCloseFixture) awaitConnectionsGone(t *testing.T, observer neo4j.Driver, userAgent string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		session := observer.NewSession(f.ctx, neo4j.SessionConfig{})
		result, err := session.Run(f.ctx,
			`CALL dbms.listConnections() YIELD userAgent
			 WHERE userAgent = $agent
			 RETURN count(*) AS open`,
			map[string]any{"agent": userAgent})
		var open int64 = -1
		if err == nil {
			if record, err := result.Single(f.ctx); err == nil {
				open = record.Values[0].(int64)
			}
		}
		_ = session.Close(f.ctx)
		if open == 0 {
			// the server has let go; give the close a moment to traverse the wire
			time.Sleep(50 * time.Millisecond)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the server still lists connections for the user agent")
}

func newBackgroundCloseFixture() backgroundCloseFixture {
	ctx := context.Background()
	return backgroundCloseFixture{server: dbserver.GetDbServer(ctx), ctx: ctx}
}

// A server that closes a pooled connection and keeps running leaves the pool
// holding a connection that writes fine and only fails on the read.
func TestConnectionClosedInTheBackground(outer *testing.T) {
	f := newBackgroundCloseFixture()

	autocommit := func(t *testing.T, driver neo4j.Driver) error {
		t.Helper()
		session := driver.NewSession(f.ctx, neo4j.SessionConfig{})
		defer func() { _ = session.Close(f.ctx) }()
		result, err := session.Run(f.ctx, "RETURN 1 AS n", nil)
		if err != nil {
			return err
		}
		_, err = result.Single(f.ctx)
		return err
	}

	outer.Run("an autocommit query survives the close", func(t *testing.T) {
		pooled := f.driver(t, backgroundCloseUserAgent, 1)
		closer := f.driver(t, backgroundCloseUserAgent+"-closer", 1)

		if err := autocommit(t, pooled); err != nil {
			t.Fatalf("first query: %v", err)
		}
		if closed := f.closeConnectionsOf(t, closer, backgroundCloseUserAgent); closed == 0 {
			t.Fatal("no connection was closed, so the test proves nothing")
		}
		f.awaitConnectionsGone(t, closer, backgroundCloseUserAgent)

		if err := autocommit(t, pooled); err != nil {
			t.Fatalf("query after the server closed the pooled connection: %v", err)
		}
		if err := autocommit(t, pooled); err != nil {
			t.Fatalf("following query: %v", err)
		}
	})

	outer.Run("an explicit transaction survives the close", func(t *testing.T) {
		pooled := f.driver(t, backgroundCloseUserAgent, 1)
		closer := f.driver(t, backgroundCloseUserAgent+"-closer", 1)

		if err := autocommit(t, pooled); err != nil {
			t.Fatalf("warm-up: %v", err)
		}
		if closed := f.closeConnectionsOf(t, closer, backgroundCloseUserAgent); closed == 0 {
			t.Fatal("no connection was closed")
		}
		f.awaitConnectionsGone(t, closer, backgroundCloseUserAgent)

		session := pooled.NewSession(f.ctx, neo4j.SessionConfig{})
		defer func() { _ = session.Close(f.ctx) }()
		tx, err := session.BeginTransaction(f.ctx)
		if err != nil {
			t.Fatalf("begin after the close: %v", err)
		}
		result, err := tx.Run(f.ctx, "RETURN 1 AS n", nil)
		if err != nil {
			t.Fatalf("run in transaction: %v", err)
		}
		if _, err := result.Single(f.ctx); err != nil {
			t.Fatalf("consume in transaction: %v", err)
		}
		if err := tx.Commit(f.ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}
	})

	outer.Run("a managed transaction survives the close without retrying", func(t *testing.T) {
		pooled := f.driver(t, backgroundCloseUserAgent, 1)
		closer := f.driver(t, backgroundCloseUserAgent+"-closer", 1)

		if err := autocommit(t, pooled); err != nil {
			t.Fatalf("warm-up: %v", err)
		}
		if closed := f.closeConnectionsOf(t, closer, backgroundCloseUserAgent); closed == 0 {
			t.Fatal("no connection was closed")
		}
		f.awaitConnectionsGone(t, closer, backgroundCloseUserAgent)

		session := pooled.NewSession(f.ctx, neo4j.SessionConfig{})
		defer func() { _ = session.Close(f.ctx) }()
		attempts := 0
		_, err := session.ExecuteRead(f.ctx, func(tx neo4j.ManagedTransaction) (any, error) {
			attempts++
			result, err := tx.Run(f.ctx, "RETURN 1 AS n", nil)
			if err != nil {
				return nil, err
			}
			return result.Single(f.ctx)
		})
		if err != nil {
			t.Fatalf("managed read: %v", err)
		}
		// The point of the check is that the retry never has to fire.
		if attempts != 1 {
			t.Errorf("took %d attempts, expected the connection to be replaced before the first", attempts)
		}
	})

	outer.Run("ExecuteQuery survives the close", func(t *testing.T) {
		pooled := f.driver(t, backgroundCloseUserAgent, 1)
		closer := f.driver(t, backgroundCloseUserAgent+"-closer", 1)

		if _, err := neo4j.ExecuteQuery(f.ctx, pooled, "RETURN 1 AS n", nil,
			neo4j.EagerResultTransformer); err != nil {
			t.Fatalf("warm-up: %v", err)
		}
		if closed := f.closeConnectionsOf(t, closer, backgroundCloseUserAgent); closed == 0 {
			t.Fatal("no connection was closed")
		}
		f.awaitConnectionsGone(t, closer, backgroundCloseUserAgent)

		if _, err := neo4j.ExecuteQuery(f.ctx, pooled, "RETURN 1 AS n", nil,
			neo4j.EagerResultTransformer); err != nil {
			t.Fatalf("ExecuteQuery after the close: %v", err)
		}
	})
}

// Production-shaped traffic at concurrency, interleaved with the roll itself.
// A roll drains before it closes, so each cycle runs a burst of concurrent
// work, lets it finish, then closes every connection the pool is now holding
// idle. Nothing should ever surface to the caller.
//
// Connections are deliberately not closed mid-query: the server terminates
// those transactions with Neo.ClientError.Transaction.Terminated, which the
// driver is specified not to retry, and which no borrow-time check can prevent.
func TestBackgroundCloseUnderConcurrentTraffic(t *testing.T) {
	f := newBackgroundCloseFixture()
	pooled := f.driver(t, backgroundCloseUserAgent, 25)
	closer := f.driver(t, backgroundCloseUserAgent+"-closer", 1)

	label := fmt.Sprintf("BgClose%d", time.Now().UnixNano())
	t.Cleanup(func() {
		session := pooled.NewSession(f.ctx, neo4j.SessionConfig{})
		defer func() { _ = session.Close(f.ctx) }()
		_, _ = session.Run(f.ctx, fmt.Sprintf("MATCH (n:%s) DELETE n", label), nil)
	})

	const workers = 12
	const cycles = 5
	const roundsPerCycle = 3
	var failures, writes, reads, closedTotal int32
	errs := make(chan error, workers*cycles*roundsPerCycle*2)

	burst := func() {
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func(worker int) {
				defer wg.Done()
				for r := 0; r < roundsPerCycle; r++ {
					session := pooled.NewSession(f.ctx, neo4j.SessionConfig{})
					_, err := session.ExecuteWrite(f.ctx, func(tx neo4j.ManagedTransaction) (any, error) {
						_, err := tx.Run(f.ctx,
							fmt.Sprintf("CREATE (n:%s {worker: $w, round: $r})", label),
							map[string]any{"w": worker, "r": r})
						return nil, err
					})
					if err != nil {
						atomic.AddInt32(&failures, 1)
						errs <- fmt.Errorf("write w%d r%d: %w", worker, r, err)
					} else {
						atomic.AddInt32(&writes, 1)
					}
					_, err = session.ExecuteRead(f.ctx, func(tx neo4j.ManagedTransaction) (any, error) {
						result, err := tx.Run(f.ctx,
							fmt.Sprintf("MATCH (n:%s {worker: $w}) RETURN count(n) AS c", label),
							map[string]any{"w": worker})
						if err != nil {
							return nil, err
						}
						return result.Single(f.ctx)
					})
					if err != nil {
						atomic.AddInt32(&failures, 1)
						errs <- fmt.Errorf("read w%d r%d: %w", worker, r, err)
					} else {
						atomic.AddInt32(&reads, 1)
					}
					_ = session.Close(f.ctx)
				}
			}(w)
		}
		wg.Wait()
	}

	for cycle := 0; cycle < cycles; cycle++ {
		burst()
		// every pooled connection is idle now, exactly as it would be when a
		// drained instance closes them
		closed := f.closeConnectionsOf(t, closer, backgroundCloseUserAgent)
		atomic.AddInt32(&closedTotal, int32(closed))
		f.awaitConnectionsGone(t, closer, backgroundCloseUserAgent)
	}
	// one last burst, which has to run entirely on connections replaced after
	// the final close
	burst()
	close(errs)

	reported := 0
	for err := range errs {
		if reported < 5 {
			t.Errorf("%v", err)
			reported++
		}
	}
	t.Logf("%d writes, %d reads, %d failures, %d idle connections closed by the server across %d cycles",
		writes, reads, failures, closedTotal, cycles)
	if closedTotal == 0 {
		t.Fatal("the server never closed anything, so the test proves nothing")
	}
}
