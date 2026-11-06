package pi

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// snapshotWorkspaceRoot captures Go's FileInfo and libuv-compatible volume/file
// IDs from the same short-lived handle. File.Stat eagerly records Go's file ID;
// os.Stat alone can defer that identity read until a later os.SameFile call.
func snapshotWorkspaceRoot(path string) (os.FileInfo, string, string, error) {
	// Canonical roots are absolute. Use the extended namespace so identity
	// capture does not narrow Go's existing support for long drive/UNC paths.
	nativePath := path
	if !strings.HasPrefix(nativePath, `\\?\`) {
		if strings.HasPrefix(nativePath, `\\`) {
			nativePath = `\\?\UNC\` + nativePath[2:]
		} else {
			nativePath = `\\?\` + nativePath
		}
	}
	name, err := syscall.UTF16PtrFromString(nativePath)
	if err != nil {
		return nil, "", "", err
	}
	handle, err := syscall.CreateFile(name, 0,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, "", "", err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = syscall.CloseHandle(handle)
		return nil, "", "", os.ErrInvalid
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, "", "", err
	}
	var identity syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(handle, &identity); err != nil {
		return info, "", "", nil
	}
	// libuv uses the volume serial's low 32 bits and the complete 64-bit FileId.
	inode := uint64(identity.FileIndexHigh)<<32 | uint64(identity.FileIndexLow)
	return info, strconv.FormatUint(uint64(identity.VolumeSerialNumber), 10), strconv.FormatUint(inode, 10), nil
}
