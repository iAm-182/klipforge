package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"klipforge/internal/deps"
	"klipforge/internal/events"
	"klipforge/internal/server"
	"klipforge/internal/store"
)

func main() {
	port := flag.Int("port", 8787, "HTTP dashboard port")
	host := flag.String("host", "127.0.0.1", "HTTP bind address")
	noOpen := flag.Bool("no-open", false, "Do not auto-open browser on startup")
	flag.Parse()

	baseDir := resolveBaseDir()
	st, err := store.NewStore(baseDir)
	if err != nil {
		log.Fatalf("Gagal inisialisasi storage: %v", err)
	}

	depMgr := deps.NewManager(baseDir)
	broker := events.NewBroker()
	srv := server.New(st, depMgr, broker)

	addr := fmt.Sprintf("%s:%d", *host, *port)
	url := fmt.Sprintf("http://%s", addr)

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
	}

	printBanner(url, baseDir, depMgr)

	if !*noOpen {
		go func() {
			time.Sleep(600 * time.Millisecond)
			openBrowser(url)
		}()
	}

	// Graceful shutdown on Ctrl+C
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		<-sigCh
		fmt.Println("\nMenutup KlipForge...")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(ctx)
	}()

	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Server error: %v", err)
	}
}

func resolveBaseDir() string {
	// Prefer current working directory if it contains go.mod or bin/
	if wd, err := os.Getwd(); err == nil {
		if _, err := os.Stat(filepath.Join(wd, "bin")); err == nil {
			return wd
		}
		if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
			return wd
		}
	}
	if exe, err := os.Executable(); err == nil {
		return filepath.Dir(exe)
	}
	return "."
}

func printBanner(url, baseDir string, depMgr *deps.Manager) {
	st := depMgr.GetStatus()
	fmt.Println("============================================================")
	fmt.Println("  KLIPFORGE — Ultra-Fast Go Auto-Clipper & MotionKlip Studio")
	fmt.Println("============================================================")
	fmt.Printf("  Dashboard : %s\n", url)
	fmt.Printf("  Workspace : %s\n", baseDir)
	fmt.Printf("  FFmpeg    : %s\n", statusStr(st.FFmpegPath))
	fmt.Printf("  yt-dlp    : %s\n", statusStr(st.YtDlpPath))
	fmt.Println("------------------------------------------------------------")
	fmt.Println("  Tekan Ctrl+C untuk berhenti.")
}

func statusStr(p string) string {
	if p == "" {
		return "TIDAK DITEMUKAN!"
	}
	return p
}

func openBrowser(url string) {
	switch runtime.GOOS {
	case "windows":
		_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		_ = exec.Command("open", url).Start()
	default:
		_ = exec.Command("xdg-open", url).Start()
	}
}
