// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
)

// StreamConfiguration describes one enabled input. Its identity changes when
// the provider decides that the input's existing continuation no longer applies.
type StreamConfiguration struct {
	stream   Stream
	identity string
}

func NewStreamConfiguration(stream Stream, identity []byte) (StreamConfiguration, error) {
	if stream.ID() == "" || stream.Format() == "" {
		return StreamConfiguration{}, ErrInvalid
	}

	if len(identity) > 1<<20 {
		return StreamConfiguration{}, ErrLimit
	}

	digest := sha256.Sum256(identity)

	return StreamConfiguration{stream: stream, identity: hex.EncodeToString(digest[:])}, nil
}

func (c StreamConfiguration) Stream() Stream   { return c.stream }
func (c StreamConfiguration) Identity() string { return c.identity }

func (r Registration) WithStreamConfigurer(configurer StreamConfigurer) (Registration, error) {
	if r.descriptor.ID() == "" || configurer == nil {
		return Registration{}, ErrInvalid
	}

	r.streamConfigurer = configurer

	return r, nil
}

// ConfigureStreams defaults to all declared inputs, each dependent on the full
// settings value. Providers can replace that policy without exposing field keys.
func (r Registration) ConfigureStreams(settings Settings) ([]StreamConfiguration, error) {
	var configurations []StreamConfiguration
	if r.streamConfigurer != nil {
		var err error

		configurations, err = r.streamConfigurer.ConfigureStreams(settings)
		if err != nil {
			return nil, err
		}
	} else {
		data, err := json.Marshal(settings.Values())
		if err != nil {
			return nil, ErrInvalid
		}

		digest := sha256.Sum256(data)
		for _, stream := range r.descriptor.streams {
			configuration, err := NewStreamConfiguration(stream, digest[:])
			if err != nil {
				return nil, err
			}

			configurations = append(configurations, configuration)
		}
	}

	if len(configurations) > len(r.descriptor.streams) {
		return nil, ErrInvalid
	}

	seen := map[string]bool{}
	for _, configuration := range configurations {
		stream := configuration.Stream()
		if configuration.identity == "" || seen[stream.ID()] || !slices.Contains(r.descriptor.streams, stream) {
			return nil, ErrInvalid
		}

		seen[stream.ID()] = true
	}

	return slices.Clone(configurations), nil
}
