// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package daemon

import (
	"context"
	"log/slog"
	"net"
	"os"
	"syscall"
)

// sameUIDListener rejects a Unix peer before it can reach HTTP dispatch.
type sameUIDListener struct {
	net.Listener
	logger *slog.Logger
}

func (listener sameUIDListener) Accept() (net.Conn, error) {
	for {
		connection, err := listener.Listener.Accept()
		if err != nil {
			return nil, err
		}

		unixConnection, ok := connection.(*net.UnixConn)
		if !ok || peerUID(unixConnection) != os.Geteuid() {
			_ = connection.Close()

			if listener.logger != nil {
				listener.logger.WarnContext(
					context.Background(),
					"socket peer rejected",
					"event",
					"socket_peer_rejected",
					"code",
					"peer_not_authorized",
				)
			}

			continue
		}

		return connection, nil
	}
}

func peerUID(connection *net.UnixConn) int {
	raw, err := connection.SyscallConn()
	if err != nil {
		return -1
	}

	uid := -1
	if raw.Control(func(fd uintptr) {
		credentials, controlErr := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
		if controlErr == nil {
			uid = int(credentials.Uid)
		}
	}) != nil {
		return -1
	}

	return uid
}
