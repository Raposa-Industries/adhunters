// Package archive is where sealed raw files live for good: a bucket in object
// storage in production, a folder in tests and on a laptop.
//
// Keys are the path under the spool (<network>/<yyyy>/<mm>/<dd>/<hh>/…), so
// a file keeps one name from capture to the loader. A key is written once;
// writing the same bytes again is harmless, and Put never replaces a key
// holding different bytes.
package archive

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// ErrNotFound means no object has that key.
var ErrNotFound = errors.New("archive: not found")

// ErrDifferent means the key already holds other bytes. Nothing was written.
var ErrDifferent = errors.New("archive: key holds different bytes")

// Object is what the archive says about one stored file.
type Object struct {
	Size int64
	MD5  string // hex
}

// Store is one archive.
type Store interface {
	// Put stores size bytes from r under key and checks that the archive
	// received exactly them (md5 is the hex MD5 of the bytes).
	Put(ctx context.Context, key string, r io.Reader, size int64, md5 string) error
	Stat(ctx context.Context, key string) (Object, error)
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	String() string
}

// Open opens the archive a URI names:
//
//	file:///var/lib/tracks/archive     a folder
//	s3://bucket/prefix                 object storage; endpoint and keys from
//	                                   S3_ENDPOINT, S3_ACCESS_KEY, S3_SECRET_KEY
//	                                   and S3_REGION
func Open(uri string) (Store, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}
	switch u.Scheme {
	case "file":
		if u.Path == "" {
			return nil, fmt.Errorf("archive: %s has no folder", uri)
		}
		return &Dir{Root: u.Path}, nil
	case "s3":
		return openS3(u.Host, strings.Trim(u.Path, "/"))
	}
	return nil, fmt.Errorf("archive: unknown scheme in %q (file:// or s3://)", uri)
}

func checkKey(key string) error {
	if key == "" || strings.HasPrefix(key, "/") || path.Clean(key) != key || strings.Contains(key, "..") {
		return fmt.Errorf("archive: bad key %q", key)
	}
	return nil
}

// Dir is an archive in a local folder.
type Dir struct{ Root string }

func (d *Dir) String() string { return "file://" + d.Root }

func (d *Dir) path(key string) string { return filepath.Join(d.Root, filepath.FromSlash(key)) }

func (d *Dir) Put(ctx context.Context, key string, r io.Reader, size int64, sum string) error {
	if err := checkKey(key); err != nil {
		return err
	}
	if o, err := d.Stat(ctx, key); err == nil {
		if o.Size == size && o.MD5 == sum {
			return nil
		}
		return ErrDifferent
	}
	final := d.path(key)
	if err := os.MkdirAll(filepath.Dir(final), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(final), ".put-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	h := md5.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), r)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n != size || hex.EncodeToString(h.Sum(nil)) != sum {
		return fmt.Errorf("archive: %s: got %d bytes md5 %s, want %d bytes md5 %s", key, n, hex.EncodeToString(h.Sum(nil)), size, sum)
	}
	return os.Rename(tmp.Name(), final)
}

func (d *Dir) Stat(_ context.Context, key string) (Object, error) {
	if err := checkKey(key); err != nil {
		return Object{}, err
	}
	f, err := os.Open(d.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return Object{}, ErrNotFound
	}
	if err != nil {
		return Object{}, err
	}
	defer func() { _ = f.Close() }()
	h := md5.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return Object{}, err
	}
	return Object{Size: n, MD5: hex.EncodeToString(h.Sum(nil))}, nil
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

// S3 is an archive in an S3-compatible bucket (Hetzner Object Storage).
type S3 struct {
	client *minio.Client
	bucket string
	prefix string // "" or "raw/"-like, ending in "/"
}

func openS3(bucket, prefix string) (*S3, error) {
	endpoint := os.Getenv("S3_ENDPOINT")
	if bucket == "" || endpoint == "" {
		return nil, errors.New("archive: s3 needs a bucket and S3_ENDPOINT")
	}
	c, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(os.Getenv("S3_ACCESS_KEY"), os.Getenv("S3_SECRET_KEY"), ""),
		Secure: os.Getenv("S3_INSECURE") == "",
		Region: os.Getenv("S3_REGION"),
	})
	if err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}
	if prefix != "" {
		prefix += "/"
	}
	return &S3{client: c, bucket: bucket, prefix: prefix}, nil
}

func (s *S3) String() string { return "s3://" + s.bucket + "/" + s.prefix }

func (s *S3) Put(ctx context.Context, key string, r io.Reader, size int64, sum string) error {
	if err := checkKey(key); err != nil {
		return err
	}
	if o, err := s.Stat(ctx, key); err == nil {
		if o.Size == size && o.MD5 == sum {
			return nil
		}
		return ErrDifferent
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	// Content-MD5 makes the storage refuse bytes that changed on the way.
	info, err := s.client.PutObject(ctx, s.bucket, s.prefix+key, r, size, minio.PutObjectOptions{
		ContentType:      "application/zstd",
		SendContentMd5:   true,
		DisableMultipart: true,
	})
	if err != nil {
		return fmt.Errorf("archive: put %s: %w", key, err)
	}
	if info.Size != size {
		return fmt.Errorf("archive: put %s: stored %d bytes, sent %d", key, info.Size, size)
	}
	o, err := s.Stat(ctx, key)
	if err != nil {
		return err
	}
	if o.Size != size || o.MD5 != sum {
		return fmt.Errorf("archive: %s reads back as %d bytes md5 %s, want %d bytes md5 %s", key, o.Size, o.MD5, size, sum)
	}
	return nil
}

func (s *S3) Stat(ctx context.Context, key string) (Object, error) {
	info, err := s.client.StatObject(ctx, s.bucket, s.prefix+key, minio.StatObjectOptions{})
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return Object{}, ErrNotFound
		}
		return Object{}, fmt.Errorf("archive: stat %s: %w", key, err)
	}
	// A single-part upload's ETag is the MD5 of its bytes.
	return Object{Size: info.Size, MD5: strings.Trim(info.ETag, `"`)}, nil
}

func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := checkKey(key); err != nil {
		return nil, err
	}
	if _, err := s.Stat(ctx, key); err != nil {
		return nil, err
	}
	return s.client.GetObject(ctx, s.bucket, s.prefix+key, minio.GetObjectOptions{})
}
