// Copyright (C) 2026 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/syncthing/syncthing/lib/build"
	"github.com/syncthing/syncthing/lib/fs"
	"github.com/syncthing/syncthing/lib/protocol"
)

func TestWalkHardlinks(t *testing.T) {
	if !build.IsWindows {
		t.Skip("hard links are only synced on Windows")
	}

	dir := t.TempDir()
	testFS := fs.NewFilesystem(fs.FilesystemTypeBasic, dir)
	current := make(fakeCurrentFiler)

	if err := os.WriteFile(filepath.Join(dir, "testa.txt"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	expectHardlinkTargets(t, walkHardlinks(t, testFS, current, "."), map[string]string{"testa.txt": ""})

	// A new link that sorts first becomes the target of the existing name,
	// even though the watcher only reports the new name.
	link(t, dir, "testa.txt", "testa - Hardlink.txt")
	expectHardlinkTargets(t, walkHardlinks(t, testFS, current, "testa - Hardlink.txt"), map[string]string{
		"testa - Hardlink.txt": "",
		"testa.txt":            "testa - Hardlink.txt",
	})

	// Nothing changes when scanning again.
	expectHardlinkTargets(t, walkHardlinks(t, testFS, current, "."), map[string]string{})

	// A link that sorts after the target changes nothing for the others.
	link(t, dir, "testa.txt", "testb.txt")
	expectHardlinkTargets(t, walkHardlinks(t, testFS, current, "testb.txt"), map[string]string{
		"testb.txt": "testa - Hardlink.txt",
	})

	// A link in a subdirectory that sorts first moves all of them over.
	if err := os.Mkdir(filepath.Join(dir, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	link(t, dir, "testa.txt", `a\new.txt`)
	expectHardlinkTargets(t, walkHardlinks(t, testFS, current, "a"), map[string]string{
		"a":                    "",
		`a\new.txt`:            "",
		"testa - Hardlink.txt": `a\new.txt`,
		"testa.txt":            `a\new.txt`,
		"testb.txt":            `a\new.txt`,
	})
	expectHardlinkTargets(t, walkHardlinks(t, testFS, current, "."), map[string]string{})

	// A broken link isn't reported as a change.
	if err := os.Remove(filepath.Join(dir, "testb.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "testb.txt"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(dir, "testb.txt"), current["testb.txt"].ModTime(), current["testb.txt"].ModTime()); err != nil {
		t.Fatal(err)
	}
	expectHardlinkTargets(t, walkHardlinks(t, testFS, current, "."), map[string]string{})
}

func link(t *testing.T, dir, existing, name string) {
	t.Helper()
	if err := os.Link(filepath.Join(dir, existing), filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}
}

// walkHardlinks scans sub with hard link scanning enabled and records the
// results in current.
func walkHardlinks(t *testing.T, testFS fs.Filesystem, current fakeCurrentFiler, sub string) []protocol.FileInfo {
	t.Helper()
	cfg, cancel := testConfig()
	defer cancel()
	cfg.Filesystem = testFS
	cfg.Subs = []string{sub}
	cfg.CurrentFiler = current
	cfg.ScanHardlinks = true

	var files []protocol.FileInfo
	for res := range Walk(context.Background(), cfg) {
		if res.Err != nil {
			t.Fatalf("scanning %s: %v", res.Path, res.Err)
		}
		files = append(files, res.File)
	}
	for _, f := range files {
		current[f.Name] = f
	}
	return files
}

func expectHardlinkTargets(t *testing.T, files []protocol.FileInfo, expected map[string]string) {
	t.Helper()
	got := make(map[string]string)
	for _, f := range files {
		if _, ok := got[f.Name]; ok {
			t.Errorf("%s scanned twice", f.Name)
		}
		got[f.Name] = f.Platform.Windows.GetHardlinkTarget()
	}
	if len(got) != len(expected) {
		t.Errorf("scanned %v, expected %v", got, expected)
		return
	}
	for name, target := range expected {
		if gotTarget, ok := got[name]; !ok || gotTarget != target {
			t.Errorf("scanned %v, expected %v", got, expected)
			return
		}
	}
}
