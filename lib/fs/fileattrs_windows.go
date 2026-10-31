// Copyright (C) 2026 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

//go:build windows

package fs

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// Read-only is synced through the permission bits, Archive deliberately not at all.
const syncedWindowsAttributes = windows.FILE_ATTRIBUTE_HIDDEN | windows.FILE_ATTRIBUTE_SYSTEM

// WindowsAttributes returns the synced attribute bits of a file as returned by Lstat.
func WindowsAttributes(fi FileInfo) uint32 {
	if data, ok := fi.Sys().(*syscall.Win32FileAttributeData); ok {
		return data.FileAttributes & syncedWindowsAttributes
	}
	return 0
}

// SetWindowsAttributes sets the synced attribute bits of name, leaving all others as they are.
func SetWindowsAttributes(fsys Filesystem, name string, attrs uint32) error {
	basic, ok := unwrapFilesystem[*BasicFilesystem](fsys)
	if !ok {
		return nil
	}
	path, err := basic.rooted(name)
	if err != nil {
		return err
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	cur, err := windows.GetFileAttributes(p)
	if err != nil {
		return err
	}
	want := cur&^syncedWindowsAttributes | attrs&syncedWindowsAttributes
	if want == cur {
		return nil
	}
	return windows.SetFileAttributes(p, want)
}
