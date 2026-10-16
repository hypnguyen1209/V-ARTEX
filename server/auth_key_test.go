package server

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Matches b2903f0: old binaries ignore ARTEX_JWT_KEY_DIR and use the entire
// trimmed BaseDir/jwt.key, creating it when no usable file exists.
func oldBinaryJWTKey(t *testing.T, baseDir, dataDir string) []byte {
	t.Helper()
	path := filepath.Join(baseDir, jwtKeyFilename)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if data, err := os.ReadFile(filepath.Join(dataDir, jwtKeyFilename)); err == nil {
			if err := os.WriteFile(path, data, 0o600); err == nil {
				_ = os.Remove(filepath.Join(dataDir, jwtKeyFilename))
			}
		}
	}
	if data, err := os.ReadFile(path); err == nil && len(bytes.TrimSpace(data)) >= 32 {
		return bytes.TrimSpace(data)
	}
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	key := make([]byte, 32)
	for i := range key {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		if err != nil {
			t.Fatal(err)
		}
		key[i] = chars[n.Int64()]
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		t.Fatal(err)
	}
	return key
}

func TestLoadOrCreateJWTKeyRetiresExecutableKeyAcrossRollbacks(t *testing.T) {
	keyDir, dataDir, baseDir := t.TempDir(), t.TempDir(), t.TempDir()
	var oldTokens []string
	var hardenedKey []byte
	for cycle := 0; cycle < 3; cycle++ {
		oldKey := oldBinaryJWTKey(t, baseDir, dataDir)
		for _, token := range oldTokens {
			if verifyJWT(token, oldKey) {
				t.Fatal("rollback revived a previous executable-directory token")
			}
		}
		token, err := signJWT(oldKey)
		if err != nil {
			t.Fatal(err)
		}
		if !verifyJWT(token, oldKey) {
			t.Fatal("old binary token must verify before upgrade")
		}
		oldTokens = append(oldTokens, token)
		key, err := loadOrCreateJWTKey(keyDir, dataDir, baseDir)
		if err != nil {
			t.Fatal(err)
		}
		if hardenedKey != nil && !bytes.Equal(key, hardenedKey) {
			t.Fatal("rollback cleanup must preserve the configured root key")
		}
		hardenedKey = key
		assertVersionedJWTKey(t, keyDir, key)
		for _, token := range oldTokens {
			if verifyJWT(token, key) {
				t.Fatal("old binary token must not verify after upgrade")
			}
		}
		if _, err := os.Lstat(filepath.Join(baseDir, jwtKeyFilename)); !os.IsNotExist(err) {
			t.Fatalf("executable-directory key must be retired on every startup, got %v", err)
		}
	}
}

func TestLoadOrCreateJWTKeyRetiredDirectoryAliases(t *testing.T) {
	for _, kind := range []string{"same", "relative", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			keyDir, dataDir := t.TempDir(), t.TempDir()
			key, err := loadOrCreateJWTKey(keyDir, dataDir)
			if err != nil {
				t.Fatal(err)
			}
			baseDir := keyDir
			if kind == "relative" {
				cwd, err := os.Getwd()
				if err != nil {
					t.Fatal(err)
				}
				baseDir, err = filepath.Rel(cwd, keyDir)
				if err != nil {
					t.Fatal(err)
				}
			} else if kind == "symlink" {
				baseDir = filepath.Join(t.TempDir(), "executable")
				if err := os.Symlink(keyDir, baseDir); err != nil {
					if runtime.GOOS == "windows" {
						t.Skipf("symlinks unavailable: %v", err)
					}
					t.Fatal(err)
				}
			}
			got, err := loadOrCreateJWTKey(keyDir, dataDir, baseDir)
			if err != nil || !bytes.Equal(got, key) {
				t.Fatalf("physical directory alias must preserve canonical key: %v", err)
			}
			assertVersionedJWTKey(t, keyDir, key)
		})
	}
}

func TestLoadOrCreateJWTKeyRetiredFileAliases(t *testing.T) {
	for _, kind := range []string{"hardlink", "legacy symlink", "canonical symlink"} {
		t.Run(kind, func(t *testing.T) {
			keyDir, dataDir, baseDir := t.TempDir(), t.TempDir(), t.TempDir()
			key, err := loadOrCreateJWTKey(keyDir, dataDir)
			if err != nil {
				t.Fatal(err)
			}
			canonical := filepath.Join(keyDir, jwtKeyFilename)
			retired := filepath.Join(baseDir, jwtKeyFilename)
			if kind == "canonical symlink" {
				if err := os.Rename(canonical, retired); err != nil {
					t.Fatal(err)
				}
				canonical, retired = retired, canonical
			}
			link := os.Symlink
			if kind == "hardlink" {
				link = os.Link
			}
			if err := link(canonical, retired); err != nil {
				if runtime.GOOS == "windows" {
					t.Skipf("links unavailable: %v", err)
				}
				t.Fatal(err)
			}
			got, err := loadOrCreateJWTKey(keyDir, dataDir, baseDir)
			if kind == "canonical symlink" {
				if err == nil {
					t.Fatal("canonical symlink into retired entry must fail closed")
				}
			} else {
				if err != nil || !bytes.Equal(got, key) {
					t.Fatalf("unlinking retired alias must preserve canonical key: %v", err)
				}
				if _, err := os.Lstat(retired); !os.IsNotExist(err) {
					t.Fatalf("retired file alias must be removed: %v", err)
				}
			}
			assertVersionedJWTKey(t, keyDir, key)
		})
	}
}

func TestLoadOrCreateJWTKeyRetiredMissing(t *testing.T) {
	for _, missingDir := range []bool{false, true} {
		keyDir, dataDir, baseDir := t.TempDir(), t.TempDir(), t.TempDir()
		if missingDir {
			baseDir = filepath.Join(baseDir, "missing")
		}
		key, err := loadOrCreateJWTKey(keyDir, dataDir, baseDir)
		if err != nil {
			t.Fatal(err)
		}
		assertVersionedJWTKey(t, keyDir, key)
	}
}

func TestLoadOrCreateJWTKeyRetiredWorkspaceOverlap(t *testing.T) {
	for _, canonicalSymlink := range []bool{false, true} {
		t.Run(fmt.Sprint("canonical symlink=", canonicalSymlink), func(t *testing.T) {
			keyDir, baseDir := t.TempDir(), t.TempDir()
			key, err := loadOrCreateJWTKey(keyDir, baseDir)
			if err != nil {
				t.Fatal(err)
			}
			canonical := filepath.Join(keyDir, jwtKeyFilename)
			retired := filepath.Join(baseDir, jwtKeyFilename)
			if canonicalSymlink {
				if err := os.Rename(canonical, retired); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(retired, canonical); err != nil {
					if runtime.GOOS == "windows" {
						t.Skipf("symlinks unavailable: %v", err)
					}
					t.Fatal(err)
				}
			} else {
				oldBinaryJWTKey(t, baseDir, t.TempDir())
			}
			removeCalls := 0
			ops := jwtKeyFileOps{rename: os.Rename, remove: func(path string) error {
				if path == retired {
					removeCalls++
				}
				return os.Remove(path)
			}}
			got, err := loadOrCreateJWTKeyWithOps(keyDir, baseDir, ops, baseDir)
			if canonicalSymlink {
				if err == nil || removeCalls != 0 {
					t.Fatalf("unsafe overlap must fail before removing canonical target: err=%v, removals=%d", err, removeCalls)
				}
			} else if err != nil || !bytes.Equal(got, key) || removeCalls != 1 {
				t.Fatalf("workspace/retired overlap must be removed once: err=%v, removals=%d", err, removeCalls)
			}
			assertVersionedJWTKey(t, keyDir, key)
		})
	}
}

func TestLoadOrCreateJWTKeyRetirementFailureRetry(t *testing.T) {
	keyDir, dataDir, baseDir := t.TempDir(), t.TempDir(), t.TempDir()
	legacy := oldBinaryJWTKey(t, baseDir, dataDir)
	retired := filepath.Join(baseDir, jwtKeyFilename)
	removeErr := errors.New("executable key removal failed")
	ops := jwtKeyFileOps{rename: os.Rename, remove: func(path string) error {
		if path == retired {
			return removeErr
		}
		return os.Remove(path)
	}}
	if _, err := loadOrCreateJWTKeyWithOps(keyDir, dataDir, ops, baseDir); !errors.Is(err, removeErr) {
		t.Fatalf("retirement failure must fail startup, got %v", err)
	}
	disk, err := os.ReadFile(filepath.Join(keyDir, jwtKeyFilename))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(retired); err != nil || !bytes.Equal(got, legacy) {
		t.Fatalf("failed removal must preserve executable key: %v", err)
	}
	key, err := loadOrCreateJWTKey(keyDir, dataDir, baseDir)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(assertVersionedJWTKey(t, keyDir, key), disk) {
		t.Fatal("retirement retry must preserve canonical key")
	}
	if _, err := os.Lstat(retired); !os.IsNotExist(err) {
		t.Fatalf("retry must retire executable key: %v", err)
	}
}

func TestLoadOrCreateJWTKeyRetiredInspectionFailure(t *testing.T) {
	for _, fileLoop := range []bool{false, true} {
		keyDir, dataDir := t.TempDir(), t.TempDir()
		key, err := loadOrCreateJWTKey(keyDir, dataDir)
		if err != nil {
			t.Fatal(err)
		}
		workspace := filepath.Join(dataDir, jwtKeyFilename)
		legacy := []byte(strings.Repeat("w", 32))
		if err := os.WriteFile(workspace, legacy, 0o600); err != nil {
			t.Fatal(err)
		}
		baseDir := filepath.Join(t.TempDir(), "loop")
		if fileLoop {
			if err := os.Mkdir(baseDir, 0o700); err != nil {
				t.Fatal(err)
			}
			// ENOTDIR while inspecting the legacy path must fail closed.
			baseDir = filepath.Join(baseDir, "not-directory")
			if err := os.WriteFile(baseDir, []byte("file"), 0o600); err != nil {
				t.Fatal(err)
			}
		} else if err := os.Symlink(baseDir, baseDir); err != nil {
			if runtime.GOOS == "windows" {
				t.Skipf("symlinks unavailable: %v", err)
			}
			t.Fatal(err)
		}
		if _, err := loadOrCreateJWTKey(keyDir, dataDir, baseDir); err == nil {
			t.Fatal("retired directory inspection failure must fail startup")
		}
		assertVersionedJWTKey(t, keyDir, key)
		if got, err := os.ReadFile(workspace); err != nil || !bytes.Equal(got, legacy) {
			t.Fatalf("inspection failure must precede workspace cleanup: %v", err)
		}
	}
}

func TestLoadOrCreateJWTKeyFailurePreservesRetiredKey(t *testing.T) {
	for _, failure := range []string{"malformed canonical", "missing canonical directory", "install failure"} {
		t.Run(failure, func(t *testing.T) {
			keyDir, dataDir, baseDir := t.TempDir(), t.TempDir(), t.TempDir()
			legacy := oldBinaryJWTKey(t, baseDir, dataDir)
			ops := jwtKeyFileOps{rename: os.Rename, remove: os.Remove}
			if failure == "malformed canonical" {
				if err := os.WriteFile(filepath.Join(keyDir, jwtKeyFilename), []byte("invalid"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if failure == "missing canonical directory" {
				keyDir = filepath.Join(keyDir, "missing")
			} else {
				ops.rename = func(string, string) error { return errors.New("install failed") }
			}
			if _, err := loadOrCreateJWTKeyWithOps(keyDir, dataDir, ops, baseDir); err == nil {
				t.Fatal("canonical load/create must fail")
			}
			if got, err := os.ReadFile(filepath.Join(baseDir, jwtKeyFilename)); err != nil || !bytes.Equal(got, legacy) {
				t.Fatalf("canonical failure must preserve recoverable executable key: %v", err)
			}
		})
	}
}

func assertVersionedJWTKey(t *testing.T, keyDir string, key []byte) []byte {
	t.Helper()
	if len(key) != 32 {
		t.Fatalf("key length = %d, want 32", len(key))
	}
	path := filepath.Join(keyDir, "jwt.key")
	disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(disk, []byte("v2:")) || !bytes.HasSuffix(disk, []byte("\n")) {
		t.Fatalf("key file must use v2 format and end with a newline")
	}
	payload, err := base64.RawURLEncoding.DecodeString(string(disk[3 : len(disk)-1]))
	if err != nil {
		t.Fatalf("decode key payload: %v", err)
	}
	if !bytes.Equal(payload, key) {
		t.Fatal("persisted payload differs from returned key")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("key permissions = %o, want 600", info.Mode().Perm())
		}
	}
	return disk
}

func TestLoadOrCreateJWTKeyNew(t *testing.T) {
	keyDir, dataDir := t.TempDir(), t.TempDir()
	key, err := loadOrCreateJWTKey(keyDir, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	disk := assertVersionedJWTKey(t, keyDir, key)
	again, err := loadOrCreateJWTKey(keyDir, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(key, again) || !bytes.Equal(disk, assertVersionedJWTKey(t, keyDir, again)) {
		t.Fatal("second load must reuse the persisted key")
	}
}

func TestLoadOrCreateJWTKeyRotatesCurrentLegacyOnce(t *testing.T) {
	keyDir, dataDir := t.TempDir(), t.TempDir()
	legacy := []byte(strings.Repeat("a", 32))
	path := filepath.Join(keyDir, "jwt.key")
	if err := os.WriteFile(path, legacy, 0o644); err != nil {
		t.Fatal(err)
	}
	legacyToken, err := signJWT(legacy)
	if err != nil {
		t.Fatal(err)
	}
	key, err := loadOrCreateJWTKey(keyDir, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(key, legacy) {
		t.Fatal("legacy key must be replaced with a new root key")
	}
	disk := assertVersionedJWTKey(t, keyDir, key)
	if verifyJWT(legacyToken, key) {
		t.Fatal("legacy token must be invalid after rotation")
	}
	// The old binary trims the complete file and uses it as an opaque key.
	oldBinaryKey := bytes.TrimSpace(disk)
	if verifyJWT(legacyToken, oldBinaryKey) {
		t.Fatal("rollback must not restore legacy sessions")
	}
	token, err := signJWT(key)
	if err != nil {
		t.Fatal(err)
	}
	if !verifyJWT(token, key) {
		t.Fatal("hardened token must verify with the decoded payload")
	}
	if verifyJWT(token, oldBinaryKey) {
		t.Fatal("hardened token must not verify with the old binary's opaque key")
	}
	again, err := loadOrCreateJWTKey(keyDir, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(key, again) || !bytes.Equal(disk, assertVersionedJWTKey(t, keyDir, again)) {
		t.Fatal("versioned key must not rotate on subsequent loads")
	}
	if _, err := os.Stat(path + ".legacy"); !os.IsNotExist(err) {
		t.Fatalf("rotation backup must be removed, got %v", err)
	}
}

func TestLoadOrCreateJWTKeyRotatesWorkspaceLegacy(t *testing.T) {
	keyDir, dataDir := t.TempDir(), t.TempDir()
	legacy := []byte(strings.Repeat("b", 32))
	workspacePath := filepath.Join(dataDir, "jwt.key")
	if err := os.WriteFile(workspacePath, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := loadOrCreateJWTKey(keyDir, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	assertVersionedJWTKey(t, keyDir, key)
	if bytes.Equal(key, legacy) {
		t.Fatal("workspace key must be replaced with a new root key")
	}
	if _, err := os.Stat(workspacePath); !os.IsNotExist(err) {
		t.Fatalf("workspace key must be removed, got %v", err)
	}
}

func TestLoadOrCreateJWTKeyRecoversInterruptedRotation(t *testing.T) {
	keyDir := t.TempDir()
	legacy := []byte(strings.Repeat("c", 32))
	backup := filepath.Join(keyDir, "jwt.key.legacy")
	if err := os.WriteFile(backup, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := loadOrCreateJWTKey(keyDir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	assertVersionedJWTKey(t, keyDir, key)
	if bytes.Equal(key, legacy) {
		t.Fatal("interrupted rotation must generate a new key")
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatalf("recovered backup must be removed, got %v", err)
	}
}

func TestLoadOrCreateJWTKeyRemovesStaleBackup(t *testing.T) {
	keyDir, dataDir := t.TempDir(), t.TempDir()
	key := bytes.Repeat([]byte{42}, 32)
	disk := []byte("v2:" + base64.RawURLEncoding.EncodeToString(key) + "\n")
	if err := os.WriteFile(filepath.Join(keyDir, "jwt.key"), disk, 0o600); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(keyDir, "jwt.key.legacy")
	if err := os.WriteFile(backup, []byte(strings.Repeat("d", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadOrCreateJWTKey(keyDir, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, key) {
		t.Fatal("completed rotation must reuse the versioned key")
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatalf("stale backup must be removed, got %v", err)
	}
}

func TestLoadOrCreateJWTKeyRemovesStaleWorkspaceKey(t *testing.T) {
	keyDir, dataDir := t.TempDir(), t.TempDir()
	key := bytes.Repeat([]byte{42}, 32)
	disk := []byte("v2:" + base64.RawURLEncoding.EncodeToString(key) + "\n")
	if err := os.WriteFile(filepath.Join(keyDir, "jwt.key"), disk, 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(dataDir, "jwt.key")
	if err := os.WriteFile(workspace, []byte(strings.Repeat("e", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadOrCreateJWTKey(keyDir, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, key) || !bytes.Equal(assertVersionedJWTKey(t, keyDir, got), disk) {
		t.Fatal("workspace cleanup must reuse the existing versioned key")
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("stale workspace key must be removed, got %v", err)
	}
}

func TestLoadOrCreateJWTKeyFailsWhenStaleWorkspaceCleanupFails(t *testing.T) {
	keyDir, dataDir := t.TempDir(), t.TempDir()
	key := bytes.Repeat([]byte{42}, 32)
	if err := os.WriteFile(filepath.Join(keyDir, "jwt.key"), []byte("v2:"+base64.RawURLEncoding.EncodeToString(key)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(dataDir, "jwt.key")
	legacy := strings.Repeat("f", 32)
	if err := os.WriteFile(workspace, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	removeErr := errors.New("workspace removal failed")
	ops := jwtKeyFileOps{rename: os.Rename, remove: func(path string) error {
		if path == workspace {
			return removeErr
		}
		return os.Remove(path)
	}}
	if _, err := loadOrCreateJWTKeyWithOps(keyDir, dataDir, ops); !errors.Is(err, removeErr) {
		t.Fatalf("workspace cleanup failure must fail startup, got %v", err)
	}
	assertVersionedJWTKey(t, keyDir, key)
	if got, err := os.ReadFile(workspace); err != nil || string(got) != legacy {
		t.Fatalf("workspace key must remain after removal failure: %v", err)
	}
	got, err := loadOrCreateJWTKey(keyDir, dataDir)
	if err != nil || !bytes.Equal(got, key) {
		t.Fatalf("cleanup retry must preserve the current key: %v", err)
	}
	if _, err := os.Lstat(workspace); !os.IsNotExist(err) {
		t.Fatalf("cleanup retry must remove workspace key, got %v", err)
	}
}

func TestLoadOrCreateJWTKeyRestoresLegacyAfterInstallFailure(t *testing.T) {
	for _, failRestore := range []bool{false, true} {
		name := "restore succeeds"
		if failRestore {
			name = "restore fails"
		}
		t.Run(name, func(t *testing.T) {
			keyDir, dataDir := t.TempDir(), t.TempDir()
			path := filepath.Join(keyDir, "jwt.key")
			backup := path + ".legacy"
			legacy := strings.Repeat("a", 32)
			if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
				t.Fatal(err)
			}
			installErr := errors.New("install rename failed")
			restoreErr := errors.New("restore rename failed")
			ops := jwtKeyFileOps{remove: os.Remove, rename: func(from, to string) error {
				if to == path && from != backup {
					return installErr
				}
				if failRestore && from == backup && to == path {
					return restoreErr
				}
				return os.Rename(from, to)
			}}
			_, err := loadOrCreateJWTKeyWithOps(keyDir, dataDir, ops)
			if !errors.Is(err, installErr) || errors.Is(err, restoreErr) != failRestore {
				t.Fatalf("expected install failure and restore failure=%v, got %v", failRestore, err)
			}
			present, absent := path, backup
			if failRestore {
				present, absent = backup, path
			}
			if got, err := os.ReadFile(present); err != nil || string(got) != legacy {
				t.Fatalf("original legacy key must survive at %s: %v", present, err)
			}
			if _, err := os.Lstat(absent); !os.IsNotExist(err) {
				t.Fatalf("unexpected artifact %s after failed install: %v", absent, err)
			}
			entries, err := os.ReadDir(keyDir)
			if err != nil || len(entries) != 1 {
				t.Fatalf("failed install must clean up its temp file: entries=%d, error=%v", len(entries), err)
			}
			key, err := loadOrCreateJWTKey(keyDir, dataDir)
			if err != nil {
				t.Fatal(err)
			}
			assertVersionedJWTKey(t, keyDir, key)
			if bytes.Equal(key, []byte(legacy)) {
				t.Fatal("retry must rotate the surviving legacy key")
			}
			if _, err := os.Lstat(backup); !os.IsNotExist(err) {
				t.Fatalf("successful retry must remove backup, got %v", err)
			}
		})
	}
}

func TestLoadOrCreateJWTKeyRetriesBackupRemoval(t *testing.T) {
	keyDir, dataDir := t.TempDir(), t.TempDir()
	path := filepath.Join(keyDir, "jwt.key")
	backup := path + ".legacy"
	legacy := strings.Repeat("a", 32)
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	removeErr := errors.New("backup removal failed")
	ops := jwtKeyFileOps{rename: os.Rename, remove: func(name string) error {
		if name == backup {
			return removeErr
		}
		return os.Remove(name)
	}}
	if _, err := loadOrCreateJWTKeyWithOps(keyDir, dataDir, ops); !errors.Is(err, removeErr) {
		t.Fatalf("backup removal failure must fail startup, got %v", err)
	}
	disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	key, err := decodeVersionedJWTKey(disk)
	if err != nil || key == nil {
		t.Fatalf("complete versioned key must survive backup removal failure: %v", err)
	}
	assertVersionedJWTKey(t, keyDir, key)
	if got, err := os.ReadFile(backup); err != nil || string(got) != legacy {
		t.Fatalf("legacy backup must survive removal failure: %v", err)
	}
	got, err := loadOrCreateJWTKey(keyDir, dataDir)
	if err != nil || !bytes.Equal(got, key) || !bytes.Equal(assertVersionedJWTKey(t, keyDir, got), disk) {
		t.Fatalf("backup cleanup retry must reuse the current key: %v", err)
	}
	if _, err := os.Lstat(backup); !os.IsNotExist(err) {
		t.Fatalf("cleanup retry must remove backup, got %v", err)
	}
}

func TestLoadOrCreateJWTKeyKeepsSharedKeyPath(t *testing.T) {
	keyDir := t.TempDir()
	key, err := loadOrCreateJWTKey(keyDir, keyDir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := loadOrCreateJWTKey(keyDir, keyDir)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, key) {
		t.Fatal("shared key and workspace path must reuse the key")
	}
	assertVersionedJWTKey(t, keyDir, got)
}

func TestLoadOrCreateJWTKeyKeepsAliasedKeyPath(t *testing.T) {
	keyDir := t.TempDir()
	key, err := loadOrCreateJWTKey(keyDir, keyDir)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "workspace")
	if err := os.Symlink(keyDir, alias); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("directory symlinks unavailable: %v", err)
		}
		t.Fatal(err)
	}
	got, err := loadOrCreateJWTKey(keyDir, alias)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, key) {
		t.Fatal("aliased key and workspace path must reuse the key")
	}
	assertVersionedJWTKey(t, keyDir, got)
}

func TestLoadOrCreateJWTKeyRemovesWorkspaceFileAliases(t *testing.T) {
	versioned := "v2:" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{42}, 32)) + "\n"
	for _, tc := range []struct {
		name, current string
		link          func(string, string) error
		symlink       bool
	}{
		{"versioned hardlink", versioned, os.Link, false},
		{"versioned symlink", versioned, os.Symlink, true},
		{"legacy hardlink", strings.Repeat("a", 32), os.Link, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			keyDir, dataDir := t.TempDir(), t.TempDir()
			path := filepath.Join(keyDir, "jwt.key")
			workspace := filepath.Join(dataDir, "jwt.key")
			if err := os.WriteFile(path, []byte(tc.current), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := tc.link(path, workspace); err != nil {
				if tc.symlink && runtime.GOOS == "windows" {
					t.Skipf("file symlinks unavailable: %v", err)
				}
				t.Fatal(err)
			}
			key, err := loadOrCreateJWTKey(keyDir, dataDir)
			if err != nil {
				t.Fatal(err)
			}
			disk := assertVersionedJWTKey(t, keyDir, key)
			if tc.current == versioned && string(disk) != versioned {
				t.Fatal("file alias cleanup must preserve the versioned root key")
			}
			if bytes.Equal(key, []byte(tc.current)) {
				t.Fatal("legacy root key must rotate before alias cleanup")
			}
			if _, err := os.Lstat(workspace); !os.IsNotExist(err) {
				t.Fatalf("workspace file alias must be removed, got %v", err)
			}
		})
	}
}

func TestLoadOrCreateJWTKeyRejectsDanglingWorkspaceSymlink(t *testing.T) {
	keyDir, dataDir := t.TempDir(), t.TempDir()
	path := filepath.Join(keyDir, "jwt.key")
	workspace := filepath.Join(dataDir, "jwt.key")
	if err := os.Symlink(path, workspace); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("file symlinks unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if _, err := loadOrCreateJWTKey(keyDir, dataDir); err == nil {
		t.Fatal("dangling workspace symlink must fail closed before destination creation")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("destination key must remain absent, got %v", err)
	}
	target, err := os.Readlink(workspace)
	if err != nil || target != path {
		t.Fatalf("dangling workspace symlink must remain untouched, got target %q, error %v", target, err)
	}
}

func TestLoadOrCreateJWTKeyValidatesWorkspaceBeforeMutation(t *testing.T) {
	versioned := "v2:" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{42}, 32)) + "\n"
	for _, source := range []struct {
		name, current, backup string
	}{
		{"current versioned", versioned, ""},
		{"current versioned with backup", versioned, strings.Repeat("a", 32)},
		{"current legacy", strings.Repeat("a", 32), ""},
		{"backup legacy", "", strings.Repeat("a", 32)},
	} {
		for _, workspace := range []struct {
			name, data string
			valid      bool
		}{
			{"valid legacy", strings.Repeat("b", 32), true},
			{"empty", "", false},
			{"short legacy", "too-short", false},
			{"malformed versioned", "v2:" + strings.Repeat("!", 43) + "\n", false},
		} {
			t.Run(source.name+"/"+workspace.name, func(t *testing.T) {
				keyDir, dataDir := t.TempDir(), t.TempDir()
				path := filepath.Join(keyDir, "jwt.key")
				backup := path + ".legacy"
				workspacePath := filepath.Join(dataDir, "jwt.key")
				original := map[string]string{workspacePath: workspace.data}
				if source.current != "" {
					original[path] = source.current
				}
				if source.backup != "" {
					original[backup] = source.backup
				}
				for name, data := range original {
					if err := os.WriteFile(name, []byte(data), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				key, err := loadOrCreateJWTKey(keyDir, dataDir)
				if !workspace.valid {
					if err == nil {
						t.Fatal("malformed workspace key must fail before changing any key artifact")
					}
					for _, name := range []string{path, backup, workspacePath} {
						got, err := os.ReadFile(name)
						want, existed := original[name]
						if existed && (err != nil || string(got) != want) {
							t.Fatalf("key artifact %s changed after validation failure: %v", name, err)
						}
						if !existed && !os.IsNotExist(err) {
							t.Fatalf("unexpected key artifact %s after validation failure: %v", name, err)
						}
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				disk := assertVersionedJWTKey(t, keyDir, key)
				if strings.HasPrefix(source.current, "v2:") && string(disk) != source.current {
					t.Fatal("workspace cleanup must not change an existing versioned root key")
				}
				if bytes.Equal(key, []byte(source.current)) || bytes.Equal(key, []byte(source.backup)) || bytes.Equal(key, []byte(workspace.data)) {
					t.Fatal("legacy root and workspace keys must not be reused")
				}
				for _, name := range []string{backup, workspacePath} {
					if _, err := os.Stat(name); !os.IsNotExist(err) {
						t.Fatalf("stale key artifact %s must be removed: %v", name, err)
					}
				}
			})
		}
	}
}

func TestLoadOrCreateJWTKeyRejectsMalformedKeys(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	for _, location := range []string{"current", "backup", "workspace"} {
		for _, tc := range []struct {
			name string
			data string
		}{
			{"empty", ""},
			{"short legacy", "too-short"},
			{"invalid base64", "v2:" + strings.Repeat("!", 43) + "\n"},
			{"short payload", "v2:" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 31)) + "\n"},
			{"long payload", "v2:" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 33)) + "\n"},
			{"padded payload", "v2:" + base64.URLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)) + "\n"},
			{"missing newline", "v2:" + payload},
			{"leading whitespace", " v2:" + payload + "\n"},
			{"trailing whitespace", "v2:" + payload + " \n"},
			{"extra trailing newline", "v2:" + payload + "\n\n"},
			{"CRLF", "v2:" + payload + "\r\n"},
			{"embedded newline", "v2:" + payload[:10] + "\n" + payload[10:] + "\n"},
		} {
			t.Run(location+"/"+tc.name, func(t *testing.T) {
				keyDir, dataDir := t.TempDir(), t.TempDir()
				path := filepath.Join(keyDir, "jwt.key")
				if location == "backup" {
					path += ".legacy"
				} else if location == "workspace" {
					path = filepath.Join(dataDir, "jwt.key")
				}
				if err := os.WriteFile(path, []byte(tc.data), 0o600); err != nil {
					t.Fatal(err)
				}
				if _, err := loadOrCreateJWTKey(keyDir, dataDir); err == nil {
					t.Fatal("malformed existing key must fail closed")
				}
				got, err := os.ReadFile(path)
				if err != nil || string(got) != tc.data {
					t.Fatalf("malformed key must remain untouched: read error = %v", err)
				}
			})
		}
	}
}
