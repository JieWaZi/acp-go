// Package sessionfiles 提供原生分支重绑定所需的独立文件复制。
package sessionfiles

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// CopyTree 独占创建目标树，不跟随原生状态目录里的软链接。
func CopyTree(source, target string) error {
	if err := os.Mkdir(target, 0700); err != nil {
		return err
	}
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			return os.Mkdir(destination, 0700)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("native session tree contains a symbolic link")
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		defer input.Close()
		info, err := input.Stat()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("native session entry is not a regular file")
		}
		output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, input)
		return errors.Join(copyErr, output.Sync(), output.Close())
	})
	if err != nil {
		return err
	}
	return filepath.WalkDir(target, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return SyncDirectory(path)
		}
		return nil
	})
}
