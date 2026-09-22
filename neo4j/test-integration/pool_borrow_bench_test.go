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

	"github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j/config"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j/test-integration/dbserver"
)

// One pool borrow per iteration, run in parallel, so anything the borrow path
// adds shows up whether or not it serialises other callers.
func BenchmarkConcurrentQueries(b *testing.B) {
	server := dbserver.GetDbServer(context.Background())
	ctx := context.Background()

	driver, err := neo4j.NewDriver(server.BoltURI(), server.AuthToken(),
		func(c *config.Config) { c.MaxConnectionPoolSize = 50 })
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = driver.Close(ctx) }()

	// warm the pool so no iteration pays for a dial
	warm := make([]neo4j.Session, 0, 16)
	for i := 0; i < 16; i++ {
		session := driver.NewSession(ctx, neo4j.SessionConfig{})
		result, err := session.Run(ctx, "RETURN 1 AS n", nil)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := result.Single(ctx); err != nil {
			b.Fatal(err)
		}
		warm = append(warm, session)
	}
	for _, session := range warm {
		_ = session.Close(ctx)
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			session := driver.NewSession(ctx, neo4j.SessionConfig{})
			result, err := session.Run(ctx, "RETURN 1 AS n", nil)
			if err != nil {
				b.Fatal(err)
			}
			if _, err := result.Single(ctx); err != nil {
				b.Fatal(err)
			}
			_ = session.Close(ctx)
		}
	})
}
