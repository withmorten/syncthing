// Copyright (C) 2026 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

//go:build !windows

package fs

import "errors"

func HardlinkNames(Filesystem, string) ([]string, error) {
	return nil, nil
}

func CreateHardlink(Filesystem, string, string) error {
	return errors.New("hardlinks are only synced on Windows")
}
