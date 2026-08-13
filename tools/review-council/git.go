package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func detectMigrationHead(root string) (string, error) {
	dir := filepath.Join(root, "backend", "migrations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read migrations: %w", err)
	}
	type item struct {
		version int
		name    string
	}
	var items []item
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(e.Name(), "_")
		if !ok {
			continue
		}
		v, err := strconv.Atoi(prefix)
		if err != nil {
			continue
		}
		items = append(items, item{version: v, name: e.Name()})
	}
	if len(items) == 0 {
		return "", fmt.Errorf("no numbered SQL migrations found in %s", dir)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].version == items[j].version {
			return items[i].name < items[j].name
		}
		return items[i].version < items[j].version
	})
	return items[len(items)-1].name, nil
}

func gitOutput(ctx context.Context, root string, args ...string) (string, error) {
	full := append([]string{"-C", root}, args...)
	out, err := exec.CommandContext(ctx, "git", full...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func captureGitProvenance(ctx context.Context, root, baseRef string) (Provenance, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return Provenance{}, err
	}
	head, err := gitOutput(ctx, abs, "rev-parse", "HEAD")
	if err != nil {
		return Provenance{}, err
	}
	origin := "UNAVAILABLE"
	if v, err := gitOutput(ctx, abs, "rev-parse", "origin/main"); err == nil {
		origin = v
	}
	statusDetail, err := gitOutput(ctx, abs, "status", "--porcelain")
	if err != nil {
		return Provenance{}, err
	}
	status := "CLEAN"
	if statusDetail != "" {
		status = "DIRTY"
	}
	if baseRef == "" {
		baseRef = "HEAD^"
	}
	baseSHA, err := gitOutput(ctx, abs, "rev-parse", baseRef)
	if err != nil {
		return Provenance{}, err
	}
	migrationHead, err := detectMigrationHead(abs)
	if err != nil {
		return Provenance{}, err
	}
	return Provenance{
		RepoPath: abs, HeadSHA: head, OriginMainSHA: origin, MigrationHead: migrationHead,
		GitStatus: status, GitStatusDetail: statusDetail, BaseRef: baseRef, BaseSHA: baseSHA,
	}, nil
}
