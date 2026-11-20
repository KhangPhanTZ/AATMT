// AATMT — AI Automation Task Management Tool.
//
// One self-contained binary: JSON-file storage, embedded web UI, and an agent
// that turns captured messages/screenshots + source code into a brief, a plan
// and executed changes, using whichever AI endpoints you configure.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/khangphantz/aatmt/internal/agent"
	"github.com/khangphantz/aatmt/internal/llm"
	"github.com/khangphantz/aatmt/internal/logbuf"
	"github.com/khangphantz/aatmt/internal/notify"
	"github.com/khangphantz/aatmt/internal/server"
	"github.com/khangphantz/aatmt/internal/store"
	"github.com/khangphantz/aatmt/internal/version"
	"github.com/khangphantz/aatmt/web"
)

func main() {
	addr := flag.String("addr", env("AATMT_ADDR", "127.0.0.1:8787"), "listen address")
	dataDir := flag.String("data", env("AATMT_DATA", defaultDataDir()), "data directory")
	token := flag.String("token", os.Getenv("AATMT_TOKEN"), "access token (required when not listening on loopback)")
	open := flag.Bool("open", false, "open the UI in the default browser")
	showVersion := flag.Bool("version", false, "print version")
	flag.Parse()
	if *showVersion {
		fmt.Println("aatmt", version.Version)
		return
	}

	host, _, err := net.SplitHostPort(*addr)
	if err != nil {
		log.Fatalf("bad -addr: %v", err)
	}
	ip := net.ParseIP(host)
	local := host == "localhost" || (ip != nil && ip.IsLoopback())
	if !local && *token == "" {
		log.Fatal("refusing to listen beyond loopback without -token / AATMT_TOKEN (the agent can read files and run commands)")
	}

	logs := logbuf.New(store.DefaultLimits().LogLines)
	log.SetOutput(io.MultiWriter(os.Stderr, logs))

	st, err := store.Open(*dataDir)
	if err != nil {
		log.Fatalf("open data dir: %v", err)
	}
	st.OnEvent = notify.Hook(st)
	pool := llm.NewPool()
	eng := agent.New(st, pool)
	srv := &server.Server{Store: st, Engine: eng, Pool: pool, Token: *token, UI: web.FS(), Local: local, Logs: logs, Version: version.Version}
	srv.Apply(st.Config().Settings) // limits, retry policy, model defaults, log size

	hs := &http.Server{Addr: *addr, Handler: srv.Handler(), ReadHeaderTimeout: readHeaderTimeout}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	url := "http://" + ln.Addr().String()
	notify.SetBaseURL(url)
	log.Printf("AATMT %s — %s (data: %s)", version.Version, url, *dataDir)
	if *open {
		go openBrowser(url)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		_ = hs.Shutdown(sctx)
		eng.MCP.Close() // stop stdio MCP server processes
	}()
	if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

// Server hardening values (not user limits): a slow client gets this long to
// send request headers, and shutdown waits this long for open requests.
const (
	readHeaderTimeout = 10 * time.Second
	shutdownGrace     = 5 * time.Second
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func defaultDataDir() string {
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "aatmt")
	}
	return "data"
}

// openBrowser runs after the listener is open, so the page loads immediately.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
