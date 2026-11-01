// Copyright (C) 2026 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

//go:build windows

package fs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

const volumeNameNone = 0x4 // GetFinalPathNameByHandle flag, not defined in x/sys/windows

var (
	errHardlinksNotSupported = errors.New("hardlinks not supported on this filesystem")

	procFindFirstFileNameW = windows.NewLazySystemDLL("kernel32.dll").NewProc("FindFirstFileNameW")
	procFindNextFileNameW  = windows.NewLazySystemDLL("kernel32.dll").NewProc("FindNextFileNameW")

	volumeRelativeRoots sync.Map // filesystem root -> root path relative to its volume
)

// HardlinkNames returns the root-relative names of the other hard links to
// name that are located inside the filesystem root.
func HardlinkNames(fsys Filesystem, name string) ([]string, error) {
	basic, ok := unwrapFilesystem[*BasicFilesystem](fsys)
	if !ok {
		return nil, nil
	}
	path, err := basic.rooted(name)
	if err != nil {
		return nil, err
	}
	links, err := findFileNames(path)
	if err != nil || len(links) < 2 {
		return nil, err
	}
	root, err := volumeRelativeRoot(basic.root)
	if err != nil {
		return nil, err
	}

	// Link names are relative to the volume, e.g. \folder\dir\file.
	prefix := strings.TrimSuffix(root, `\`) + `\`
	self := filepath.Clean(name)
	var others []string
	for _, link := range links {
		if len(link) <= len(prefix) || !strings.EqualFold(link[:len(prefix)], prefix) {
			continue
		}
		if rel := link[len(prefix):]; !strings.EqualFold(rel, self) {
			others = append(others, rel)
		}
	}
	return others, nil
}

// CreateHardlink creates name as a hard link to the existing file.
func CreateHardlink(fsys Filesystem, existing, name string) error {
	basic, ok := unwrapFilesystem[*BasicFilesystem](fsys)
	if !ok {
		return errHardlinksNotSupported
	}
	existingPath, err := basic.rooted(existing)
	if err != nil {
		return err
	}
	namePath, err := basic.rooted(name)
	if err != nil {
		return err
	}
	return os.Link(existingPath, namePath)
}

// findFileNames lists the volume-relative names of all hard links to path.
func findFileNames(path string) ([]string, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	buf := make([]uint16, windows.MAX_PATH)

	var h uintptr
	for {
		n := uint32(len(buf))
		var errno error
		h, _, errno = procFindFirstFileNameW.Call(uintptr(unsafe.Pointer(p)), 0, uintptr(unsafe.Pointer(&n)), uintptr(unsafe.Pointer(&buf[0])))
		if windows.Handle(h) != windows.InvalidHandle {
			break
		}
		if !errors.Is(errno, windows.ERROR_MORE_DATA) {
			return nil, errno
		}
		buf = make([]uint16, n)
	}
	defer windows.FindClose(windows.Handle(h))

	names := []string{windows.UTF16ToString(buf)}
	for {
		n := uint32(len(buf))
		ok, _, errno := procFindNextFileNameW.Call(h, uintptr(unsafe.Pointer(&n)), uintptr(unsafe.Pointer(&buf[0])))
		switch {
		case ok != 0:
			names = append(names, windows.UTF16ToString(buf))
		case errors.Is(errno, windows.ERROR_MORE_DATA):
			buf = make([]uint16, n)
		case errors.Is(errno, windows.ERROR_HANDLE_EOF):
			return names, nil
		default:
			return nil, errno
		}
	}
}

// volumeRelativeRoot returns root as a path relative to its volume, the
// same form FindFirstFileNameW returns link names in.
func volumeRelativeRoot(root string) (string, error) {
	if rel, ok := volumeRelativeRoots.Load(root); ok {
		return rel.(string), nil
	}
	p, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return "", err
	}
	h, err := windows.CreateFile(p, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)

	buf := make([]uint16, windows.MAX_PATH)
	for {
		n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), volumeNameNone)
		if err != nil {
			return "", err
		}
		if int(n) < len(buf) {
			rel := windows.UTF16ToString(buf[:n])
			volumeRelativeRoots.Store(root, rel)
			return rel, nil
		}
		buf = make([]uint16, n+1)
	}
}
