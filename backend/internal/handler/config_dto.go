package handler

// Shared config DTOs. These were formerly co-located with the header-
// authenticated admin config API (removed in EXT-1 — that was a role-unaware
// parallel door to configsvc). The portal's RBAC- and scope-gated config
// handlers are the sole config surface now, and reuse these shapes.

import (
	"encoding/json"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
)

// draftRequest is the create-draft body (domain/scope/reason/content).
type draftRequest struct {
	Domain  string          `json:"domain"`
	Scope   string          `json:"scope"`
	Reason  string          `json:"reason"`
	Content json.RawMessage `json:"content"`
}

type configVersionResponse struct {
	ConfigVersionID string          `json:"config_version_id"`
	Domain          string          `json:"domain"`
	Scope           string          `json:"scope"`
	VersionNo       int             `json:"version_no"`
	State           string          `json:"state"`
	Content         json.RawMessage `json:"content"`
	ContentHash     string          `json:"content_hash"`
	EffectiveFrom   *time.Time      `json:"effective_from,omitempty"`
	EffectiveTo     *time.Time      `json:"effective_to,omitempty"`
	CreatedBy       string          `json:"created_by"`
	ApprovedBy      string          `json:"approved_by,omitempty"`
	Reason          string          `json:"reason"`
}

func toConfigResponse(c entity.ConfigVersion) configVersionResponse {
	return configVersionResponse{
		ConfigVersionID: c.ConfigVersionID,
		Domain:          c.Domain,
		Scope:           c.Scope,
		VersionNo:       c.VersionNo,
		State:           string(c.State),
		Content:         json.RawMessage(c.Content),
		ContentHash:     c.ContentHash,
		EffectiveFrom:   c.EffectiveFrom,
		EffectiveTo:     c.EffectiveTo,
		CreatedBy:       c.CreatedBy,
		ApprovedBy:      c.ApprovedBy,
		Reason:          c.Reason,
	}
}
