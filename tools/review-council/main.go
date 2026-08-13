package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func main() {
	if err := runCLI(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "review-council:", err)
		os.Exit(1)
	}
}

func runCLI(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("review-council", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var includes stringList
	candidate := fs.String("candidate", "", "candidate/tranche description file (required)")
	base := fs.String("base", "HEAD^", "git base ref for candidate diff")
	rolesCSV := fs.String("roles", "all", "comma-separated reviewer roles or all")
	modelFlag := fs.String("model", "", "review model slug")
	maxBytes := fs.Int64("max-source-bytes", 600000, "maximum combined candidate/diff/source bytes")
	dryRun := fs.Bool("dry-run", false, "build prompts and artifacts without remote model calls")
	outFlag := fs.String("out", "", "output directory (default .review-council/runs/<timestamp>-<shortsha>)")
	fs.Var(&includes, "include", "file or directory to include in full source bundle (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*candidate) == "" {
		return fmt.Errorf("--candidate is required")
	}
	if *maxBytes <= 0 {
		return fmt.Errorf("--max-source-bytes must be positive")
	}
	roles, err := selectedRoles(*rolesCSV)
	if err != nil {
		return err
	}
	root, err := gitOutput(ctx, ".", "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("locate repository root: %w", err)
	}
	p, err := captureGitProvenance(ctx, root, *base)
	if err != nil {
		return err
	}
	candidateAbs, err := safeRepoPath(root, *candidate)
	if err != nil {
		return err
	}
	candidateBytes, err := os.ReadFile(candidateAbs)
	if err != nil {
		return fmt.Errorf("read candidate: %w", err)
	}
	candidateSum := sha256.Sum256(candidateBytes)
	p.CandidateSHA256 = hex.EncodeToString(candidateSum[:])
	b, err := buildBundle(ctx, BundleInput{
		Root: root, CandidatePath: *candidate, BaseRef: *base, Includes: includes,
		MaxBytes: *maxBytes, Provenance: p,
	})
	if err != nil {
		return err
	}
	p.SourceSHA256 = b.SHA256

	prompts := make(map[string]string, len(roles))
	for _, role := range roles {
		prompt, err := buildPrompt(root, role, p, b)
		if err != nil {
			return err
		}
		prompts[role.Slug] = prompt
	}
	adjudicationBytes, err := os.ReadFile(filepath.Join(root, "review-council", "ADJUDICATION.md"))
	if err != nil {
		return fmt.Errorf("read adjudication rules: %w", err)
	}

	model := strings.TrimSpace(*modelFlag)
	if model == "" {
		model = strings.TrimSpace(os.Getenv("REVIEW_COUNCIL_MODEL"))
	}
	if model == "" {
		model = defaultReviewModel
	}
	outDir := strings.TrimSpace(*outFlag)
	if outDir == "" {
		short := p.HeadSHA
		if len(short) > 8 {
			short = short[:8]
		}
		runID := time.Now().UTC().Format("20060102T150405Z") + "-" + short
		outDir = filepath.Join(root, ".review-council", "runs", runID)
	} else if !filepath.IsAbs(outDir) {
		outDir = filepath.Join(root, outDir)
	}

	if *dryRun {
		results := make([]ReviewerResult, 0, len(roles))
		for _, role := range roles {
			results = append(results, ReviewerResult{Role: role, Error: "DRY_RUN_NO_REMOTE_CALL"})
		}
		if err := writeRunArtifacts(outDir, model, p, b, prompts, results, true, string(adjudicationBytes)); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "dry-run review council artifacts: %s\n", outDir)
		return nil
	}

	apiKey := os.Getenv("OPENROUTER_API_KEY")
	if strings.TrimSpace(apiKey) == "" {
		return fmt.Errorf("OPENROUTER_API_KEY is required unless --dry-run is used")
	}
	endpoint := strings.TrimSpace(os.Getenv("OPENROUTER_BASE_URL"))
	if endpoint == "" {
		endpoint = defaultOpenRouterEndpoint
	}
	client := &OpenRouterClient{
		Endpoint:  endpoint,
		APIKey:    apiKey,
		Model:     model,
		HTTP:      &http.Client{Timeout: 5 * time.Minute},
		MaxTokens: 9000,
		Retries:   2,
		Sleep:     time.Sleep,
	}
	results := runCouncil(ctx, client, root, roles, p, b)
	failed := false
	for i := range results {
		if results[i].Error != "" {
			failed = true
			continue
		}
		if err := validateReviewerProvenance(results[i].Content, p); err != nil {
			results[i].Error = err.Error()
			results[i].ValidProvenance = false
			failed = true
			continue
		}
		results[i].ValidProvenance = true
	}
	if err := writeRunArtifacts(outDir, model, p, b, prompts, results, false, string(adjudicationBytes)); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "review council artifacts: %s\n", outDir)
	if failed {
		return fmt.Errorf("one or more requested review lenses failed or returned invalid provenance; successful outputs were preserved")
	}
	return nil
}
