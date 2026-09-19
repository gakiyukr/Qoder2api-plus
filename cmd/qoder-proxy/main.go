package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/J-York/QoderProxy/internal/config"
	"github.com/J-York/QoderProxy/internal/credential"
	"github.com/J-York/QoderProxy/internal/pool"
	"github.com/J-York/QoderProxy/internal/protocol"
	"github.com/J-York/QoderProxy/internal/qoder"
	"github.com/J-York/QoderProxy/internal/server"
)

// 版本與提交雜湊由 CI 透過 -ldflags "-X main.version=... -X main.commit=..."
// 注入；本機未注入時退回 dev，不影響邏輯。
var (
	version = "dev"
	commit  = "none"
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.LUTC)
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(os.Args[2:])
	case "login":
		err = loginDevice(os.Args[2:])
	case "import-pat":
		err = importPAT(os.Args[2:])
	case "models":
		err = models(os.Args[2:])
	case "doctor":
		err = doctor(os.Args[2:])
	case "version", "--version":
		fmt.Printf("qoder-proxy %s (commit %s, %s %s/%s)\n", version, commit,
			runtime.Version(), runtime.GOOS, runtime.GOARCH)
		return
	case "help", "--help", "-h":
		usage()
		return
	default:
		usage()
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		log.Printf("error: %s", qoder.SanitizeSnippet([]byte(err.Error()), 512))
		os.Exit(1)
	}
}
func usage() {
	fmt.Fprintln(os.Stderr, "qoder-proxy <serve|login|import-pat|models|doctor|version> [options]")
}

func load(args []string, name string) (config.Config, *flag.FlagSet, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	path := fs.String("config", "config.json", "JSON configuration path")
	if err := fs.Parse(args); err != nil {
		return config.Config{}, fs, err
	}
	cfg, err := config.Load(*path)
	return cfg, fs, err
}

func components(cfg config.Config) (*credential.Store, *qoder.AuthClient, *pool.Pool, *qoder.Catalog, qoder.ChatTransport, qoder.ChatTransport, error) {
	httpClient, err := qoder.NewHTTPClient(cfg)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, err
	}
	store := credential.NewStore(cfg.CredentialsFile)
	accounts, err := store.Load()
	if err != nil {
		return nil, nil, nil, nil, nil, nil, err
	}
	auth := qoder.NewAuthClient(httpClient)
	p := pool.New(accounts, store, auth)
	catalog := qoder.NewCatalog(httpClient, cfg.CatalogTTL.Duration)
	signer := qoder.NewCosySigner()
	return store, auth, p, catalog, &qoder.BearerTransport{HTTP: httpClient}, &qoder.CosyTransport{HTTP: httpClient, Signer: signer}, nil
}

func serve(args []string) error {
	cfg, _, err := load(args, "serve")
	if err != nil {
		return err
	}
	if err := cfg.ValidateServe(); err != nil {
		return err
	}
	_, auth, p, catalog, bearer, cosy, err := components(cfg)
	if err != nil {
		return err
	}
	s := server.New(cfg, p, catalog, bearer, cosy, auth, os.Getenv("QODER_PROXY_API_KEY"), log.Default())
	primeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	s.Prime(primeCtx)
	cancel()
	httpServer := &http.Server{Addr: cfg.Listen, Handler: s, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 0, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	log.Printf("qoder-proxy listening on %s accounts=%d transport=%s", cfg.Listen, p.Count(), cfg.Transport)
	err = httpServer.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func importPAT(args []string) error {
	fs := flag.NewFlagSet("import-pat", flag.ContinueOnError)
	configPath := fs.String("config", "config.json", "JSON configuration path")
	regionName := fs.String("region", "global", "global or cn")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	region := protocol.Region(*regionName)
	if _, ok := protocol.ForRegion(region); !ok {
		return errors.New("region must be global or cn")
	}
	pat := os.Getenv("QODER_PAT")
	if region == protocol.CN && pat == "" {
		pat = os.Getenv("QODERCN_PAT")
	}
	if pat == "" {
		pat, err = readSecret("Qoder PAT: ")
		if err != nil {
			return err
		}
	}
	if strings.TrimSpace(pat) == "" {
		return errors.New("empty PAT")
	}
	store, auth, _, _, _, _, err := components(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	account, err := auth.ExchangePAT(ctx, region, strings.TrimSpace(pat), "")
	pat = ""
	if err != nil {
		return err
	}
	if err := store.Upsert(account); err != nil {
		return err
	}
	fmt.Printf("Imported account %s (%s); PAT was not stored.\n", account.AnonymousID(), account.Region)
	return nil
}

func loginDevice(args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	configPath := fs.String("config", "config.json", "JSON configuration path")
	regionName := fs.String("region", "global", "global only for browser login")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *regionName != "global" {
		return errors.New("CN browser login is not verified; use import-pat --region cn")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	store, auth, _, _, _, _, err := components(cfg)
	if err != nil {
		return err
	}
	flow, err := auth.BeginDeviceFlow(protocol.Global)
	if err != nil {
		return err
	}
	fmt.Printf("Open this URL to authorize your Qoder account:\n%s\n", flow.VerificationURL)
	_ = openBrowser(flow.VerificationURL)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	account, err := auth.PollDeviceFlow(ctx, flow)
	if err != nil {
		return err
	}
	if err := store.Upsert(account); err != nil {
		return err
	}
	fmt.Printf("Saved account %s with credential file mode 0600.\n", account.AnonymousID())
	return nil
}

func models(args []string) error {
	cfg, _, err := load(args, "models")
	if err != nil {
		return err
	}
	_, _, p, catalog, _, _, err := components(cfg)
	if err != nil {
		return err
	}
	entry, err := p.Select("", nil)
	if err != nil {
		return err
	}
	account, err := p.EnsureFresh(context.Background(), entry, false)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	snap, fetchErr := catalog.Get(ctx, account)
	out := map[string]any{"models": snap.Models, "degraded": snap.Degraded}
	if fetchErr != nil {
		out["warning"] = qoder.SanitizeSnippet([]byte(fetchErr.Error()), 256)
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
	return nil
}

func doctor(args []string) error {
	cfg, _, err := load(args, "doctor")
	if err != nil {
		return err
	}
	store, auth, p, catalog, bearer, cosy, err := components(cfg)
	if err != nil {
		return err
	}
	accounts, err := store.Load()
	if err != nil {
		return err
	}
	fmt.Printf("config: ok\ncredentials: %d account(s), permissions ok\n", len(accounts))
	if len(accounts) == 0 {
		return errors.New("no accounts; run login or import-pat")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, a := range accounts {
		info, infoErr := auth.UserInfo(ctx, protocol.Region(a.Region), a.AccessToken)
		qErr := error(nil)
		_, qErr = auth.Quota(ctx, a)
		bErr := bearer.Probe(ctx, a)
		cErr := cosy.Probe(ctx, a)
		fmt.Printf("account=%s region=%s userinfo=%s quota=%s bearer=%s cosy=%s user=%s\n", a.AnonymousID(), a.Region, okErr(infoErr), okErr(qErr), okErr(bErr), okErr(cErr), safeName(info.Name))
	}
	entry, _ := p.Select("", nil)
	account, _ := p.EnsureFresh(ctx, entry, false)
	snap, catErr := catalog.Get(ctx, account)
	fmt.Printf("catalog: models=%d degraded=%v status=%s\n", len(snap.Models), snap.Degraded, okErr(catErr))
	return nil
}

func readSecret(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	if runtime.GOOS != "windows" {
		cmd := exec.Command("stty", "-echo")
		cmd.Stdin = os.Stdin
		if err := cmd.Run(); err == nil {
			defer func() {
				restore := exec.Command("stty", "echo")
				restore.Stdin = os.Stdin
				_ = restore.Run()
				fmt.Fprintln(os.Stderr)
			}()
		}
	}
	return bufio.NewReader(os.Stdin).ReadString('\n')
}
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
func okErr(err error) string {
	if err == nil {
		return "ok"
	}
	return qoder.SanitizeSnippet([]byte(err.Error()), 100)
}
func safeName(s string) string {
	if s == "" {
		return "unknown"
	}
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		return r
	}, s)
}
