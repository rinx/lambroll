package lambroll

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/fujiwara/lambroll/wildcard"
)

type ArchiveOption struct {
	Src  string `help:"function zip archive or src dir" default:"."`
	Dest string `help:"destination file path" default:"function.zip"`

	ExcludeFileOption
}

// Archive archives zip
func (app *App) Archive(ctx context.Context, opt *ArchiveOption) error {
	if err := opt.Expand(); err != nil {
		return err
	}

	zipfile, _, err := createZipArchive(opt.Src, opt.excludes)
	if err != nil {
		return err
	}
	defer zipfile.Close()
	var w io.WriteCloser
	if opt.Dest == "-" {
		log.Printf("[info] writing zip archive to stdout")
		w = os.Stdout
	} else {
		log.Printf("[info] writing zip archive to %s", opt.Dest)
		w, err = os.Create(opt.Dest)
		if err != nil {
			return fmt.Errorf("failed to create %s: %w", opt.Dest, err)
		}
		defer w.Close()
	}
	_, err = io.Copy(w, zipfile)
	return err
}

func loadZipArchive(src string) (*os.File, os.FileInfo, error) {
	log.Printf("[info] reading zip archive from %s", src)
	r, err := zip.OpenReader(src)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open zip file %s: %w", src, err)
	}
	for _, f := range r.File {
		header := f.FileHeader
		log.Printf("[debug] %s %10d %s %s",
			header.Mode(),
			header.UncompressedSize64,
			header.Modified.Format(time.RFC3339),
			header.Name,
		)
	}
	r.Close()
	info, err := os.Stat(src)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to stat %s: %w", src, err)
	}
	log.Printf("[info] zip archive %d bytes", info.Size())
	fh, err := os.Open(src)
	return fh, info, err
}

// createZipArchive creates a zip archive
func createZipArchive(src string, excludes []string) (*os.File, os.FileInfo, error) {
	log.Printf("[info] creating zip archive from %s", src)
	tmpfile, err := os.CreateTemp("", "archive")
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open tempFile: %w", err)
	}
	w := zip.NewWriter(tmpfile)
	err = filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		log.Println("[trace] waking", path)
		if err != nil {
			log.Println("[error] failed to walking dir in", src)
			return err
		}
		if info.IsDir() {
			return nil
		}
		relpath, _ := filepath.Rel(src, path)
		if matchExcludes(relpath, excludes) {
			log.Println("[trace] skipping", relpath)
			return nil
		}
		log.Println("[trace] adding", relpath)
		return addToZip(w, path, relpath, info)
	})
	if err := w.Close(); err != nil {
		return nil, nil, fmt.Errorf("failed to create zip archive: %w", err)
	}
	tmpfile.Seek(0, os.SEEK_SET)
	stat, _ := tmpfile.Stat()
	log.Printf("[info] zip archive wrote %d bytes", stat.Size())
	return tmpfile, stat, err
}

func matchExcludes(path string, excludes []string) bool {
	for _, pattern := range excludes {
		if wildcard.Match(pattern, path) {
			return true
		}
	}
	return false
}

func addToZip(z *zip.Writer, path, relpath string, info os.FileInfo) error {
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		log.Println("[error] failed to create zip file header", err)
		return err
	}
	header.Name = relpath // fix name as subdir
	header.Method = zip.Deflate
	w, err := z.CreateHeader(header)
	if err != nil {
		log.Println("[error] failed to create in zip", err)
		return err
	}
	r, err := os.Open(path)
	if err != nil {
		log.Printf("[error] failed to open %s: %s", path, err)
		return err
	}
	defer r.Close()
	_, err = io.Copy(w, r)
	log.Printf("[debug] %s %10d %s %s",
		header.Mode(),
		header.UncompressedSize64,
		header.Modified.Format(time.RFC3339),
		header.Name,
	)
	return err
}

func (app *App) uploadFunctionToS3(ctx context.Context, f *os.File, bucket, key string) (string, error) {
	svc := s3.NewFromConfig(app.awsConfig)
	log.Printf("[debug] PutObject to s3://%s/%s", bucket, key)
	res, err := svc.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   f,
	})
	if err != nil {
		return "", err
	}
	if res.VersionId != nil {
		return *res.VersionId, nil
	}
	return "", nil // not versioned
}
