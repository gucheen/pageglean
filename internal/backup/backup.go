package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"pageglean/internal/store"
)

type Manifest struct {
	FormatVersion int       `json:"formatVersion"`
	CreatedAt     time.Time `json:"createdAt"`
}

func Create(ctx context.Context, data *store.Store, dataDir, output string) error {
	if _, err := os.Stat(output); err == nil {
		return fmt.Errorf("backup file already exists: %s", output)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tempDir, err := os.MkdirTemp("", "pageglean-backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)
	snapshot := filepath.Join(tempDir, "pageglean.db")
	if err := data.BackupDatabase(ctx, snapshot); err != nil {
		return err
	}

	file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		_ = file.Close()
		if !committed {
			_ = os.Remove(output)
		}
	}()
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	closeWriters := func() error {
		if err := tarWriter.Close(); err != nil {
			return err
		}
		if err := gzipWriter.Close(); err != nil {
			return err
		}
		return file.Close()
	}

	manifest, _ := json.MarshalIndent(Manifest{FormatVersion: 1, CreatedAt: time.Now().UTC()}, "", "  ")
	if err := addBytes(tarWriter, "manifest.json", manifest); err != nil {
		return err
	}
	if err := addFile(tarWriter, snapshot, "pageglean.db"); err != nil {
		return err
	}
	blobDir := filepath.Join(dataDir, "blobs")
	if _, err := os.Stat(blobDir); err == nil {
		if err := filepath.WalkDir(blobDir, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("refusing to back up symlink: %s", path)
			}
			relative, err := filepath.Rel(dataDir, path)
			if err != nil {
				return err
			}
			return addFile(tarWriter, path, filepath.ToSlash(relative))
		}); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := closeWriters(); err != nil {
		return err
	}
	committed = true
	return nil
}

func Verify(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("open backup compression: %w", err)
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	foundManifest := false
	foundDatabase := false
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read backup: %w", err)
		}
		clean := filepath.Clean(header.Name)
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("unsafe backup entry: %s", header.Name)
		}
		switch filepath.ToSlash(clean) {
		case "manifest.json":
			var manifest Manifest
			if err := json.NewDecoder(io.LimitReader(reader, 64<<10)).Decode(&manifest); err != nil {
				return fmt.Errorf("decode backup manifest: %w", err)
			}
			if manifest.FormatVersion != 1 {
				return fmt.Errorf("unsupported backup format version %d", manifest.FormatVersion)
			}
			foundManifest = true
		case "pageglean.db":
			headerBytes := make([]byte, 16)
			if _, err := io.ReadFull(reader, headerBytes); err != nil {
				return fmt.Errorf("read SQLite snapshot: %w", err)
			}
			if string(headerBytes) != "SQLite format 3\x00" {
				return fmt.Errorf("backup contains an invalid SQLite snapshot")
			}
			foundDatabase = true
		}
	}
	if !foundManifest || !foundDatabase {
		return fmt.Errorf("backup is missing manifest.json or a PageGlean SQLite database")
	}
	return nil
}

func addBytes(writer *tar.Writer, name string, content []byte) error {
	header := &tar.Header{Name: name, Mode: 0o600, Size: int64(len(content)), ModTime: time.Now().UTC()}
	if err := writer.WriteHeader(header); err != nil {
		return err
	}
	_, err := writer.Write(content)
	return err
}

func addFile(writer *tar.Writer, path, name string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	header := &tar.Header{Name: name, Mode: 0o600, Size: info.Size(), ModTime: info.ModTime()}
	if err := writer.WriteHeader(header); err != nil {
		return err
	}
	_, err = io.Copy(writer, file)
	return err
}
