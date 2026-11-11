package transfer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

const (
	maxPathDepth = 32
	maxPathBytes = 1024
	tempPrefix   = ".godeck-upload-"
)

func validName(name string) bool {
	if name == "" || len(name) > 240 || name == "." || name == ".." || filepath.Base(name) != name ||
		strings.ContainsAny(name, `/\<>:"|?*`+"\x00") || strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return false
	}
	if strings.HasPrefix(name, tempPrefix) {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	stem := strings.ToUpper(strings.TrimRight(strings.SplitN(name, ".", 2)[0], " "))
	switch stem {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9",
		"COM¹", "COM²", "COM³", "LPT¹", "LPT²", "LPT³":
		return false
	}
	return true
}

// splitPath validates a wire path: '/'-separated components, each a valid
// file name. The empty string is the share root and yields no components.
func splitPath(p string) ([]string, bool) {
	if p == "" {
		return nil, true
	}
	if len(p) > maxPathBytes {
		return nil, false
	}
	parts := strings.Split(p, "/")
	if len(parts) > maxPathDepth {
		return nil, false
	}
	for _, c := range parts {
		if !validName(c) {
			return nil, false
		}
	}
	return parts, true
}

// JoinPath joins wire path components.
func JoinPath(parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "/")
}

// ParentPath returns the parent of a wire path ("" for top-level entries).
func ParentPath(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[:i]
	}
	return ""
}

// BaseName returns the last component of a wire path.
func BaseName(p string) string {
	return p[strings.LastIndex(p, "/")+1:]
}

// isPlainDir reports whether info is a real directory, not a symlink,
// junction or other reparse point.
func isPlainDir(info os.FileInfo) bool {
	return info.IsDir() && info.Mode()&(os.ModeSymlink|os.ModeIrregular) == 0
}

// resolveDir walks root/parts one component at a time, refusing symlinks and
// reparse points. With create, missing directories are made (0700).
func resolveDir(root string, parts []string, create bool) (string, error) {
	dir := root
	if create {
		if err := os.MkdirAll(root, 0700); err != nil {
			return "", err
		}
	}
	info, err := os.Lstat(dir)
	if err != nil || !isPlainDir(info) {
		return "", errors.New("共享目录不可用")
	}
	for _, c := range parts {
		next := filepath.Join(dir, c)
		info, err = os.Lstat(next)
		if os.IsNotExist(err) && create {
			if err = os.Mkdir(next, 0700); err != nil && !os.IsExist(err) {
				return "", fmt.Errorf("无法创建目录 %s", c)
			}
			info, err = os.Lstat(next)
		}
		if err != nil {
			return "", errors.New("目录不存在")
		}
		if !isPlainDir(info) {
			return "", errors.New("路径包含非普通目录（链接或文件）")
		}
		dir = next
	}
	return dir, nil
}

// uniqueName returns name, or "stem (n).ext" for the n-th attempt.
func uniqueName(name string, n int) string {
	if n == 0 {
		return name
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if stem == "" { // dotfile such as ".bashrc"
		stem, ext = name, ""
	}
	return fmt.Sprintf("%s (%d)%s", stem, n, ext)
}

// commitUnique moves the finished temp file into dir without ever replacing an
// existing entry. On a name clash it picks "name (n).ext". It returns the
// final file name.
func commitUnique(src, dir, name string) (string, error) {
	for n := 0; n < 1000; n++ {
		candidate := uniqueName(name, n)
		dst := filepath.Join(dir, candidate)
		err := os.Link(src, dst)
		if err == nil {
			return candidate, nil
		}
		if _, statErr := os.Lstat(dst); statErr == nil {
			continue // taken, try the next name
		}
		// Hard links unsupported (e.g. FAT/exFAT): reserve the name atomically,
		// then replace only our own empty placeholder.
		f, createErr := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if createErr != nil {
			if os.IsExist(createErr) {
				continue
			}
			return "", errors.New("无法保存文件：" + createErr.Error())
		}
		f.Close()
		if renErr := os.Rename(src, dst); renErr != nil {
			_ = os.Remove(dst)
			return "", errors.New("无法保存文件：" + renErr.Error())
		}
		return candidate, nil
	}
	return "", errors.New("同名文件过多，无法保存")
}
