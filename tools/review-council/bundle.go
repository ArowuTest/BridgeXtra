package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type BundleInput struct {
	Root          string
	CandidatePath string
	BaseRef       string
	Includes      []string
	MaxBytes      int64
	Provenance    Provenance
}

type Bundle struct {
	Candidate string
	Diff      string
	Source    string
	Combined  string
	SHA256    string
}

func buildBundle(ctx context.Context, in BundleInput) (Bundle, error) {
	if in.MaxBytes <= 0 {
		return Bundle{}, fmt.Errorf("max source budget must be positive")
	}
	root, err := filepath.Abs(in.Root)
	if err != nil {
		return Bundle{}, err
	}
	candidatePath, err := safeRepoPath(root, in.CandidatePath)
	if err != nil {
		return Bundle{}, fmt.Errorf("candidate: %w", err)
	}
	candidateBytes, err := os.ReadFile(candidatePath)
	if err != nil {
		return Bundle{}, fmt.Errorf("read candidate: %w", err)
	}
	if bytes.IndexByte(candidateBytes, 0) >= 0 {
		return Bundle{}, fmt.Errorf("candidate is binary")
	}
	candidate := string(candidateBytes)

	base := in.BaseRef
	if base == "" {
		base = "HEAD^"
	}
	diff, err := gitOutput(ctx, root, "diff", "--no-ext-diff", base+"..HEAD")
	if err != nil {
		return Bundle{}, err
	}

	files, err := collectIncludeFiles(root, in.Includes)
	if err != nil {
		return Bundle{}, err
	}
	var source strings.Builder
	for _, rel := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		data, err := os.ReadFile(abs)
		if err != nil {
			return Bundle{}, fmt.Errorf("read include %s: %w", rel, err)
		}
		if bytes.IndexByte(data, 0) >= 0 {
			continue
		}
		source.WriteString("\n===== FILE: ")
		source.WriteString(filepath.ToSlash(rel))
		source.WriteString(" =====\n")
		source.WriteString(numberLines(string(data)))
	}

	combined := "===== CANDIDATE =====\n" + candidate + "\n\n===== GIT DIFF " + base + "..HEAD =====\n" + diff + "\n\n===== INCLUDED SOURCE =====\n" + source.String()
	if int64(len(combined)) > in.MaxBytes {
		return Bundle{}, fmt.Errorf("source budget exceeded: combined packet is %d bytes, limit is %d", len(combined), in.MaxBytes)
	}
	sum := sha256.Sum256([]byte(combined))
	return Bundle{Candidate: candidate, Diff: diff, Source: source.String(), Combined: combined, SHA256: hex.EncodeToString(sum[:])}, nil
}

func safeRepoPath(root, rel string) (string, error) {
	if strings.TrimSpace(rel) == "" {
		return "", fmt.Errorf("path is empty")
	}
	var abs string
	if filepath.IsAbs(rel) {
		abs = filepath.Clean(rel)
	} else {
		abs = filepath.Join(root, filepath.Clean(rel))
	}
	relToRoot, err := filepath.Rel(root, abs)
	if err != nil {
		return "", err
	}
	if relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes repository: %s", rel)
	}
	return abs, nil
}

func collectIncludeFiles(root string, includes []string) ([]string, error) {
	seen := map[string]bool{}
	var files []string
	for _, include := range includes {
		abs, err := safeRepoPath(root, include)
		if err != nil {
			return nil, err
		}
		info, err := os.Lstat(abs)
		if err != nil {
			return nil, fmt.Errorf("include %s: %w", include, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("include %s is a symlink; symlinks are refused", include)
		}
		if !info.IsDir() {
			data, err := os.ReadFile(abs)
			if err != nil {
				return nil, err
			}
			if bytes.IndexByte(data, 0) >= 0 {
				return nil, fmt.Errorf("include %s is binary", include)
			}
			rel, _ := filepath.Rel(root, abs)
			rel = filepath.ToSlash(rel)
			if !excludedReviewPath(rel) && !seen[rel] {
				seen[rel] = true
				files = append(files, rel)
			}
			continue
		}
		err = filepath.Walk(abs, func(path string, fi os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if fi.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("include tree contains symlink: %s", path)
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if fi.IsDir() {
				if rel != "." && excludedReviewPath(rel) {
					return filepath.SkipDir
				}
				return nil
			}
			if excludedReviewPath(rel) || excludedArtifact(rel) {
				return nil
			}
			if !seen[rel] {
				seen[rel] = true
				files = append(files, rel)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(files)
	return files, nil
}

func excludedReviewPath(rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for _, p := range parts {
		switch p {
		case ".git", ".review-council", "node_modules", ".next", "dist":
			return true
		}
	}
	return false
}

func excludedArtifact(rel string) bool {
	lower := strings.ToLower(rel)
	for _, suffix := range []string{".exe", ".test", ".out", ".dll", ".so", ".dylib", ".zip", ".tar", ".gz", ".png", ".jpg", ".jpeg", ".webp", ".pdf"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

func numberLines(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	var b strings.Builder
	for i, line := range lines {
		fmt.Fprintf(&b, "L%04d %s\n", i+1, line)
	}
	return b.String()
}

func buildPrompt(root string, role Role, p Provenance, b Bundle) (string, error) {
	read := func(rel string) (string, error) {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return "", fmt.Errorf("read %s: %w", rel, err)
		}
		return string(data), nil
	}
	product, err := read("review-council/context/PRODUCT.md")
	if err != nil { return "", err }
	guardrails, err := read("review-council/context/ENGINEERING_GUARDRAILS.md")
	if err != nil { return "", err }
	status, err := read("review-council/context/CURRENT_STATUS.md")
	if err != nil { return "", err }
	lens, err := read(role.PromptPath)
	if err != nil { return "", err }
	return fmt.Sprintf(`You are one member of the BridgeXtra adversarial review council.

ROLE
%s

MANDATORY PROVENANCE TO ECHO EXACTLY AT THE START OF YOUR RESPONSE
repoPath: %s
headSHA: %s
originMainSHA: %s
migrationHead: %s
gitStatus: %s
premisesVerified: YES

If you cannot verify a premise from the supplied source packet, say so. Do not invent runtime facts.

SHARED PRODUCT CONTEXT
%s

ENGINEERING GUARDRAILS
%s

CURRENT AUDIT / TRANCHE STATUS
%s

SPECIALIST LENS
%s

CANDIDATE AND SOURCE PACKET
%s

REQUIRED FINDING FORMAT
Return NO_GENUINE_FINDINGS if you found none. Otherwise use one block per finding:
## <finding-id>
severity: CRITICAL|HIGH|MEDIUM|LOW
classificationCandidate: GENUINE_CURRENT|NEXT_TRANCHE|DORMANT|EXTERNAL|DUPLICATE|FALSE_POSITIVE|WRONG_PREMISE
premise: ...
sourceEvidence: <path:line-range plus explanation>
reachablePath: ...
failureMode: ...
moneySafetyImpact: ...
reproduction: ...
expectedREDTest: ...
suggestedFixBoundary: ...
`, role.Name, p.RepoPath, p.HeadSHA, p.OriginMainSHA, p.MigrationHead, p.GitStatus, product, guardrails, status, lens, b.Combined), nil
}
