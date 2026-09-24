package sessionfiles

import (
	"errors"
	"os"
	"path/filepath"
)

// SyncDirectory 把已完成的目录条目变更同步到磁盘。
func SyncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

// WriteFile 以同步临时文件和原子替换更新原生状态元数据。
func WriteFile(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".fork-state-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(data)
	if err = errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		return err
	}
	if err = os.Rename(file.Name(), path); err != nil {
		return err
	}
	return SyncDirectory(filepath.Dir(path))
}
