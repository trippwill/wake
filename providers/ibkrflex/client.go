// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package ibkrflex

import (
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"go.trippwill.dev/wake/provider"
)

type control struct {
	XMLName   xml.Name
	Status    string `xml:"Status"`
	Reference string `xml:"ReferenceCode"`
	Code      string `xml:"ErrorCode"`
}

type reply struct {
	body      []byte
	header    http.Header
	code      string
	reference string
}

func (s *Source) request(
	ctx context.Context,
	operation string,
	secret []byte,
	query string,
	parameters url.Values,
) (reply, error) {
	if len(secret) == 0 {
		return reply{}, provider.ErrUnauthorized
	}

	values := url.Values{"t": {string(secret)}, "q": {query}, "v": {"3"}}
	for key, entries := range parameters {
		values[key] = entries
	}

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		s.base+"/"+operation+"?"+values.Encode(),
		nil,
	)
	if err != nil {
		return reply{}, provider.ErrInvalid
	}

	request.Header.Set("User-Agent", "Wake Flex client")

	response, err := s.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return reply{}, ctx.Err()
		}

		return reply{}, provider.ErrTemporary
	}

	defer func() { _ = response.Body.Close() }()

	result := reply{header: response.Header}
	if response.StatusCode != http.StatusOK {
		switch response.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return result, provider.ErrUnauthorized
		default:
			if response.StatusCode < 500 && response.StatusCode != http.StatusTooManyRequests {
				return result, provider.ErrInvalid
			}

			result.code = strconv.Itoa(response.StatusCode)

			return result, nil
		}
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, s.limit+1))
	if err != nil {
		if ctx.Err() != nil {
			return reply{}, ctx.Err()
		}

		return reply{}, provider.ErrTemporary
	}

	if int64(len(body)) > s.limit {
		return reply{}, provider.ErrLimit
	}

	if operation == "GetStatement" && reportRoot(body) {
		if bytes.Contains(body, secret) {
			return reply{}, provider.ErrMalformed
		}

		result.body = body

		return result, nil
	}

	var message control

	if err = walk(ctx, body, nil); err != nil {
		return reply{}, err
	}

	if err = xml.Unmarshal(
		body,
		&message,
	); err != nil || message.XMLName.Local != "FlexStatementResponse" ||
		message.XMLName.Space != "" {
		return reply{}, provider.ErrMalformed
	}

	if message.Status == "Success" && operation == "SendRequest" {
		if bytes.Contains(body, secret) {
			return reply{}, provider.ErrMalformed
		}

		if !safeText(message.Reference, 1024) {
			return reply{}, provider.ErrMalformed
		}

		result.reference = message.Reference

		return result, nil
	}

	if !digits(message.Code) || len(message.Code) > 8 {
		return reply{}, provider.ErrMalformed
	}

	result.code = message.Code

	return result, nil
}

func reportRoot(body []byte) bool {
	decoder := xml.NewDecoder(bytes.NewReader(body))
	for {
		token, err := decoder.Token()
		if err != nil {
			return false
		}

		if start, ok := token.(xml.StartElement); ok {
			return start.Name.Local == "FlexQueryResponse" && start.Name.Space == ""
		}
	}
}

func retryAfter(header http.Header, now time.Time) time.Time {
	value := header.Get("Retry-After")
	if seconds, err := strconv.ParseInt(
		value,
		10,
		64,
	); err == nil && seconds >= 0 &&
		seconds <= math.MaxInt64/int64(time.Second) {
		return now.Add(time.Duration(seconds) * time.Second)
	}

	if date, err := http.ParseTime(value); err == nil {
		return date
	}

	return time.Time{}
}

func digits(value string) bool {
	if value == "" {
		return false
	}

	for _, c := range value {
		if c < '0' || c > '9' {
			return false
		}
	}

	return true
}
