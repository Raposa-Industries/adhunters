// Command create-web serves AdHunters Create's launcher page and the API it
// calls: a plan of headlines and image briefs from one prompt, and pictures
// one at a time, both from OpenAI.
//
//	create-web [-addr 127.0.0.1:8091] [-keep create-kept]
//	create-web version
//
// Every OpenAI reply it hands back is first kept in the keep folder (-keep or
// CREATE_KEEP_DIR, relative to the working directory). It listens on
// localhost unless told otherwise (CREATE_WEB_ADDR). /healthz and /metrics
// are on OPS_ADDR, 127.0.0.1:9109 by default. It stops cleanly on SIGTERM.
//
// Settings come from the environment:
//
//	OPENAI_API_KEY          unset: the page loads, generation is off
//	OPENAI_BASE_URL         https://api.openai.com (a local fake, for trying the page)
//	CREATE_IMAGE_MODEL      gpt-image-2.5-flare
//	CREATE_IMAGE_QUALITY    medium (low, medium, high)
//	CREATE_TEXT_MODEL       gpt-5-mini
//	CREATE_TEXT_REASONING   low (set it empty to send no reasoning_effort)
//	CREATE_IMAGE_PRICE_IN   10    USD per million image input tokens
//	CREATE_IMAGE_PRICE_OUT  30    USD per million image output tokens
//	CREATE_TEXT_PRICE_IN    0.25  USD per million text input tokens
//	CREATE_TEXT_PRICE_OUT   2     USD per million text output tokens
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/Raposa-Industries/adhunters/create/internal/api"
	"github.com/Raposa-Industries/adhunters/create/internal/keep"
	"github.com/Raposa-Industries/adhunters/create/internal/openai"
	"github.com/Raposa-Industries/adhunters/create/web"
	"github.com/Raposa-Industries/adhunters/kit/logx"
	"github.com/Raposa-Industries/adhunters/kit/ops"
	"github.com/Raposa-Industries/adhunters/kit/run"
)

// version is set at build time: -ldflags "-X main.version=…".
var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println(version)
		return
	}
	if err := serve(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "create-web:", err)
		os.Exit(1)
	}
}

func serve(args []string) error {
	fs := flag.NewFlagSet("create-web", flag.ExitOnError)
	addr := fs.String("addr", envOr("CREATE_WEB_ADDR", "127.0.0.1:8091"), "where the page and API listen")
	keepDir := fs.String("keep", envOr("CREATE_KEEP_DIR", "create-kept"), "folder every OpenAI reply is kept in")
	_ = fs.Parse(args)

	log := logx.New("create-web", version)
	set, err := settings()
	if err != nil {
		return err
	}
	if !openai.ValidQuality(set.ImageQuality) {
		return fmt.Errorf("CREATE_IMAGE_QUALITY must be low, medium or high, not %q", set.ImageQuality)
	}

	srv := ops.New("create-web", version)
	client := openai.New(set, srv, keep.New(*keepDir), log)
	if !client.Available() {
		log.Warn("OPENAI_API_KEY is not set: the page loads but generation is off")
	}

	mux := http.NewServeMux()
	mux.Handle("/", web.Handler())
	mux.Handle("/api/", api.New(client, log).Handler())
	// The API spends money, so a request another site makes the browser
	// send (a form posted cross-origin) is refused; the page's own fetches
	// are same-origin and pass.
	cop := http.NewCrossOriginProtection()
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"pedido vindo de outro site recusado"}` + "\n"))
	}))
	handler := web.Secure(cop.Handler(mux))

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	httpSrv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		// Long enough for one picture: a slot, up to three attempts, a slow
		// render (the API's own budget is 5.5 minutes).
		WriteTimeout: 6 * time.Minute,
		IdleTimeout:  2 * time.Minute,
	}
	log.Info("create-web listening", "addr", ln.Addr().String(), "keep", *keepDir,
		"image_model", set.ImageModel, "image_quality", set.ImageQuality, "text_model", set.TextModel,
		"generation", client.Available())

	opsAddr := envOr("OPS_ADDR", "127.0.0.1:9109")
	return run.Main(log, run.DefaultGrace, func(ctx context.Context) error {
		opsDone := make(chan error, 1)
		go func() { opsDone <- srv.Serve(ctx, log, opsAddr) }()
		served := make(chan error, 1)
		go func() { served <- httpSrv.Serve(ln) }()
		var err error
		select {
		case err = <-served:
		case <-ctx.Done():
			// Pictures in flight get most of the grace to finish and be kept;
			// what is still running after that is cut.
			shut, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			err = httpSrv.Shutdown(shut)
			cancel()
			if errors.Is(err, context.DeadlineExceeded) {
				log.Warn("calls still running at stop were cut")
				_ = httpSrv.Close()
				err = nil
			}
		}
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		if oerr := <-opsDone; err == nil {
			err = oerr
		}
		return err
	})
}

func settings() (openai.Settings, error) {
	s := openai.Settings{
		APIKey:       os.Getenv("OPENAI_API_KEY"),
		BaseURL:      os.Getenv("OPENAI_BASE_URL"),
		ImageModel:   envOr("CREATE_IMAGE_MODEL", "gpt-image-2.5-flare"),
		ImageQuality: envOr("CREATE_IMAGE_QUALITY", "medium"),
		TextModel:    envOr("CREATE_TEXT_MODEL", "gpt-5-mini"),
		// Set but empty means "send no reasoning_effort", for a text model
		// that does not reason.
		TextReasoning: "low",
	}
	if v, ok := os.LookupEnv("CREATE_TEXT_REASONING"); ok {
		s.TextReasoning = v
	}
	prices := []struct {
		key string
		def float64
		to  *float64
	}{
		{"CREATE_IMAGE_PRICE_IN", 10, &s.ImagePriceIn},
		{"CREATE_IMAGE_PRICE_OUT", 30, &s.ImagePriceOut},
		{"CREATE_TEXT_PRICE_IN", 0.25, &s.TextPriceIn},
		{"CREATE_TEXT_PRICE_OUT", 2, &s.TextPriceOut},
	}
	for _, p := range prices {
		*p.to = p.def
		if v := os.Getenv(p.key); v != "" {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil || f < 0 {
				return s, fmt.Errorf("%s must be a price in USD per million tokens, not %q", p.key, v)
			}
			*p.to = f
		}
	}
	return s, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
