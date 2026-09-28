// Package files is where Raposa keeps the files of the pages it keeps whole:
// a bucket in object storage in production, a folder in tests and on a
// laptop. No file sits in the database.
//
// Files are content addressed: the key is files/<md5 of the bytes>, so one
// sales video twenty pages load is stored once, and storing the same bytes
// again is harmless.
package files

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// ErrNotFound means no file has that key.
var ErrNotFound = errors.New("files: not found")

// Store is one place files are kept.
type Store interface {
	// Put stores the file at path under Key(md5), unless a file of that size
	// is there already. md5 is the hex MD5 of its bytes.
	Put(ctx context.Context, md5, mediaType, path string, size int64) (key string, err error)
	// Get opens a stored file.
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	String() string
}

// Key is where a file with this md5 is kept.
func Key(md5hex string) string { return "files/" + md5hex }

var keyRe = regexp.MustCompile(`^files/[0-9a-f]{32}$`)

func checkKey(key string) error {
	if !keyRe.MatchString(key) {
		return fmt.Errorf("files: bad key %q", key)
	}
	return nil
}

// Open opens the store a URI names:
//
//	file:///var/lib/raposa/files    a folder
//	s3://bucket/prefix              object storage; endpoint and keys from
//	                                S3_ENDPOINT, S3_ACCESS_KEY, S3_SECRET_KEY
//	                                and S3_REGION (S3_INSECURE=1 for plain http)
func Open(uri string) (Store, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, fmt.Errorf("files: %w", err)
	}
	switch u.Scheme {
	case "file":
		if u.Path == "" {
			return nil, fmt.Errorf("files: %s has no folder", uri)
		}
		return &Dir{Root: u.Path}, nil
	case "s3":
		return openS3(u.Host, strings.Trim(u.Path, "/"))
	}
	return nil, fmt.Errorf("files: unknown scheme in %q (file:// or s3://)", uri)
}

// Sum reads a file and returns the hex MD5 of its bytes and its size.
func Sum(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()
	h := md5.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// Dir keeps files in a local folder.
type Dir struct{ Root string }

func (d *Dir) String() string { return "file://" + d.Root }

func (d *Dir) path(key string) string { return filepath.Join(d.Root, filepath.FromSlash(key)) }

func (d *Dir) Put(_ context.Context, sum, _ string, path string, size int64) (string, error) {
	key := Key(sum)
	if err := checkKey(key); err != nil {
		return "", err
	}
	final := d.path(key)
	if st, err := os.Stat(final); err == nil && st.Size() == size {
		return key, nil
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o750); err != nil {
		return "", err
	}
	src, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = src.Close() }()
	tmp, err := os.CreateTemp(filepath.Dir(final), ".put-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	h := md5.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), src)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	if got := hex.EncodeToString(h.Sum(nil)); n != size || got != sum {
		return "", fmt.Errorf("files: %s changed while it was stored: %d bytes md5 %s, want %d bytes md5 %s", path, n, got, size, sum)
	}
	return key, os.Rename(tmp.Name(), final)
}

func (d *Dir) Get(_ context.Context, key string) (io.ReadCloser, error) {
	if err := checkKey(key); err != nil {
		return nil, err
	}
	f, err := os.Open(d.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return f, err
}

// S3 keeps files in an S3-compatible bucket (Hetzner Object Storage).
type S3 struct {
	client *minio.Client
	bucket string
	prefix string // "" or ending in "/"
}

func openS3(bucket, prefix string) (*S3, error) {
	return OpenS3(S3Config{
		Endpoint:  os.Getenv("S3_ENDPOINT"),
		Region:    os.Getenv("S3_REGION"),
		AccessKey: os.Getenv("S3_ACCESS_KEY"),
		SecretKey: os.Getenv("S3_SECRET_KEY"),
		Insecure:  os.Getenv("S3_INSECURE") != "",
	}, bucket, prefix)
}

// S3Config is how to reach one object storage.
type S3Config struct {
	Endpoint  string // host[:port]; a leading https:// or http:// is dropped
	Region    string
	AccessKey string
	SecretKey string
	Insecure  bool // plain http
}

// OpenS3 opens bucket/prefix with the keys given, not the S3_ ones. The
// import reads the collector's bucket this way.
func OpenS3(cfg S3Config, bucket, prefix string) (*S3, error) {
	endpoint := strings.TrimSuffix(cfg.Endpoint, "/")
	if rest, ok := strings.CutPrefix(endpoint, "http://"); ok {
		endpoint, cfg.Insecure = rest, true
	}
	endpoint = strings.TrimPrefix(endpoint, "https://")
	if bucket == "" || endpoint == "" {
		return nil, errors.New("files: s3 needs a bucket and an endpoint")
	}
	c, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: !cfg.Insecure,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("files: %w", err)
	}
	prefix = strings.Trim(prefix, "/")
	if prefix != "" {
		prefix += "/"
	}
	return &S3{client: c, bucket: bucket, prefix: prefix}, nil
}

func (s *S3) String() string { return "s3://" + s.bucket + "/" + s.prefix }

func (s *S3) Put(ctx context.Context, sum, mediaType, path string, size int64) (string, error) {
	key := Key(sum)
	if err := checkKey(key); err != nil {
		return "", err
	}
	if info, err := s.client.StatObject(ctx, s.bucket, s.prefix+key, minio.StatObjectOptions{}); err == nil && info.Size == size {
		return key, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	// One part, with Content-MD5: the storage refuses bytes that changed on
	// the way, and the ETag stays the MD5.
	info, err := s.client.PutObject(ctx, s.bucket, s.prefix+key, f, size, minio.PutObjectOptions{
		ContentType:      mediaType,
		SendContentMd5:   true,
		DisableMultipart: true,
	})
	if err != nil {
		return "", fmt.Errorf("files: put %s: %w", key, err)
	}
	if info.Size != size {
		return "", fmt.Errorf("files: put %s: stored %d bytes, sent %d", key, info.Size, size)
	}
	return key, nil
}

func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := checkKey(key); err != nil {
		return nil, err
	}
	if _, err := s.client.StatObject(ctx, s.bucket, s.prefix+key, minio.StatObjectOptions{}); err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("files: stat %s: %w", key, err)
	}
	return s.client.GetObject(ctx, s.bucket, s.prefix+key, minio.GetObjectOptions{})
}
