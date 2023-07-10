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
	"fmt"
	"testing"
)

func TestNew(test *testing.T) {
	actual := New(4)

	actual.Track(ApiTypeManaged)
	actual.Track(ApiTypeUnmanaged)
	actual.Track(ApiTypeRun)

	fmt.Printf("Full: %v\n", actual.IsCacheFull())

	actual.Track(ApiTypeExecuteQuery)
	actual.Track(ApiTypeExecuteQuery)
	actual.Track(ApiTypeExecuteQuery)

	fmt.Printf("Full: %v\n", actual.IsCacheFull())

	fmt.Printf("%+v\n", actual)

	actual.Clear()

	fmt.Printf("%+v\n", actual)
}

func TestCreateMessage(t *testing.T) {
	actual := New(4)

	actual.Track(ApiTypeManaged)
	actual.Track(ApiTypeManaged)
	actual.Track(ApiTypeRun)

	fmt.Printf("%+v\n", actual)
	fmt.Printf("%+v\n", actual.CreateMessage())
}
