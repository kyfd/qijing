// Package ipcpipe provides the local, permission-restricted transport used
// between the desktop main process and the scanner subprocess.
//
// Windows 使用 Named Pipe，其 DACL 通过 SDDL 只授予当前用户；其他平台仅
// 为开发编译提供 Unix domain socket 回退。两端的随机名称由 Broker 生成，
// 不写文件系统（除回退路径外）。
package ipcpipe

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
)

// Conn is the minimal stream both protocol peers need.
type Conn interface {
	io.ReadWriteCloser
}

// Listener accepts exactly one connection. The scanner process serves a
// single broker per pipe, so multi-accept semantics are deliberately absent.
type Listener interface {
	Accept() (Conn, error)
	Close() error
	// Name is the address the client dials.
	Name() string
}

// RandomName returns a fresh, unguessable pipe address.
func RandomName() (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate pipe name: %w", err)
	}
	return pipeName("qijing-" + hex.EncodeToString(raw[:])), nil
}
