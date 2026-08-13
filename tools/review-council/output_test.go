package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func expectedProv() Provenance {
	return Provenance{RepoPath: `C:\Users\sanus\BridgeXtra`, HeadSHA: "abc123", OriginMainSHA: "def456", MigrationHead: "0085_test.sql", GitStatus: "CLEAN", BaseRef: "HEAD^", BaseSHA: "base123", CandidateSHA256: "cand", SourceSHA256: "src"}
}

func validReviewerBody(p Provenance) string {
	return "repoPath: " + p.RepoPath + "\n" +
		"headSHA: " + p.HeadSHA + "\n" +
		"originMainSHA: " + p.OriginMainSHA + "\n" +
		"migrationHead: " + p.MigrationHead + "\n" +
		"gitStatus: " + p.GitStatus + "\n" +
		"premisesVerified: YES\n\n## BX-TEST-1\nseverity: HIGH\nclassificationCandidate: GENUINE_CURRENT\npremise: x\n"
}

func TestValidateReviewerProvenance(t *testing.T) {
	p := expectedProv()
	if err := validateReviewerProvenance(validReviewerBody(p), p); err != nil {
		t.Fatalf("valid rejected: %v", err)
	}
	cases := map[string]string{
		"wrong head":      strings.Replace(validReviewerBody(p), "headSHA: abc123", "headSHA: wrong", 1),
		"wrong migration": strings.Replace(validReviewerBody(p), "migrationHead: 0085_test.sql", "migrationHead: 0073_old.sql", 1),
		"not verified":    strings.Replace(validReviewerBody(p), "premisesVerified: YES", "premisesVerified: NO", 1),
		"missing status":  strings.Replace(validReviewerBody(p), "gitStatus: CLEAN\n", "", 1),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if err := validateReviewerProvenance(body, p); err == nil {
				t.Fatal("expected provenance failure")
			}
		})
	}
}

func TestBuildAdjudicationPacketExcludesInvalidLens(t *testing.T) {
	p := expectedProv()
	good := ReviewerResult{Role: roleRegistry[0], Content: validReviewerBody(p), ValidProvenance: true}
	bad := ReviewerResult{Role: roleRegistry[1], Content: "wrong", ValidProvenance: false, Error: "INVALID_PROVENANCE"}
	packet := buildAdjudicationPacket(p, []ReviewerResult{good, bad}, "ADJUDICATE-RULES")
	if !strings.Contains(packet, "BX-TEST-1") || !strings.Contains(packet, "ADJUDICATE-RULES") {
		t.Fatalf("valid output/rules missing:\n%s", packet)
	}
	if strings.Contains(packet, "\nwrong\n") {
		t.Fatalf("invalid reviewer content leaked into packet:\n%s", packet)
	}
	if !strings.Contains(packet, "security") {
		t.Fatalf("missing lens not named:\n%s", packet)
	}
}

func TestArtifactsNeverPersistOpenRouterKey(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "SECRET-SENTINEL-KEY")
	p := expectedProv()
	out := t.TempDir()
	b := Bundle{Combined: "source SECRET-SENTINEL-KEY", SHA256: "hash"}
	prompts := map[string]string{"correctness": "prompt SECRET-SENTINEL-KEY"}
	results := []ReviewerResult{{Role: roleRegistry[0], Content: validReviewerBody(p) + " SECRET-SENTINEL-KEY", ValidProvenance: true}}
	if err := writeRunArtifacts(out, "x-ai/grok-4.6", p, b, prompts, results, false, "rules"); err != nil {
		t.Fatal(err)
	}
	err := filepath.Walk(out, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), "SECRET-SENTINEL-KEY") {
			t.Fatalf("secret persisted in %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
