//go:build windows

package secret

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

type dpapiStore struct {
	dir string
}

func New(namespace string) Store {
	base, err := os.UserConfigDir()
	if err != nil {
		return &dpapiStore{dir: ""}
	}
	if namespace == "" {
		namespace = "fileecosystem"
	}
	return &dpapiStore{dir: filepath.Join(base, namespace, "secrets")}
}

func (s *dpapiStore) Save(name string, value []byte) error {
	path, err := s.path(name)
	if err != nil {
		return err
	}
	protected, err := protect(value)
	if err != nil {
		return fmt.Errorf("protect secret: %w", err)
	}
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, protected, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (s *dpapiStore) Load(name string) ([]byte, error) {
	path, err := s.path(name)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return unprotect(data)
}

func (s *dpapiStore) Delete(name string) error {
	path, err := s.path(name)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *dpapiStore) path(name string) (string, error) {
	if s.dir == "" {
		return "", errors.New("user configuration directory unavailable")
	}
	if name == "" || strings.ContainsAny(name, `/\\`) || name == "." || name == ".." {
		return "", errors.New("invalid secret name")
	}
	sum := sha256.Sum256([]byte(name))
	return filepath.Join(s.dir, fmt.Sprintf("%x.dpapi", sum[:])), nil
}

func blob(data []byte) windows.DataBlob {
	if len(data) == 0 {
		return windows.DataBlob{}
	}
	return windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
}

func protect(data []byte) ([]byte, error) {
	in := blob(data)
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(uintptr(unsafe.Pointer(out.Data))))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}

func unprotect(data []byte) ([]byte, error) {
	in := blob(data)
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(uintptr(unsafe.Pointer(out.Data))))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}
