// Copyright (C) 2014 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

//go:build windows

package fs

import (
	"errors"
	"math/bits"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const optNoFollow = 0 // not defined for Windows

var (
	errNotJunction         = errors.New("not a junction")
	errJunctionTarget      = errors.New("junction target must be absolute path")
	errJunctionTargetEmpty = errors.New("junction target is empty")
)

const (
	_FSCTL_GET_REPARSE_POINT = 0x900A8
	_FSCTL_SET_REPARSE_POINT = 0x900A4
)

// readJunctionTarget reads the target of a junction using FSCTL_GET_REPARSE_POINT.
// Returns the clean target path (e.g., "C:\path") without the NT prefix.
func readJunctionTarget(path string) (string, error) {
	namep, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}

	// Open the junction with reparse point flag
	h, err := syscall.CreateFile(
		namep,
		syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_FLAG_BACKUP_SEMANTICS|syscall.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return "", err
	}
	defer syscall.CloseHandle(h)

	// Buffer for REPARSE_DATA_BUFFER - max reparse data is 16KB
	buf := make([]byte, 16*1024)
	var bytesReturned uint32

	err = syscall.DeviceIoControl(
		h,
		_FSCTL_GET_REPARSE_POINT,
		nil,
		0,
		&buf[0],
		uint32(len(buf)),
		&bytesReturned,
		nil,
	)
	if err != nil {
		return "", err
	}

	// Parse REPARSE_DATA_BUFFER
	// struct layout:
	//   ULONG  ReparseTag;           // offset 0
	//   USHORT ReparseDataLength;    // offset 4
	//   USHORT Reserved;             // offset 6
	//   USHORT SubstituteNameOffset; // offset 8
	//   USHORT SubstituteNameLength; // offset 10
	//   USHORT PrintNameOffset;      // offset 12
	//   USHORT PrintNameLength;      // offset 14
	//   WCHAR  PathBuffer[1];        // offset 16

	reparseTag := *(*uint32)(unsafe.Pointer(&buf[0]))
	if reparseTag != windows.IO_REPARSE_TAG_MOUNT_POINT {
		return "", errNotJunction
	}

	substNameOffset := *(*uint16)(unsafe.Pointer(&buf[8]))
	substNameLength := *(*uint16)(unsafe.Pointer(&buf[10]))

	// PathBuffer starts at offset 16
	pathBufStart := 16
	substStart := pathBufStart + int(substNameOffset)
	substEnd := substStart + int(substNameLength)

	if substEnd > int(bytesReturned) {
		return "", errors.New("invalid reparse data")
	}

	// Convert UTF-16 bytes to string
	substBytes := buf[substStart:substEnd]
	u16 := make([]uint16, len(substBytes)/2)
	for i := range u16 {
		u16[i] = uint16(substBytes[i*2]) | uint16(substBytes[i*2+1])<<8
	}
	target := string(utf16.Decode(u16))

	target = strings.TrimPrefix(target, `\??\`)

	if target == "" {
		return "", errJunctionTargetEmpty
	}

	return target, nil
}

// createJunction creates a junction at name pointing to target.
// target must be an absolute path.
func createJunction(target, name string) error {
	if target == "" {
		return errJunctionTargetEmpty
	}
	if !filepath.IsAbs(target) {
		return errJunctionTarget
	}

	// Create the directory for the junction
	if err := os.Mkdir(name, 0o755); err != nil {
		return err
	}

	namep, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		os.Remove(name)
		return err
	}

	// Open the directory with write access
	h, err := syscall.CreateFile(
		namep,
		syscall.GENERIC_WRITE,
		0,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_FLAG_BACKUP_SEMANTICS|syscall.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		os.Remove(name)
		return err
	}
	defer syscall.CloseHandle(h)

	// Convert target to NT path format: \??\C:\path
	ntTarget := `\??\` + target

	// Convert to UTF-16
	targetU16 := utf16.Encode([]rune(ntTarget))

	// Build REPARSE_DATA_BUFFER for mount point
	// SubstituteName = ntTarget (with null terminator)
	// PrintName = target (without NT prefix, with null terminator)
	printU16 := utf16.Encode([]rune(target))

	substNameLen := len(targetU16) * 2
	printNameLen := len(printU16) * 2
	pathBufLen := substNameLen + 2 + printNameLen + 2 // +2 for null terminators

	// Total buffer size: 8 (header) + 8 (mount point fields) + pathBufLen
	bufSize := 8 + 8 + pathBufLen
	buf := make([]byte, bufSize)

	// ReparseTag
	*(*uint32)(unsafe.Pointer(&buf[0])) = windows.IO_REPARSE_TAG_MOUNT_POINT
	// ReparseDataLength = 8 (mount point fields) + pathBufLen
	*(*uint16)(unsafe.Pointer(&buf[4])) = uint16(8 + pathBufLen)
	// Reserved = 0
	*(*uint16)(unsafe.Pointer(&buf[6])) = 0
	// SubstituteNameOffset = 0
	*(*uint16)(unsafe.Pointer(&buf[8])) = 0
	// SubstituteNameLength (without null terminator)
	*(*uint16)(unsafe.Pointer(&buf[10])) = uint16(substNameLen)
	// PrintNameOffset = substNameLen + 2 (after substitute name + null)
	*(*uint16)(unsafe.Pointer(&buf[12])) = uint16(substNameLen + 2)
	// PrintNameLength (without null terminator)
	*(*uint16)(unsafe.Pointer(&buf[14])) = uint16(printNameLen)

	// PathBuffer starts at offset 16
	pathBuf := buf[16:]

	// Write SubstituteName
	for i, c := range targetU16 {
		pathBuf[i*2] = byte(c)
		pathBuf[i*2+1] = byte(c >> 8)
	}
	// Null terminator for SubstituteName
	pathBuf[substNameLen] = 0
	pathBuf[substNameLen+1] = 0

	// Write PrintName
	printStart := substNameLen + 2
	for i, c := range printU16 {
		pathBuf[printStart+i*2] = byte(c)
		pathBuf[printStart+i*2+1] = byte(c >> 8)
	}
	// Null terminator for PrintName
	pathBuf[printStart+printNameLen] = 0
	pathBuf[printStart+printNameLen+1] = 0

	var bytesReturned uint32
	err = syscall.DeviceIoControl(
		h,
		_FSCTL_SET_REPARSE_POINT,
		&buf[0],
		uint32(len(buf)),
		nil,
		0,
		&bytesReturned,
		nil,
	)
	if err != nil {
		syscall.CloseHandle(h)
		os.Remove(name)
		return err
	}

	return nil
}

func (f *BasicFilesystem) ReadSymlink(path string) (string, error) {
	if f.junctionsAsDirs {
		return "", ErrSymlinksNotSupported
	}

	absPath, err := f.rooted(path)
	if err != nil {
		return "", err
	}

	// Check if this is a junction
	reparseTag, err := readReparseTag(absPath)
	if err != nil {
		return "", err
	}
	if !isDirectoryJunction(reparseTag) {
		return "", ErrSymlinksNotSupported
	}

	return readJunctionTarget(absPath)
}

func (f *BasicFilesystem) CreateSymlink(target, name string) error {
	if f.junctionsAsDirs {
		return ErrSymlinksNotSupported
	}

	absName, err := f.rooted(name)
	if err != nil {
		return err
	}

	// Create a junction (target must be absolute path)
	return createJunction(target, absName)
}

func (f *BasicFilesystem) Unhide(name string) error {
	name, err := f.rooted(name)
	if err != nil {
		return err
	}
	p, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return err
	}

	attrs, err := syscall.GetFileAttributes(p)
	if err != nil {
		return err
	}

	attrs &^= syscall.FILE_ATTRIBUTE_HIDDEN
	return syscall.SetFileAttributes(p, attrs)
}

func (f *BasicFilesystem) Hide(name string) error {
	name, err := f.rooted(name)
	if err != nil {
		return err
	}
	p, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return err
	}

	attrs, err := syscall.GetFileAttributes(p)
	if err != nil {
		return err
	}

	attrs |= syscall.FILE_ATTRIBUTE_HIDDEN
	return syscall.SetFileAttributes(p, attrs)
}

func (f *BasicFilesystem) Roots() ([]string, error) {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return nil, err
	}

	drives := make([]string, 0, bits.OnesCount32(mask))
	for letter := byte('A'); mask != 0; letter++ {
		if mask&1 == 1 {
			drives = append(drives, string([]byte{letter, ':', '\\'}))
		}
		mask >>= 1
	}

	return drives, nil
}

func (f *BasicFilesystem) Lchown(name, uid, gid string) error {
	name, err := f.rooted(name)
	if err != nil {
		return err
	}

	hdl, err := windows.Open(name, windows.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer windows.Close(hdl)

	// Depending on whether we got an uid or a gid, we need to set the
	// appropriate flag and parse the corresponding SID. The one we're not
	// setting remains nil, which is what we want in the call to
	// SetSecurityInfo.

	var si windows.SECURITY_INFORMATION
	var ownerSID, groupSID *windows.SID
	if uid != "" {
		ownerSID, err = windows.StringToSid(uid)
		if err == nil {
			si |= windows.OWNER_SECURITY_INFORMATION
		}
	} else if gid != "" {
		groupSID, err = windows.StringToSid(uid)
		if err == nil {
			si |= windows.GROUP_SECURITY_INFORMATION
		}
	} else {
		return errors.New("neither uid nor gid specified")
	}

	return windows.SetSecurityInfo(hdl, windows.SE_FILE_OBJECT, si, ownerSID, groupSID, nil, nil)
}

func (f *BasicFilesystem) Remove(name string) error {
	name, err := f.rooted(name)
	if err != nil {
		return err
	}
	err = os.Remove(name)
	if os.IsPermission(err) {
		// Try to remove the read-only attribute and try again
		if os.Chmod(name, 0o600) == nil {
			err = os.Remove(name)
		}
	}
	return err
}

// unrootedChecked returns the path relative to the folder root (same as
// unrooted) or an error if the given path is not a subpath and handles the
// special case when the given path is the folder root without a trailing
// pathseparator.
func (f *BasicFilesystem) unrootedChecked(absPath string, roots []string) (string, error) {
	absPath = f.resolveWin83(absPath)
	lowerAbsPath := UnicodeLowercaseNormalized(absPath)
	for _, root := range roots {
		lowerRoot := UnicodeLowercaseNormalized(root)
		if lowerAbsPath+string(PathSeparator) == lowerRoot {
			return ".", nil
		}
		if strings.HasPrefix(lowerAbsPath, lowerRoot) {
			return rel(absPath, root), nil
		}
	}
	return "", f.newErrWatchEventOutsideRoot(lowerAbsPath, roots)
}

func rel(path, prefix string) string {
	lowerRel := strings.TrimPrefix(strings.TrimPrefix(UnicodeLowercaseNormalized(path), UnicodeLowercaseNormalized(prefix)), string(PathSeparator))
	return path[len(path)-len(lowerRel):]
}

func (f *BasicFilesystem) resolveWin83(absPath string) string {
	if !isMaybeWin83(absPath) {
		return absPath
	}
	if in, err := syscall.UTF16FromString(absPath); err == nil {
		out := make([]uint16, 4*len(absPath)) // *2 for UTF16 and *2 to double path length
		if n, err := syscall.GetLongPathName(&in[0], &out[0], uint32(len(out))); err == nil {
			if n <= uint32(len(out)) {
				return syscall.UTF16ToString(out[:n])
			}
			out = make([]uint16, n)
			if _, err = syscall.GetLongPathName(&in[0], &out[0], n); err == nil {
				return syscall.UTF16ToString(out)
			}
		}
	}
	// Failed getting the long path. Return the part of the path which is
	// already a long path.
	lowerRoot := UnicodeLowercaseNormalized(f.root)
	for absPath = filepath.Dir(absPath); strings.HasPrefix(UnicodeLowercaseNormalized(absPath), lowerRoot); absPath = filepath.Dir(absPath) {
		if !isMaybeWin83(absPath) {
			return absPath
		}
	}
	return f.root
}

func isMaybeWin83(absPath string) bool {
	if !strings.Contains(absPath, "~") {
		return false
	}
	if strings.Contains(filepath.Dir(absPath), "~") {
		return true
	}
	return strings.Contains(strings.TrimPrefix(filepath.Base(absPath), WindowsTempPrefix), "~")
}

func getFinalPathName(in string) (string, error) {
	// Return the normalized path
	// Wrap the call to GetFinalPathNameByHandleW
	// The string returned by this function uses the \?\ syntax
	// Implies GetFullPathName + GetLongPathName
	inPath, err := syscall.UTF16PtrFromString(in)
	if err != nil {
		return "", err
	}
	// Get a file handle
	h, err := windows.CreateFile(inPath,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS,
		0)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)

	const VOLUME_NAME_DOS = 0x0 // not yet defined in x/sys/windows
	var bufSize uint32 = windows.MAX_PATH

	for i := 0; i < 2; i++ {
		buf := make([]uint16, bufSize)
		var ret uint32
		ret, err = windows.GetFinalPathNameByHandle(
			h, &buf[0], bufSize, VOLUME_NAME_DOS)
		// The returned value is the actual length of the norm path
		// After Win 10 build 1607, MAX_PATH limitations have been removed
		// so it is necessary to check newBufSize
		newBufSize := ret + 1
		if ret == 0 || newBufSize > bufSize*100 {
			break
		}
		if newBufSize <= bufSize {
			return syscall.UTF16ToString(buf), nil
		}
		bufSize = newBufSize
	}
	return "", err
}

func evalSymlinks(in string) (string, error) {
	out, err := filepath.EvalSymlinks(in)
	if err != nil && strings.HasPrefix(in, `\\?\`) {
		// Try again without the `\\?\` prefix
		out, err = filepath.EvalSymlinks(in[4:])
	}
	if err != nil {
		// Try to get a normalized path from Win-API
		var err1 error
		out, err1 = getFinalPathName(in)
		if err1 != nil {
			return "", err // return the prior error
		}
		// Trim UNC prefix, equivalent to
		// https://github.com/golang/go/blob/2396101e0590cb7d77556924249c26af0ccd9eff/src/os/file_windows.go#L470
		if strings.HasPrefix(out, `\\?\UNC\`) {
			out = `\` + out[7:] // path like \\server\share\...
		} else {
			out = strings.TrimPrefix(out, `\\?\`)
		}
	}
	return longFilenameSupport(out), nil
}

// watchPaths adjust the folder root for use with the notify backend and the
// corresponding absolute path to be passed to notify to watch name.
func (f *BasicFilesystem) watchPaths(name string) (string, []string, error) {
	root, err := evalSymlinks(f.root)
	if err != nil {
		return "", nil, err
	}

	// Remove `\\?\` prefix if the path is just a drive letter as a dirty
	// fix for https://github.com/syncthing/syncthing/issues/5578
	if filepath.Clean(name) == "." && len(root) <= 7 && len(root) > 4 && root[:4] == `\\?\` {
		root = root[4:]
	}

	absName, err := rooted(name, root)
	if err != nil {
		return "", nil, err
	}

	roots := []string{f.resolveWin83(root)}
	absName = f.resolveWin83(absName)

	// Events returned from fs watching are all over the place, so allow
	// both the user's input and the result of "canonicalizing" the path.
	if roots[0] != f.root {
		roots = append(roots, f.root)
	}

	return filepath.Join(absName, "..."), roots, nil
}
