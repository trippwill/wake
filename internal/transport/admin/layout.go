// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

func pageTitle(path string) string {
	switch path {
	case "/configuration":
		return "Configuration"
	case "/status":
		return "Daemon status"
	default:
		return "Overview"
	}
}
