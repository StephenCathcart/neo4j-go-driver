/*
 * Copyright (c) "Neo4j"
 * Neo4j Sweden AB [https://neo4j.com]
 *
 * This file is part of Neo4j.
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

package telemetry

import (
	"sync"
)

const DefaultCacheSize = 20

type apiType string

const (
	// ApiTypeManaged for tracking [session.ExecuteRead] or [session.ExecuteWrite].
	ApiTypeManaged apiType = "fn"
	// ApiTypeUnmanaged for tracking [session.BeginTransaction].
	ApiTypeUnmanaged apiType = "tx"
	// ApiTypeRun for tracking [session.Run].
	ApiTypeRun apiType = "run"
	// ApiTypeExecuteQuery for tracking [driver.ExecuteQuery].
	ApiTypeExecuteQuery apiType = "exe"
)

type counter uint64

type metrics struct {
	api map[apiType]counter
}

type Telemetry struct {
	cacheSize      uint64
	aggregateCount uint64
	metrics        metrics
	mutex          sync.RWMutex
}

func New(cacheSize uint64) *Telemetry {
	return &Telemetry{
		cacheSize:      cacheSize,
		aggregateCount: 0,
		metrics:        metrics{api: make(map[apiType]counter)},
		mutex:          sync.RWMutex{},
	}
}

func (t *Telemetry) Track(api apiType) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	t.aggregateCount += 1
	t.metrics.api[api] += 1
}

func (t *Telemetry) CreateMessage() map[string]any {
	t.mutex.RLock()
	defer t.mutex.RLock()

	message := map[string]any{
		"metrics": map[string]any{},
	}

	usage := make(map[string]uint64)
	for api, count := range t.metrics.api {
		usage[string(api)] = uint64(count)
	}
	message["metrics"].(map[string]any)["api"] = usage

	return message
}

func (t *Telemetry) Clear() {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	t.aggregateCount = 0
	for key := range t.metrics.api {
		delete(t.metrics.api, key)
	}
}

func (t *Telemetry) IsCacheFull() bool {
	t.mutex.RLock()
	defer t.mutex.RLock()

	return t.aggregateCount >= t.cacheSize
}
