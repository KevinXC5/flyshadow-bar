package cliinstall

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestShouldAutoInstall(t *testing.T) {
	cases := []struct {
		name string
		path string
		goos string
		want bool
	}{
		{"macOS Applications bundle", "/Applications/FlyShadow Bar.app/Contents/MacOS/FlyShadow Bar", "darwin", true},
		{"macOS user Applications bundle", "/Users/alice/Applications/FlyShadow Bar.app/Contents/MacOS/FlyShadow Bar", "darwin", true},
		{"macOS mounted DMG skipped", "/Volumes/FlyShadow Bar/FlyShadow Bar.app/Contents/MacOS/FlyShadow Bar", "darwin", false},
		{"macOS go run temp skipped", "/var/folders/xx/go-build123/b001/exe/fsbar", "darwin", false},
		{"macOS plain binary skipped", "/Users/alice/src/flyshadow-bar/bin/fsbar", "darwin", false},
		{"macOS tmp skipped", "/tmp/fsbar-test/FlyShadow Bar.app/Contents/MacOS/FlyShadow Bar", "darwin", false},
		{"windows installed exe", `C:\Users\alice\AppData\Local\Programs\FlyShadow Bar\FlyShadow Bar.exe`, "windows", true},
		{"windows go-build skipped", `C:\Users\alice\AppData\Local\Temp\go-build123\b001\exe\fsbar.exe`, "windows", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldAutoInstallForOS(tc.path, tc.goos); got != tc.want {
				t.Fatalf("shouldAutoInstallForOS(%q, %q) = %v, want %v", tc.path, tc.goos, got, tc.want)
			}
		})
	}
}

func TestUnixSymlinkInstallAndUpdate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink lifecycle test runs on unix")
	}

	root := t.TempDir()
	binDir := filepath.Join(root, "bin1")
	altDir := filepath.Join(root, "bin2")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(altDir, 0o755); err != nil {
		t.Fatal(err)
	}

	exeV1 := filepath.Join(root, "v1", "FlyShadow Bar.app", "Contents", "MacOS", "FlyShadow Bar")
	exeV2 := filepath.Join(root, "v2", "FlyShadow Bar.app", "Contents", "MacOS", "FlyShadow Bar")
	for _, p := range []string{exeV1, exeV2} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("bin"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// 1. 首次安装：在第一个可写候选目录创建软链
	st, err := Ensure(Options{
		ExecutablePath: exeV1,
		CandidateDirs:  []string{binDir, altDir},
		EnvPATH:        binDir + string(os.PathListSeparator) + altDir,
		HomeDir:        root,
	})
	if err != nil {
		t.Fatalf("Ensure v1 failed: %v", err)
	}
	wantLink := filepath.Join(binDir, CommandName)
	if !st.Installed || !st.Changed || !st.InPATH || st.LinkPath != wantLink {
		t.Fatalf("unexpected v1 status: %+v", st)
	}
	gotTarget, err := os.Readlink(wantLink)
	if err != nil || gotTarget != exeV1 {
		t.Fatalf("readlink = (%q, %v), want %q", gotTarget, err, exeV1)
	}

	// 2. 幂等校验：再次调用不重复修改
	st2, err := Ensure(Options{
		ExecutablePath: exeV1,
		CandidateDirs:  []string{binDir, altDir},
		EnvPATH:        binDir,
		HomeDir:        root,
	})
	if err != nil {
		t.Fatalf("Ensure idempotent failed: %v", err)
	}
	if st2.Changed || st2.LinkPath != wantLink {
		t.Fatalf("expected unchanged idempotent status, got %+v", st2)
	}

	// 3. 路径变更后自动原地更新已有软链
	st3, err := Ensure(Options{
		ExecutablePath: exeV2,
		CandidateDirs:  []string{altDir, binDir}, // 即便顺序调整，仍应原地更新已有己方软链
		EnvPATH:        binDir,
		HomeDir:        root,
	})
	if err != nil {
		t.Fatalf("Ensure v2 update failed: %v", err)
	}
	if !st3.Changed || st3.LinkPath != wantLink {
		t.Fatalf("expected existing symlink updated in-place, got %+v", st3)
	}
	gotTarget, err = os.Readlink(wantLink)
	if err != nil || gotTarget != exeV2 {
		t.Fatalf("updated readlink = (%q, %v), want %q", gotTarget, err, exeV2)
	}
}

func TestUnixSkipForeignConflictAndFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink conflict test runs on unix")
	}

	root := t.TempDir()
	conflictDir := filepath.Join(root, "conflict-bin")
	fallbackDir := filepath.Join(root, "fallback-bin")
	if err := os.MkdirAll(conflictDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// 在第一个目录放一个非 FlyShadow Bar 的同名普通文件，绝不可被覆盖
	foreignFile := filepath.Join(conflictDir, CommandName)
	if err := os.WriteFile(foreignFile, []byte("other-tool"), 0o755); err != nil {
		t.Fatal(err)
	}

	exe := filepath.Join(root, "FlyShadow Bar.app", "Contents", "MacOS", "FlyShadow Bar")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}

	st, err := Ensure(Options{
		ExecutablePath: exe,
		CandidateDirs:  []string{conflictDir, fallbackDir},
		EnvPATH:        fallbackDir,
		HomeDir:        root,
	})
	if err != nil {
		t.Fatalf("Ensure fallback failed: %v", err)
	}
	if st.LinkPath != filepath.Join(fallbackDir, CommandName) {
		t.Fatalf("expected fallback link in %s, got %+v", fallbackDir, st)
	}

	// 确认原冲突文件完好无损
	raw, err := os.ReadFile(foreignFile)
	if err != nil || string(raw) != "other-tool" {
		t.Fatalf("foreign file was modified: %q, err=%v", string(raw), err)
	}
}
