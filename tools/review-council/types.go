package main

import (
	"fmt"
	"strings"
)

type Role struct {
	Slug       string
	Name       string
	PromptPath string
}

var roleRegistry = []Role{
	{Slug: "correctness", Name: "Correctness & Invariants", PromptPath: "review-council/roles/correctness.md"},
	{Slug: "security", Name: "Security & Privilege", PromptPath: "review-council/roles/security.md"},
	{Slug: "postgres-concurrency", Name: "PostgreSQL & Concurrency", PromptPath: "review-council/roles/postgres-concurrency.md"},
	{Slug: "architecture-recovery", Name: "Architecture & Failure Recovery", PromptPath: "review-council/roles/architecture-recovery.md"},
	{Slug: "premise-provenance", Name: "Premise & Provenance", PromptPath: "review-council/roles/premise-provenance.md"},
}

func selectedRoles(csv string) ([]Role, error) {
	if strings.TrimSpace(csv) == "" || strings.EqualFold(strings.TrimSpace(csv), "all") {
		out := make([]Role, len(roleRegistry))
		copy(out, roleRegistry)
		return out, nil
	}
	bySlug := make(map[string]Role, len(roleRegistry))
	for _, r := range roleRegistry {
		bySlug[r.Slug] = r
	}
	seen := map[string]bool{}
	var out []Role
	for _, raw := range strings.Split(csv, ",") {
		slug := strings.TrimSpace(raw)
		if slug == "" {
			continue
		}
		r, ok := bySlug[slug]
		if !ok {
			return nil, fmt.Errorf("unknown review role %q", slug)
		}
		if seen[slug] {
			continue
		}
		seen[slug] = true
		out = append(out, r)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no review roles selected")
	}
	return out, nil
}

type Provenance struct {
	RepoPath        string `json:"repo_path"`
	HeadSHA         string `json:"head_sha"`
	OriginMainSHA   string `json:"origin_main_sha"`
	MigrationHead   string `json:"migration_head"`
	GitStatus       string `json:"git_status"`
	GitStatusDetail string `json:"git_status_detail,omitempty"`
	BaseRef         string `json:"base_ref"`
	BaseSHA         string `json:"base_sha"`
	CandidateSHA256 string `json:"candidate_sha256"`
	SourceSHA256    string `json:"source_sha256"`
}

type ReviewerResult struct {
	Role            Role   `json:"role"`
	Content         string `json:"content,omitempty"`
	ValidProvenance bool   `json:"valid_provenance"`
	Error           string `json:"error,omitempty"`
}
