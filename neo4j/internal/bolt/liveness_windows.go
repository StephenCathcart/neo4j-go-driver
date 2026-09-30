//go:build windows

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
	"syscall"
	"unsafe"
)

// Winsock values syscall does not export.
const (
	errNothingWaiting syscall.Errno = 10035 // WSAEWOULDBLOCK
	msgPeek           uint32        = 0x2
	fionbio           uint32        = 0x8004667e
)

// The socket blocks (Go uses overlapped I/O on Windows), so it is switched to
// non-blocking around the peek. Overlapped reads and writes ignore the mode.
func peekSocket(fd uintptr) (int, error) {
	if err := setNonblocking(fd, true); err != nil {
		return 0, err
	}
	defer setNonblocking(fd, false)
	var b [1]byte
	buf := syscall.WSABuf{Len: 1, Buf: &b[0]}
	var n, flags uint32 = 0, msgPeek
	err := syscall.WSARecv(syscall.Handle(fd), &buf, 1, &n, &flags, nil, nil)
	return int(n), err
}

func setNonblocking(fd uintptr, on bool) error {
	var mode uint32
	if on {
		mode = 1
	}
	var ret uint32
	return syscall.WSAIoctl(syscall.Handle(fd), fionbio, (*byte)(unsafe.Pointer(&mode)), 4, nil, 0, &ret, nil, 0)
}
