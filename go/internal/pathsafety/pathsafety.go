package pathsafety

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CanonicalizeError reports a path canonicalization failure.
type CanonicalizeError struct {
	Path   string
	Reason error
}

func (e *CanonicalizeError) Error() string {
	return fmt.Sprintf("path_canonicalize_failed path=%s: %v", e.Path, e.Reason)
}

func (e *CanonicalizeError) Unwrap() error {
	return e.Reason
}

// Canonicalize expands the path, resolves any existing symlink components, and preserves
// non-existent trailing components so callers can validate paths before creation.
func Canonicalize(path string) (string, error) {
	expanded, err := filepath.Abs(path)
	if err != nil {
		return "", &CanonicalizeError{Path: path, Reason: err}
	}

	root, segments := splitAbsolutePath(expanded)
	canonical, err := resolveSegments(root, nil, segments)
	if err != nil {
		return "", &CanonicalizeError{Path: expanded, Reason: err}
	}

	return canonical, nil
}

func splitAbsolutePath(path string) (string, []string) {
	cleaned := filepath.Clean(path)
	volume := filepath.VolumeName(cleaned)
	rest := strings.TrimPrefix(cleaned, volume)
	root := string(os.PathSeparator)
	if volume != "" {
		root = volume + root
	}

	rest = strings.TrimPrefix(rest, string(os.PathSeparator))
	if rest == "" {
		return root, nil
	}

	return root, strings.Split(rest, string(os.PathSeparator))
}

func resolveSegments(root string, resolved, remaining []string) (string, error) {
	if len(remaining) == 0 {
		return joinPath(root, resolved), nil
	}

	segment := remaining[0]
	candidate := joinPath(root, append(append([]string(nil), resolved...), segment))

	info, err := os.Lstat(candidate)
	switch {
	case err == nil && info.Mode()&os.ModeSymlink != 0:
		target, readErr := os.Readlink(candidate)
		if readErr != nil {
			return "", readErr
		}

		base := joinPath(root, resolved)
		if !filepath.IsAbs(target) {
			target = filepath.Join(base, target)
		}

		targetRoot, targetSegments := splitAbsolutePath(target)
		return resolveSegments(targetRoot, nil, append(targetSegments, remaining[1:]...))
	case err == nil:
		return resolveSegments(root, append(resolved, segment), remaining[1:])
	case os.IsNotExist(err):
		return joinPath(root, append(append([]string(nil), resolved...), remaining...)), nil
	default:
		return "", err
	}
}

func joinPath(root string, segments []string) string {
	current := root
	for _, segment := range segments {
		current = filepath.Join(current, segment)
	}
	return current
}
