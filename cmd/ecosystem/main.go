package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"fileecosystem/internal/platform"
	"fileecosystem/internal/server"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(args []string) error {
	command := "serve"
	if len(args) > 0 {
		command = args[0]
	}
	dataDir, err := appDataDir()
	if err != nil {
		return err
	}
	baseURL := "http://127.0.0.1:8765"
	switch command {
	case "serve":
		srv, err := server.New(server.Options{DataDir: dataDir, Addr: "127.0.0.1:8765"})
		if err != nil {
			return err
		}
		defer srv.Close()
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		go func() {
			<-ctx.Done()
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdown)
		}()
		fmt.Println("栖境已启动：" + baseURL)
		err = srv.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case "open":
		return platform.OpenBrowser(baseURL)
	case "status":
		return printGET(baseURL + "/api/v1/status")
	case "privacy":
		return printGET(baseURL + "/api/v1/privacy")
	case "scan":
		return errors.New("请在本地界面中启动扫描；写请求需要当前页面的本地会话令牌")
	default:
		return fmt.Errorf("未知命令 %q；可用命令：serve、open、status、privacy", command)
	}
}

func appDataDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "FileEcosystem"), nil
}

func printGET(url string) error {
	client := http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(url)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("本地服务返回 %s", response.Status)
	}
	var value any
	if err := json.NewDecoder(response.Body).Decode(&value); err != nil {
		return err
	}
	output, _ := json.MarshalIndent(value, "", "  ")
	fmt.Println(string(output))
	return nil
}
