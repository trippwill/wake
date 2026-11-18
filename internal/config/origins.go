// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package config

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var browserNumericLabel = regexp.MustCompile("^(?:[0-9]+|0x[0-9a-f]*)$")

var dnsLabel = regexp.MustCompile("^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$")

// BrowserOrigin returns the serialized HTTP origin used by browsers.
func BrowserOrigin(raw string) (string, error) {
	const invalid Error = "invalid HTTP origin"

	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil ||
		strings.ContainsAny(raw, "?#%") || (u.Path != "" && u.Path != "/") || strings.HasSuffix(u.Host, ":") {
		return "", invalid
	}

	host, err := canonicalAuthority(u, u.Scheme)
	if err != nil {
		return "", invalid
	}

	return u.Scheme + "://" + host, nil
}

func canonicalAuthority(u *url.URL, scheme string) (string, error) {
	const invalid Error = "invalid host authority"

	host := strings.ToLower(u.Hostname())
	if strings.HasPrefix(u.Host, "[") || strings.Contains(host, ":") {
		ip, err := netip.ParseAddr(host)
		if err != nil || !ip.Is6() || !strings.HasPrefix(u.Host, "[") {
			return "", invalid
		}

		host = "[" + ip.String() + "]"
		if scheme != "" && ip.Is4In6() {
			// Browsers serialize mapped IPv6 addresses as hexadecimal pieces.
			v4 := ip.As4()
			host = fmt.Sprintf("[::ffff:%x:%x]", binary.BigEndian.Uint16(v4[:2]), binary.BigEndian.Uint16(v4[2:]))
		}
	} else {
		labels := strings.Split(strings.TrimSuffix(host, "."), ".")
		for _, label := range labels {
			if !dnsLabel.MatchString(label) {
				return "", invalid
			}
		}

		if scheme != "" && browserNumericLabel.MatchString(labels[len(labels)-1]) {
			// Numeric final labels trigger browser IPv4 parsing. Require dotted
			// decimal so every accepted address has one unambiguous meaning.
			ip, err := netip.ParseAddr(strings.TrimSuffix(host, "."))
			if err != nil || !ip.Is4() {
				return "", invalid
			}

			host = ip.String()
		}
	}

	if p := u.Port(); p != "" {
		port, err := strconv.ParseUint(p, 10, 16)
		if err != nil {
			return "", invalid
		}

		defaultPort := scheme == "http" && port == 80 || scheme == "https" && port == 443
		if !defaultPort {
			host += ":" + strconv.FormatUint(port, 10)
		}
	}

	return host, nil
}

func financialHost(raw string) (string, error) {
	const invalid Error = "invalid financial host"
	if strings.ContainsAny(raw, "/?#%") || strings.HasSuffix(raw, ":") {
		return "", invalid
	}

	u, err := url.Parse("//" + raw)
	if err != nil || u.User != nil {
		return "", invalid
	}

	if _, err := canonicalAuthority(u, ""); err != nil {
		return "", err
	}

	// Native clients can preserve the literal IPv6 and port spelling in Host.
	return strings.ToLower(raw), nil
}
