//go:build windows

// qijing-scanner 是能力刻意最小的独立扫描进程。
//
// 它唯一的文件系统能力是只读扫描；唯一的对外通道是主进程预创建、DACL
// 限定当前用户的 Named Pipe。它不 import 数据库、Agent、回收站或设置
// 代码（守护测试钉住），也不监听任何 TCP 端口。
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/kyfd/qijing/internal/ipcpipe"
	"github.com/kyfd/qijing/internal/scannerproc"
	"github.com/kyfd/qijing/internal/scanproto"
)

func main() {
	pipe := flag.String("pipe", "", "named pipe served for the desktop main process")
	protocol := flag.Int("protocol", 0, "protocol version the main process speaks")
	flag.Parse()
	if *pipe == "" {
		fmt.Fprintln(os.Stderr, "usage: qijing-scanner --pipe <name> --protocol <version>")
		os.Exit(2)
	}
	if *protocol != scanproto.Version {
		fmt.Fprintf(os.Stderr, "protocol mismatch: main process speaks v%d, scanner speaks v%d\n", *protocol, scanproto.Version)
		os.Exit(3)
	}
	if err := run(*pipe); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(pipe string) error {
	listener, err := ipcpipe.ListenOn(pipe)
	if err != nil {
		return err
	}
	defer listener.Close()
	conn, err := listener.Accept()
	if err != nil {
		return err
	}
	// One connection, one scan. When it ends the process ends, so a scanner
	// can never outlive the broker conversation that authorized it.
	return scannerproc.Serve(conn, scannerproc.ScannerEngine{}, version, scannerproc.DefaultHeartbeatEvery)
}

// version is the scanner build identity reported during the handshake.
var version = "dev"
