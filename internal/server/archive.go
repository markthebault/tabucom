/*
This file owns defensive validation and extraction of uploaded ZIP sites.
It runs during publication while content is still inside a staging directory.
Path normalization, type checks, and expansion limits protect local storage.
It depends only on Go archive, filesystem, path, and HTTP primitives,
while publish.go supplies client errors and configured resource limits.
*/
package server

import (
	"archive/zip"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const maxArchivePathDepth = 50

// archiveEntry is the normalized, validated representation of one ZIP header.
// Keeping archive names in slash form until filesystem joining makes the path
// rules independent of the host operating system.
type archiveEntry struct {
	name        string
	isDirectory bool
}

// extractZip expands an already size-limited upload into its staging directory.
// The caller commits that directory only after this method and the root-index
// check succeed, so partially extracted archives never become visible.
func (s *Server) extractZip(destination, archivePath string) (int, int64, error) {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return 0, 0, invalidZip("body is not a valid ZIP archive")
	}
	defer reader.Close()

	// Normalized names, not raw header names, define uniqueness. This prevents
	// aliases such as index.html and a/../index.html from overwriting each other.
	seen := make(map[string]struct{}, len(reader.File))
	fileCount := 0
	entryCount := 0
	var expandedBytes int64

	for _, file := range reader.File {
		entry, err := validateArchiveEntry(file, seen)
		if err != nil {
			return 0, 0, err
		}

		entryCount++
		if entryCount > s.cfg.MaxFiles {
			return 0, 0, newPublishError(http.StatusRequestEntityTooLarge, "too_many_files", "ZIP exceeds entry count limit")
		}

		target := filepath.Join(destination, filepath.FromSlash(entry.name))
		if entry.isDirectory {
			if err := os.MkdirAll(target, 0750); err != nil {
				return 0, expandedBytes, invalidZip("ZIP contains conflicting paths")
			}
			continue
		}

		fileCount++
		if exceedsExpandedLimit(expandedBytes, file.UncompressedSize64, s.cfg.MaxExpandedSize) {
			return 0, 0, newPublishError(http.StatusRequestEntityTooLarge, "upload_too_large", "ZIP exceeds expanded size limit")
		}

		written, err := extractArchiveFile(file, target, s.cfg.MaxExpandedSize-expandedBytes)
		if err != nil {
			return 0, expandedBytes, err
		}
		expandedBytes += written
		if expandedBytes > s.cfg.MaxExpandedSize {
			// Header sizes are advisory. The streaming limit remains authoritative
			// against malformed archives that understate their expanded size.
			return 0, 0, newPublishError(http.StatusRequestEntityTooLarge, "upload_too_large", "ZIP exceeds expanded size limit")
		}
	}

	return fileCount, expandedBytes, nil
}

// validateArchiveEntry rejects path traversal, absolute paths, excessive depth,
// duplicate normalized paths, links, devices, sockets, and other special files.
func validateArchiveEntry(file *zip.File, seen map[string]struct{}) (archiveEntry, error) {
	rawName := strings.ReplaceAll(file.Name, "\\", "/")
	cleanName := path.Clean(rawName)

	if unsafeArchivePath(rawName, cleanName) {
		return archiveEntry{}, invalidZip("ZIP contains an unsafe path")
	}
	if strings.Count(cleanName, "/") > maxArchivePathDepth {
		return archiveEntry{}, invalidZip("ZIP path nesting is too deep")
	}

	// path.Clean removes a trailing slash, so directory-ness is captured before
	// the normalized name is used for duplicate detection.
	isDirectory := strings.HasSuffix(rawName, "/") || file.Mode().IsDir()
	key := strings.TrimSuffix(cleanName, "/")
	if _, exists := seen[key]; exists {
		return archiveEntry{}, invalidZip("ZIP contains duplicate normalized paths")
	}
	seen[key] = struct{}{}

	mode := file.Mode()
	if mode&os.ModeSymlink != 0 || mode&os.ModeType != 0 && !mode.IsDir() {
		return archiveEntry{}, invalidZip("ZIP links and special files are not allowed")
	}

	return archiveEntry{name: cleanName, isDirectory: isDirectory}, nil
}

// unsafeArchivePath recognizes both slash-normalized POSIX paths and Windows
// drive paths. NUL bytes are forbidden even on filesystems that reject them.
func unsafeArchivePath(rawName, cleanName string) bool {
	windowsAbsolute := len(rawName) >= 3 && isASCIILetter(rawName[0]) &&
		rawName[1] == ':' && rawName[2] == '/'

	return rawName == "" ||
		strings.HasPrefix(rawName, "/") ||
		cleanName == "." ||
		cleanName == ".." ||
		strings.HasPrefix(cleanName, "../") ||
		path.IsAbs(cleanName) ||
		windowsAbsolute ||
		strings.ContainsRune(rawName, 0)
}

// isASCIILetter is intentionally narrower than Unicode letter classification;
// Windows drive prefixes are defined by ASCII drive letters.
func isASCIILetter(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

// exceedsExpandedLimit checks the declared size without overflowing uint64 or
// subtracting past zero. Configuration validation guarantees a positive limit.
func exceedsExpandedLimit(current int64, declared uint64, limit int64) bool {
	return declared > uint64(limit) || current > limit-int64(declared)
}

// extractArchiveFile creates parents and the destination with O_EXCL. This makes
// filesystem conflicts fail closed even if a malformed archive bypasses logical
// duplicate detection through file-versus-directory collisions.
func extractArchiveFile(file *zip.File, target string, remaining int64) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(target), 0750); err != nil {
		return 0, invalidZip("ZIP contains conflicting paths")
	}

	input, err := file.Open()
	if err != nil {
		return 0, invalidZip("ZIP entry cannot be read")
	}
	defer input.Close()

	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0640)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return 0, invalidZip("ZIP contains conflicting paths")
		}
		return 0, err
	}

	// Reading one byte beyond the remaining budget detects dishonest ZIP headers.
	written, copyErr := io.Copy(output, io.LimitReader(input, remaining+1))
	closeErr := output.Close()
	if copyErr != nil || closeErr != nil {
		return written, errors.Join(copyErr, closeErr)
	}
	return written, nil
}

// invalidZip creates the stable client-visible error for structural ZIP issues.
func invalidZip(message string) error {
	return newPublishError(http.StatusBadRequest, "invalid_archive", message)
}
