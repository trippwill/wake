// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

// Package recovery defines Wake recovery plans and confirmation rules.
package recovery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

type planError string

func (e planError) Error() string { return string(e) }

const ErrSyncPlanChanged planError = "synchronization recovery plan changed; preview recovery again"

const storeCount = 3

type SyncPlan struct {
	ID           string              `json:"plan_id"`
	Stores       [storeCount]string  `json:"stores"`
	RetainAccess bool                `json:"retain_access"`
	Pending      bool                `json:"pending"`
	Applied      bool                `json:"applied"`
	Current      []SyncConfiguration `json:"current_configuration,omitempty"`
	Proposed     []SyncConfiguration `json:"proposed_configuration,omitempty"`
}

type SyncConfiguration struct {
	ID       string          `json:"connection_id"`
	Provider string          `json:"provider"`
	Name     string          `json:"name"`
	Settings json.RawMessage `json:"settings"`
}

// SyncPlanID binds the ordered store paths and content digests, plus the optional
// archive digest. The resulting ID is shown in previews and saved in durable restore intents.
func SyncPlanID(paths, inputs [storeCount]string, archive string) (string, error) {
	binding, err := json.Marshal(struct {
		Paths, Inputs [storeCount]string
		Archive       string
	}{paths, inputs, archive})
	if err != nil {
		return "", err
	}

	hash := sha256.Sum256(binding)

	return hex.EncodeToString(hash[:]), nil
}

// CheckConfirmation accepts an empty token for preview or the ID for application.
func (p SyncPlan) CheckConfirmation(confirm string) error {
	if confirm != "" && p.ID != confirm {
		return ErrSyncPlanChanged
	}

	return nil
}
