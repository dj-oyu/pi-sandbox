package fsview

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const maxPreviewBytes = 2 * 1024 * 1024

var defaultSkipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"target":       true,
	"__pycache__":  true,
	".venv":        true,
	"dist":         true,
}

type TreeEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	ModTime string `json:"modTime"`
}

func ResolveSafe(workspace, relPath string) (string, string, error) {
	if workspace == "" {
		return "", "", errors.New("PI_WORKSPACE is not set")
	}
	absRoot, err := filepath.Abs(workspace)
	if err != nil {
		return "", "", err
	}
	cleanRel := filepath.Clean(filepath.FromSlash(strings.TrimSpace(relPath)))
	if cleanRel == "" || cleanRel == "." {
		return absRoot, "", nil
	}
	if filepath.IsAbs(cleanRel) || strings.HasPrefix(cleanRel, "..") {
		return "", "", os.ErrPermission
	}
	target := filepath.Join(absRoot, cleanRel)
	relCheck, err := filepath.Rel(absRoot, target)
	if err != nil || strings.HasPrefix(relCheck, "..") {
		return "", "", os.ErrPermission
	}
	// Resolve symlinks if target exists to prevent symlink escape
	if evalRoot, err1 := filepath.EvalSymlinks(absRoot); err1 == nil {
		if evalTarget, err2 := filepath.EvalSymlinks(target); err2 == nil {
			if r, err3 := filepath.Rel(evalRoot, evalTarget); err3 != nil || strings.HasPrefix(r, "..") {
				return "", "", os.ErrPermission
			}
		}
	}
	return target, filepath.ToSlash(relCheck), nil
}

func ListDir(workspace, relDir string, includeHidden bool) ([]TreeEntry, error) {
	target, normRel, err := ResolveSafe(workspace, relDir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return nil, err
	}
	var out []TreeEntry
	for _, de := range entries {
		name := de.Name()
		if !includeHidden && de.IsDir() && defaultSkipDirs[name] {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		itemPath := name
		if normRel != "" {
			itemPath = normRel + "/" + name
		}
		out = append(out, TreeEntry{
			Name:    name,
			Path:    itemPath,
			IsDir:   de.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime().UTC().Format(time.RFC3339),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

func ReadFilePreview(workspace, relPath string) ([]byte, int64, bool, error) {
	target, _, err := ResolveSafe(workspace, relPath)
	if err != nil {
		return nil, 0, false, err
	}
	st, err := os.Stat(target)
	if err != nil {
		return nil, 0, false, err
	}
	if st.IsDir() {
		return nil, 0, false, errors.New("path is a directory")
	}
	f, err := os.Open(target)
	if err != nil {
		return nil, 0, false, err
	}
	defer f.Close()

	truncated := st.Size() > maxPreviewBytes
	data, err := io.ReadAll(io.LimitReader(f, maxPreviewBytes))
	if err != nil {
		return nil, 0, false, err
	}
	return data, st.Size(), truncated, nil
}

// ListVisibleFiles walks workspace and returns sorted non-hidden, non-ignored relative file paths.
func ListVisibleFiles(workspace string) ([]string, error) {
	absRoot, _, err := ResolveSafe(workspace, "")
	if err != nil {
		return nil, err
	}
	var out []string
	_ = filepath.WalkDir(absRoot, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if p != absRoot && strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() && defaultSkipDirs[name] {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(absRoot, p)
		if err != nil {
			return nil
		}
		out = append(out, filepath.ToSlash(rel))
		if len(out) >= 500 {
			return filepath.SkipAll
		}
		return nil
	})
	sort.Strings(out)
	return out, nil
}
