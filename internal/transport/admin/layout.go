// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

func pageTitle(path string) string {
	switch path {
	case "/system/configuration":
		return "Configuration"
	case "/system":
		return "Daemon status"
	default:
		return "Overview"
	}
}
