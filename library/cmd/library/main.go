// Command library keeps the creatives and headlines Create and Launch share,
// with a copy of each file in the team's Google Drive folder.
//
//	library               serve the API and run the Drive sync
//	library drive-login   sign in to Google once, as the folder's owner
//	library version
//
// Settings come from the environment (library/deploy/library.env.example).
// drive-login also reads /etc/adhunters/library.env (-env) for anything the
// environment does not set, so it runs as a plain command on the data box.
// The API listens on localhost; /healthz and /metrics are on OPS_ADDR. It
// stops cleanly on SIGTERM.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Raposa-Industries/adhunters/kit/logx"
	"github.com/Raposa-Industries/adhunters/kit/migrate"
	"github.com/Raposa-Industries/adhunters/kit/ops"
	"github.com/Raposa-Industries/adhunters/kit/pg"
	"github.com/Raposa-Industries/adhunters/kit/run"
	"github.com/Raposa-Industries/adhunters/library/internal/drive"
	"github.com/Raposa-Industries/adhunters/library/internal/drivesync"
	"github.com/Raposa-Industries/adhunters/library/internal/store"
	"github.com/Raposa-Industries/adhunters/library/internal/web"
	"github.com/Raposa-Industries/adhunters/library/migrations"
	"github.com/Raposa-Industries/adhunters/shared/files"
)

// version is set at build time: -ldflags "-X main.version=…".
var version = "dev"

func main() {
	var err error
	switch {
	case len(os.Args) > 1 && os.Args[1] == "version":
		fmt.Println(version)
		return
	case len(os.Args) > 1 && os.Args[1] == "drive-login":
		err = driveLogin(os.Args[2:])
	default:
		err = serve(os.Args[1:])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "library:", err)
		os.Exit(1)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func openDB(ctx context.Context, app string, log *slog.Logger) (*pgxpool.Pool, error) {
	raw := os.Getenv("DATABASE_URL")
	if raw == "" {
		return nil, errors.New("DATABASE_URL is not set")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("DATABASE_URL: %w", err)
	}
	q := u.Query()
	q.Set("timezone", "UTC")
	u.RawQuery = q.Encode()
	db, err := pg.Open(ctx, pg.Config{URL: u.String(), AppName: app, StatementTimeout: pg.WebStatementTimeout, MaxConns: 6})
	if err != nil {
		return nil, err
	}
	migs, err := migrations.Load()
	if err != nil {
		db.Close()
		return nil, err
	}
	if _, err := migrate.Up(ctx, db, log, migrations.Schema, migs); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func googleApp() drive.App {
	return drive.App{ClientID: os.Getenv("LIBRARY_GOOGLE_CLIENT_ID"), ClientSecret: os.Getenv("LIBRARY_GOOGLE_CLIENT_SECRET")}
}

func serve(args []string) error {
	fs := flag.NewFlagSet("library", flag.ExitOnError)
	addr := fs.String("addr", envOr("LIBRARY_ADDR", "127.0.0.1:8093"), "where the API listens")
	filesURI := fs.String("files", envOr("LIBRARY_FILES", "file:///var/lib/library/files"), "where safe copies are kept: file:///path or s3://bucket/prefix")
	folder := fs.String("drive-folder", os.Getenv("LIBRARY_DRIVE_FOLDER"), "the id of the team's library folder in Google Drive")
	every := fs.Duration("drive-every", 5*time.Minute, "time between Drive passes")
	_ = fs.Parse(args)

	log := logx.New("library", version)
	store0, err := files.Open(*filesURI)
	if err != nil {
		return err
	}
	ctx := context.Background()
	db, err := openDB(ctx, "library", log)
	if err != nil {
		return err
	}
	defer db.Close()
	st := store.New(db, store0)

	srv := ops.New("library", version)
	srv.AddCheck("database", func(ctx context.Context) error { return db.Ping(ctx) })
	tasks := srv.Tasks()
	tasks.Promise("drive-sync", 3**every)

	loop := &driveLoop{st: st, files: store0, folder: *folder, app: googleApp(), log: log, kick: make(chan struct{}, 1)}
	loop.refreshWhy(ctx)
	api := web.New(st, loop, log)

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	httpSrv := &http.Server{Handler: api.Handler(), ReadHeaderTimeout: 10 * time.Second}
	log.Info("library listening", "addr", ln.Addr().String(), "files", store0.String(), "drive_folder", *folder)

	return run.Main(log, run.DefaultGrace, func(ctx context.Context) error {
		opsDone := make(chan error, 1)
		go func() { opsDone <- srv.Serve(ctx, log, ops.Addr()) }()
		loopDone := make(chan struct{})
		go func() { loop.run(ctx, *every, tasks); close(loopDone) }()
		served := make(chan error, 1)
		go func() { served <- httpSrv.Serve(ln) }()
		var err error
		select {
		case err = <-served:
		case <-ctx.Done():
			shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err = httpSrv.Shutdown(shut)
			cancel()
		}
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		<-loopDone
		if oerr := <-opsDone; err == nil {
			err = oerr
		}
		return err
	})
}

// driveLoop runs Drive passes on a timer and when kicked. It reads the
// sign-in before each pass, so drive-login takes effect without a restart.
type driveLoop struct {
	st     *store.Store
	files  files.Store
	folder string
	app    drive.App
	log    *slog.Logger
	kick   chan struct{}

	mu  sync.Mutex
	why string
}

func (l *driveLoop) Kick() {
	select {
	case l.kick <- struct{}{}:
	default:
	}
}

func (l *driveLoop) Folder() string { return l.folder }

func (l *driveLoop) Why() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.why
}

func (l *driveLoop) setWhy(s string) {
	l.mu.Lock()
	l.why = s
	l.mu.Unlock()
}

// refreshWhy works out whether Drive can run, and returns the refresh token
// when it can.
func (l *driveLoop) refreshWhy(ctx context.Context) string {
	switch {
	case l.folder == "":
		l.setWhy("LIBRARY_DRIVE_FOLDER is not set")
		return ""
	case l.app.ClientID == "" || l.app.ClientSecret == "":
		l.setWhy("LIBRARY_GOOGLE_CLIENT_ID and LIBRARY_GOOGLE_CLIENT_SECRET are not set")
		return ""
	}
	var token string
	err := l.st.DB().QueryRow(ctx, `SELECT refresh_token FROM library.drive_login WHERE client_id = $1`, l.app.ClientID).Scan(&token)
	if errors.Is(err, pgx.ErrNoRows) {
		l.setWhy("not signed in to Google yet: run library drive-login on the data box")
		return ""
	}
	if err != nil {
		l.setWhy("the sign-in could not be read: " + err.Error())
		return ""
	}
	l.setWhy("")
	return token
}

func (l *driveLoop) run(ctx context.Context, every time.Duration, tasks *ops.Tasks) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if token := l.refreshWhy(ctx); token != "" {
			start := time.Now()
			sy := drivesync.New(l.st, l.files, drive.New(l.app, token), l.folder, l.log)
			res, err := sy.Run(ctx)
			if ctx.Err() != nil {
				return
			}
			tasks.Done("drive-sync", start, int64(res.Written+res.Added), err)
			if errors.Is(err, drive.ErrSignedOut) {
				l.setWhy("Google refused the sign-in: run library drive-login again")
			}
			if err != nil {
				l.log.Error("drive pass failed", "err", err, "written", res.Written, "added", res.Added)
			} else if res.Written+res.Added+res.Gone > 0 {
				l.log.Info("drive pass", "written", res.Written, "listed", res.Listed, "added", res.Added, "gone", res.Gone)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-l.kick:
		}
	}
}

// ---- drive-login ---------------------------------------------------------------

// loadEnvFile sets each KEY=VALUE line of path the environment does not set.
func loadEnvFile(path string) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if _, set := os.LookupEnv(k); !set {
			_ = os.Setenv(k, strings.Trim(v, `"'`))
		}
	}
	return nil
}

func driveLogin(args []string) error {
	fs := flag.NewFlagSet("library drive-login", flag.ExitOnError)
	envFile := fs.String("env", "/etc/adhunters/library.env", "settings file to read")
	_ = fs.Parse(args)
	if err := loadEnvFile(*envFile); err != nil {
		return err
	}
	folder := os.Getenv("LIBRARY_DRIVE_FOLDER")
	if folder == "" {
		return errors.New("LIBRARY_DRIVE_FOLDER is not set")
	}
	app := googleApp()
	login, err := drive.StartLogin(app)
	if err != nil {
		return err
	}
	fmt.Println("1. Open this link in your browser, signed in as the Google account that owns the library folder:")
	fmt.Println()
	fmt.Println(login.URL())
	fmt.Println()
	fmt.Println("2. Allow access. Google may say the app is not verified: click Advanced, then Go to the app.")
	fmt.Println("3. The browser ends on a page that does not load. Copy the whole address from the address bar and paste it here:")
	fmt.Print("> ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return errors.New("nothing was pasted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	token, err := login.Finish(ctx, line)
	if err != nil {
		return err
	}
	c := drive.New(app, token)
	who, err := c.Account(ctx)
	if err != nil {
		return err
	}
	f, err := c.Get(ctx, folder)
	if err != nil {
		return fmt.Errorf("signed in as %s, but that account cannot open the library folder %s: %w", who, folder, err)
	}
	if !f.IsFolder() {
		return fmt.Errorf("LIBRARY_DRIVE_FOLDER %s is not a folder", folder)
	}
	log := logx.NewTo(os.Stderr, "library", version, "warn")
	db, err := openDB(ctx, "library-login", log)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec(ctx, `
		INSERT INTO library.drive_login (id, account, client_id, refresh_token, signed_in_at) VALUES (1, $1, $2, $3, now())
		ON CONFLICT (id) DO UPDATE SET account = EXCLUDED.account, client_id = EXCLUDED.client_id,
		    refresh_token = EXCLUDED.refresh_token, signed_in_at = now()`, who, app.ClientID, token); err != nil {
		return err
	}
	fmt.Printf("\nSigned in as %s. The library folder is %q. The library starts copying within 5 minutes.\n", who, f.Name)
	return nil
}
