package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// commands are the services the path runs through, built from the repo.
var commands = []string{
	"./tracks/cmd/tracks-loader",
	"./spy/cmd/spy-numbers",
	"./intel/cmd/intel-numbers",
	"./create/cmd/create",
	"./library/cmd/library",
	"./launch/cmd/launch-web",
}

var built struct {
	once sync.Once
	dir  string
	err  error
}

// repoRoot is the monorepo, one folder up from this module.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// build compiles every command once per test run, as the deploy does.
func build(t *testing.T) string {
	t.Helper()
	built.once.Do(func() {
		built.dir, built.err = os.MkdirTemp("", "e2e-bin-")
		if built.err != nil {
			return
		}
		cmd := exec.Command("go", append([]string{"build", "-o", built.dir + "/"}, commands...)...)
		cmd.Dir = repoRoot(t)
		out, err := cmd.CombinedOutput()
		if err != nil {
			built.err = fmt.Errorf("go build: %v\n%s", err, out)
		}
	})
	if built.err != nil {
		t.Fatal(built.err)
	}
	return built.dir
}

// database makes an empty database on PG_TEST_URL's server and drops it
// when the test ends. Every service migrates into it, as on the data box.
func database(t *testing.T) (string, *pgxpool.Pool) {
	t.Helper()
	base := os.Getenv("PG_TEST_URL")
	if base == "" {
		t.Skip("PG_TEST_URL not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "e2e_" + hex.EncodeToString(b)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	pool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		if os.Getenv("E2E_KEEP_DB") != "" {
			t.Logf("database kept: %s", name)
		} else {
			_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		}
		_ = admin.Close(ctx)
	})
	return u.String(), pool
}

// freeAddr is a localhost address nothing listens on yet.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// logBuffer keeps a process's output for the failure report.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// service is one running binary.
type service struct {
	name string
	cmd  *exec.Cmd
	out  *logBuffer
	done chan error
}

// run runs a command to the end (a migrate), failing the test with its
// output when it fails.
func run(t *testing.T, bin string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", filepath.Base(bin), strings.Join(args, " "), err, out)
	}
	return string(out)
}

// start runs a service and waits until its /healthz on opsAddr answers 200.
// When the test ends it sends SIGTERM and checks the service stopped
// cleanly within 30 s (kit/run). Its output is printed when the test fails.
func start(t *testing.T, bin, opsAddr string, env []string, args ...string) *service {
	t.Helper()
	s := &service{name: filepath.Base(bin), out: &logBuffer{}, done: make(chan error, 1)}
	s.cmd = exec.Command(bin, args...)
	s.cmd.Env = append(os.Environ(), append(env, "OPS_ADDR="+opsAddr)...)
	s.cmd.Stdout, s.cmd.Stderr = s.out, s.out
	if err := s.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { s.done <- s.cmd.Wait() }()
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("---- %s output ----\n%s", s.name, s.out.String())
		}
	})
	t.Cleanup(func() {
		_ = s.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-s.done:
			if err != nil {
				t.Errorf("%s did not stop cleanly on SIGTERM: %v", s.name, err)
			}
		case <-time.After(30 * time.Second):
			_ = s.cmd.Process.Kill()
			t.Errorf("%s still running 30 s after SIGTERM", s.name)
		}
	})
	deadline := time.Now().Add(60 * time.Second)
	for {
		select {
		case err := <-s.done:
			t.Fatalf("%s exited at start: %v\n%s", s.name, err, s.out.String())
		default:
		}
		res, err := http.Get("http://" + opsAddr + "/healthz")
		if err == nil {
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return s
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s not healthy after 60 s (last: %v)\n%s", s.name, errors.Join(err), s.out.String())
		}
		time.Sleep(200 * time.Millisecond)
	}
}
