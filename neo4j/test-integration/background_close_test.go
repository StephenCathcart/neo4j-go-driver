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
	"testing"
	"time"

	"github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j/config"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j/test-integration/dbserver"
)

// The user agent is what identifies the connections to close, so the driver
// doing the closing is not caught by its own query.
const backgroundCloseUserAgent = "go-driver-background-close-test"

// A server that closes a pooled connection and keeps running leaves the pool
// holding a connection that writes fine and only fails on the read.
func TestConnectionClosedInTheBackground(ot *testing.T) {
	server := dbserver.GetDbServer(context.Background())
	ctx := context.Background()

	driverWithAgent := func(t *testing.T, userAgent string) neo4j.Driver {
		t.Helper()
		driver, err := neo4j.NewDriver(server.BoltURI(), server.AuthToken(),
			func(c *config.Config) {
				c.MaxConnectionPoolSize = 1
				c.UserAgent = userAgent
			})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = driver.Close(ctx) })
		return driver
	}

	query := func(t *testing.T, driver neo4j.Driver) error {
		t.Helper()
		session := driver.NewSession(ctx, neo4j.SessionConfig{})
		defer func() { _ = session.Close(ctx) }()
		result, err := session.Run(ctx, "RETURN 1 AS n", nil)
		if err != nil {
			return err
		}
		_, err = result.Single(ctx)
		return err
	}

	closeConnectionsOf := func(t *testing.T, driver neo4j.Driver, userAgent string) int {
		t.Helper()
		session := driver.NewSession(ctx, neo4j.SessionConfig{})
		defer func() { _ = session.Close(ctx) }()
		result, err := session.Run(ctx,
			`CALL dbms.listConnections() YIELD connectionId, userAgent
			 WHERE userAgent = $agent
			 CALL dbms.killConnection(connectionId) YIELD message
			 RETURN count(*) AS closed`,
			map[string]any{"agent": userAgent})
		if err != nil {
			t.Skipf("this server cannot close connections on request: %v", err)
		}
		record, err := result.Single(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return int(record.Values[0].(int64))
	}

	ot.Run("the connection is replaced rather than used", func(t *testing.T) {
		pooled := driverWithAgent(t, backgroundCloseUserAgent)
		closer := driverWithAgent(t, backgroundCloseUserAgent+"-closer")

		if err := query(t, pooled); err != nil {
			t.Fatalf("first query: %v", err)
		}
		if closed := closeConnectionsOf(t, closer, backgroundCloseUserAgent); closed == 0 {
			t.Fatal("no connection was closed, so the test proves nothing")
		}
		time.Sleep(300 * time.Millisecond)

		if err := query(t, pooled); err != nil {
			t.Fatalf("query after the server closed the pooled connection: %v", err)
		}
		if err := query(t, pooled); err != nil {
			t.Fatalf("following query: %v", err)
		}
	})
}
