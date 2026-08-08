package controller

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	maxConflictPaths  = 64
	maxConflictVisits = 100000
	maxConflictPath   = 256
)

func scanFolderConflicts(root string) ([]string, int, error) {
	if !filepath.IsAbs(root) {
		return nil, 0, errors.New("conflict scan requires an absolute folder")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, 0, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, 0, errors.New("conflict scan root is unsafe")
	}
	paths := []string{}
	total := 0
	visited := 0
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		visited++
		if visited > maxConflictVisits {
			return errors.New("conflict scan exceeds entry limit")
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("conflict scan encountered a symlink")
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return errors.New("conflict scan encountered unsupported content")
		}
		if !strings.Contains(entry.Name(), ".sync-conflict-") {
			return nil
		}
		total++
		if len(paths) >= maxConflictPaths {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("conflict path escaped its folder")
		}
		paths = append(paths, boundedConflictPath(relative))
		return nil
	})
	return paths, total, err
}

func boundedConflictPath(value string) string {
	var output strings.Builder
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			character = '?'
		}
		size := utf8.RuneLen(character)
		if size < 0 || output.Len()+size > maxConflictPath {
			break
		}
		output.WriteRune(character)
	}
	if output.Len() == 0 {
		return "Unnamed conflict"
	}
	return output.String()
}
