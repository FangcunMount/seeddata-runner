package retirement_test

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRetiredCapabilitiesAreAbsentFromRepository(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	allowedFile := filepath.Clean(filename)
	forbidden := [][]byte{
		[]byte("historical"),
		[]byte("backfill"),
		[]byte("bbolt"),
		[]byte("batch_id"),
		[]byte("batchid"),
		[]byte("scenario_id"),
		[]byte("scenarioid"),
		[]byte("timeline"),
		[]byte("manifest"),
		[]byte("resume"),
		[]byte("replay"),
		[]byte(`yaml:"rundate"`),
		[]byte("dailysimulation.rundate"),
		[]byte("qs_historical_context_secret"),
		[]byte("x-qs-historical-"),
		[]byte("/internal/v1/historical-seed"),
		[]byte("--batch-id"),
		[]byte("--run-date"),
		[]byte("--state-dir"),
		[]byte("--from"),
		[]byte("--to"),
		[]byte("--resume"),
		[]byte("--parent-workers"),
		[]byte("--submission-workers"),
		[]byte("--report-workers"),
		[]byte("--report-queue-capacity"),
		[]byte("--pending-high-watermark"),
		[]byte("--stage-read-workers"),
		[]byte("--iam-workers"),
		[]byte("--expected-database"),
	}

	err := filepath.WalkDir(repositoryRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".seeddata-cache", "bin", "logs", "tmp":
				return filepath.SkipDir
			default:
				return nil
			}
		}
		if filepath.Clean(path) == allowedFile || !isRepositoryContractSource(path) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lower := bytes.ToLower(data)
		for _, token := range forbidden {
			if bytes.Contains(lower, token) {
				t.Errorf("%s contains retired token %q", strings.TrimPrefix(path, repositoryRoot+string(filepath.Separator)), token)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func isRepositoryContractSource(path string) bool {
	switch filepath.Ext(path) {
	case ".go", ".md", ".yaml", ".yml", ".sh", ".mod", ".sum", ".json":
		return true
	default:
		return false
	}
}
