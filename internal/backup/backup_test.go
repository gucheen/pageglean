package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pageglean/internal/store"
)

func TestCreateAndVerify(t *testing.T) {
	dataDir := t.TempDir()
	data, err := store.Open(filepath.Join(dataDir, "pageglean.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	bookmark, _, err := data.CreateBookmark(context.Background(), store.Bookmark{
		URL: "https://example.com", CanonicalURL: "https://example.com/", Title: "Backup test",
	})
	if err != nil {
		t.Fatal(err)
	}
	const contentHash = "abcdef"
	if err := data.CompleteArchive(context.Background(), bookmark.ID, store.ArchiveContent{
		Text: "archived body", Path: "blobs/ab/abcdef.html.gz", Hash: contentHash,
	}); err != nil {
		t.Fatal(err)
	}
	blobDir := filepath.Join(dataDir, "blobs", "ab")
	if err := os.MkdirAll(blobDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blobDir, "abcdef.html.gz"), []byte("blob"), 0o640); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := Create(context.Background(), data, dataDir, output); err != nil {
		t.Fatal(err)
	}
	if err := Verify(output); err != nil {
		t.Fatal(err)
	}
	restoreDir := t.TempDir()
	extractBackupForTest(t, output, restoreDir)
	restored, err := store.Open(filepath.Join(restoreDir, "pageglean.db"))
	if err != nil {
		t.Fatal(err)
	}
	stats, err := restored.Stats(context.Background())
	if err != nil {
		restored.Close()
		t.Fatal(err)
	}
	if stats.Bookmarks != 1 {
		t.Fatalf("restored bookmark count = %d, want 1", stats.Bookmarks)
	}
	restoredBookmark, err := restored.GetBookmark(context.Background(), bookmark.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restoredBookmark.ContentHash != contentHash {
		t.Fatalf("restored content hash = %q, want %q", restoredBookmark.ContentHash, contentHash)
	}
	if err := restored.Close(); err != nil {
		t.Fatal(err)
	}
	restoredBlob, err := os.ReadFile(filepath.Join(restoreDir, "blobs", "ab", "abcdef.html.gz"))
	if err != nil {
		t.Fatal(err)
	}
	if string(restoredBlob) != "blob" {
		t.Fatalf("restored blob = %q, want blob", restoredBlob)
	}
	if err := Create(context.Background(), data, dataDir, output); err == nil {
		t.Fatal("backup unexpectedly overwrote an existing file")
	}
}

func extractBackupForTest(t *testing.T, source, destination string) {
	t.Helper()
	file, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		clean := filepath.Clean(header.Name)
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			t.Fatalf("unsafe archive path %q", header.Name)
		}
		target := filepath.Join(destination, clean)
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			t.Fatal(err)
		}
		output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		_, copyErr := io.Copy(output, reader)
		closeErr := output.Close()
		if copyErr != nil {
			t.Fatal(copyErr)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
	}
}

func TestVerifyRejectsInvalidArchive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.tar.gz")
	if err := os.WriteFile(path, []byte("not a backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Verify(path); err == nil {
		t.Fatal("invalid backup was accepted")
	}
}
