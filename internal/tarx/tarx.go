package tarx

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egdaemon/egt/internal/errorsx"
	"github.com/egdaemon/egt/internal/iox"
)

const (
	Mimetype = "application/tar+gzip"
)

// NewAppendWriter rewind two empty pages and return a tar writer to continue appending
func NewAppendWriter(dest io.WriteSeeker) (tw *tar.Writer, err error) {
	if _, err = dest.Seek(-1<<10, io.SeekEnd); err != nil {
		return tw, errorsx.Wrap(err, "failure to remove blank headers")
	}

	return tar.NewWriter(dest), err
}

// HeaderFromReader creates a header from a filename and reader.
func HeaderFromReader(filename string, reader *strings.Reader) (hdr *tar.Header) {
	return &tar.Header{
		Name: filename,
		Mode: 0600,
		Size: reader.Size(),
	}
}

// NewHeader creates a new header.
func NewHeader(filename string, ts time.Time, size, mode int64) (hdr *tar.Header) {
	return &tar.Header{
		Name:       filename,
		Mode:       0600,
		Size:       size,
		ChangeTime: ts,
	}
}

func NewHeaderFromSeeker(filename string, in io.Seeker) (hdr *tar.Header, err error) {
	var (
		offset int64
	)

	if offset, err = in.Seek(0, io.SeekEnd); err != nil {
		return nil, err
	}

	if _, err = in.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	return &tar.Header{
		Name: filename,
		Mode: 0600,
		Size: offset,
	}, nil
}

// CreateArchiveWith creates an archive with an initial header, reader, and writer.
func CreateArchiveWith(dst io.Writer, hdr *tar.Header, reader io.Reader) (tw *tar.Writer, err error) {
	tw = tar.NewWriter(dst)
	if err = tw.WriteHeader(hdr); err != nil {
		return tw, errorsx.Wrap(err, "failed to write header to temp file")
	}

	if _, err = io.Copy(tw, reader); err != nil {
		return tw, errorsx.Wrap(err, "failed to copy content")
	}

	return tw, nil
}

// WriteFileToArchive write a file to a tar given the contents and a header.
func WriteFileToArchive(tw *tar.Writer, hdr *tar.Header, reader io.Reader) (err error) {
	if err = tw.WriteHeader(hdr); err != nil {
		return errorsx.Wrap(err, "failed to write header for tar archive")
	}

	if _, err = io.Copy(tw, reader); err != nil {
		return errorsx.Wrap(err, "failed to copy content")
	}

	return nil
}

// WriteToArchive write Seekable content to a tar given the contents and a header.
func WriteToArchive(tw *tar.Writer, filename string, in io.ReadSeeker) (err error) {
	var (
		hdr *tar.Header
	)

	if hdr, err = NewHeaderFromSeeker(filename, in); err != nil {
		return errorsx.Wrap(err, "failed to create header for tar archive")
	}

	if err = tw.WriteHeader(hdr); err != nil {
		return errorsx.Wrap(err, "failed to write header for tar archive")
	}

	if _, err = io.Copy(tw, in); err != nil {
		return errorsx.Wrap(err, "failed to copy content")
	}

	return nil
}

// prints to stderr information about the archive
func Inspect(ctx context.Context, r io.Reader) (err error) {
	var (
		gzr *gzip.Reader
		tr  *tar.Reader
	)

	if s, ok := r.(io.Seeker); ok {
		if err = iox.Rewind(s); err != nil {
			return errorsx.Wrap(err, "unable to seek to start of file")
		}

		defer func() { errorsx.MaybeLog(errorsx.Wrap(iox.Rewind(s), "unable to rewind")) }()
	}

	if gzr, err = gzip.NewReader(r); err != nil {
		return errorsx.Wrap(err, "failed to create gzip reader")
	}
	defer gzr.Close()

	tr = tar.NewReader(gzr)

	for {
		header, err := tr.Next()
		switch {
		// if no more files are found return
		case err == io.EOF:
			return nil
		// return any other error
		case err != nil:
			return err
		// if the header is nil, just skip it (not sure how this happens)
		case header == nil:
			continue
		}

		switch header.Typeflag {
		// if its a dir ignore
		case tar.TypeDir:
		// if it's a file create it
		case tar.TypeReg:
			log.Println("reg", header.Name, header.Size, header.FileInfo().Size())
		}
	}
}

// Pack the set of paths into the archive. caller is responsible for rewinding the writer.
func Pack(dst io.Writer, paths ...string) (err error) {
	var (
		gw *gzip.Writer
		tw *tar.Writer
	)

	if s, ok := dst.(io.Seeker); ok {
		defer errorsx.MaybeLog(errorsx.Wrap(iox.Rewind(s), "unable to rewind archive"))
	}

	gw = gzip.NewWriter(dst)
	defer gw.Close()
	tw = tar.NewWriter(gw)
	defer tw.Close()

	for _, basepath := range paths {
		walker := func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			// skip the root directory itself.
			if basepath == path && info.IsDir() {
				return nil
			}

			return write(basepath, path, tw, info)
		}

		if err = filepath.Walk(basepath, walker); err != nil {
			return err
		}
	}

	return errorsx.Wrap(tw.Flush(), "failed to flush archive")
}

func write(basepath, path string, tw *tar.Writer, info os.FileInfo) (err error) {
	var (
		src    *os.File
		header *tar.Header
		target string
	)

	if target, err = filepath.Rel(basepath, path); err != nil {
		return errorsx.Wrapf(err, "failed to compute path: %s", path)
	}

	// base and path are identical
	if target == "." {
		target = filepath.Base(path)
	}

	// log.Println("writing", path, "->", target)
	if src, err = os.Open(path); err != nil {
		return errorsx.Wrap(err, "failed to open path")
	}
	defer src.Close()

	if header, err = tar.FileInfoHeader(info, path); err != nil {
		return errorsx.Wrap(err, "failed to created header")
	}
	header.Name = target

	if err = tw.WriteHeader(header); err != nil {
		return errorsx.Wrapf(err, "failed to write header to tar archive: %s", path)
	}

	// return on directories since there will be no content to tar
	if info.Mode().IsDir() {
		return nil
	}

	if _, err = io.Copy(tw, src); err != nil {
		return errorsx.Wrapf(err, "failed to write contexts to tar archive: %s", path)
	}

	return nil
}
