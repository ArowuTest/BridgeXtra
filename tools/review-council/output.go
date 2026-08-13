package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type RunManifest struct {
	RunID      string           `json:"run_id"`
	Model      string           `json:"model"`
	Provenance Provenance       `json:"provenance"`
	Roles      []string         `json:"roles"`
	Results    []ReviewerResult `json:"results"`
	DryRun     bool             `json:"dry_run"`
}

func validateReviewerProvenance(content string, p Provenance) error {
	fields := map[string]string{}
	lines := strings.Split(content, "\n")
	limit := len(lines)
	if limit > 30 {
		limit = 30
	}
	for _, raw := range lines[:limit] {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if _, exists := fields[key]; !exists {
			fields[key] = strings.TrimSpace(value)
		}
	}
	expected := map[string]string{
		"repoPath":         p.RepoPath,
		"headSHA":          p.HeadSHA,
		"originMainSHA":    p.OriginMainSHA,
		"migrationHead":    p.MigrationHead,
		"gitStatus":        p.GitStatus,
		"premisesVerified": "YES",
	}
	for key, want := range expected {
		got, ok := fields[key]
		if !ok {
			return fmt.Errorf("INVALID_PROVENANCE: missing %s", key)
		}
		if got != want {
			return fmt.Errorf("INVALID_PROVENANCE: %s=%q want %q", key, got, want)
		}
	}
	return nil
}

func buildAdjudicationPacket(p Provenance, results []ReviewerResult, adjudicationRules string) string {
	var b strings.Builder
	b.WriteString("# BridgeXtra Grok Review Council — GPT-5.6 Sol Adjudication Packet\n\n")
	b.WriteString("## Candidate provenance\n\n```text\n")
	fmt.Fprintf(&b, "repoPath: %s\nheadSHA: %s\noriginMainSHA: %s\nmigrationHead: %s\ngitStatus: %s\nbaseRef: %s\nbaseSHA: %s\n", p.RepoPath, p.HeadSHA, p.OriginMainSHA, p.MigrationHead, p.GitStatus, p.BaseRef, p.BaseSHA)
	b.WriteString("```\n\n## Adjudication rules\n\n")
	b.WriteString(adjudicationRules)
	b.WriteString("\n\n")
	var missing []string
	for _, r := range results {
		if r.Error != "" || !r.ValidProvenance {
			missing = append(missing, r.Role.Slug)
			continue
		}
		fmt.Fprintf(&b, "## Reviewer: %s (%s)\n\n", r.Role.Name, r.Role.Slug)
		b.WriteString(r.Content)
		if !strings.HasSuffix(r.Content, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		b.WriteString("## Missing/invalid lenses\n\n")
		for _, slug := range missing {
			fmt.Fprintf(&b, "- %s\n", slug)
		}
	}
	return b.String()
}

func writeRunArtifacts(outDir, model string, p Provenance, b Bundle, prompts map[string]string, results []ReviewerResult, dryRun bool, adjudicationRules string) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	secret := os.Getenv("OPENROUTER_API_KEY")
	sanitize := func(s string) string {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "[REDACTED_OPENROUTER_API_KEY]")
		}
		return s
	}
	writeText := func(name, content string) error {
		return os.WriteFile(filepath.Join(outDir, name), []byte(sanitize(content)), 0o600)
	}

	roles := make([]string, 0, len(results))
	cleanResults := make([]ReviewerResult, len(results))
	copy(cleanResults, results)
	for i := range cleanResults {
		roles = append(roles, cleanResults[i].Role.Slug)
		cleanResults[i].Content = sanitize(cleanResults[i].Content)
		cleanResults[i].Error = sanitize(cleanResults[i].Error)
	}
	manifest := RunManifest{
		RunID:      filepath.Base(filepath.Clean(outDir)),
		Model:      model,
		Provenance: p,
		Roles:      roles,
		Results:    cleanResults,
		DryRun:     dryRun,
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "manifest.json"), []byte(sanitize(string(manifestBytes))+"\n"), 0o600); err != nil {
		return err
	}
	prov := fmt.Sprintf("repoPath: %s\nheadSHA: %s\noriginMainSHA: %s\nmigrationHead: %s\ngitStatus: %s\nbaseRef: %s\nbaseSHA: %s\ncandidateSHA256: %s\nsourceSHA256: %s\n",
		p.RepoPath, p.HeadSHA, p.OriginMainSHA, p.MigrationHead, p.GitStatus, p.BaseRef, p.BaseSHA, p.CandidateSHA256, p.SourceSHA256)
	if p.GitStatusDetail != "" {
		prov += "\ngitStatusDetail:\n" + p.GitStatusDetail + "\n"
	}
	if err := writeText("provenance.txt", prov); err != nil {
		return err
	}
	if err := writeText("source-bundle.txt", b.Combined); err != nil {
		return err
	}
	promptKeys := make([]string, 0, len(prompts))
	for slug := range prompts {
		promptKeys = append(promptKeys, slug)
	}
	sort.Strings(promptKeys)
	for _, slug := range promptKeys {
		if err := writeText("prompt-"+slug+".txt", prompts[slug]); err != nil {
			return err
		}
	}
	for _, r := range cleanResults {
		content := r.Content
		if content == "" && r.Error != "" {
			content = "ERROR: " + r.Error + "\n"
		}
		if err := writeText("reviewer-"+r.Role.Slug+".md", content); err != nil {
			return err
		}
	}
	packet := buildAdjudicationPacket(p, cleanResults, sanitize(adjudicationRules))
	return writeText("adjudication-packet.md", packet)
}
