package runtime

// UNPACKING THE RUNTIME. An archive is untrusted input EVEN AFTER ITS DIGEST CHECKS OUT: the digest
// proves it is the file the manifest named, not that every path inside it is harmless. The digest
// is our trust in upstream; this is our refusal to let one bad release write outside its own
// directory.
//
// The failure being prevented has a name, "zip slip": an entry called ../../.bashrc, or a symlink
// aimed at /etc, written by an extractor that joined paths without checking where they land. The
// real upstream archive DOES contain symlinks (libmtmd.so -> libmtmd.so.0), so links have to work;
// the rule is that every file AND every link target must resolve inside the destination.

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrUnsafeArchive means an entry would land outside the directory it is being unpacked into.
var ErrUnsafeArchive = errors.New("the archive tries to write outside its own directory")

// maxEntryBytes bounds one file, so a malicious archive cannot fill the disk with a decompression
// bomb. The largest real file in a runtime build is a few tens of megabytes.
const maxEntryBytes = 1 << 30

// Unpack extracts a .tar.gz or .zip into dest, refusing anything that would escape it.
func Unpack(archive, dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	root, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	// Every write goes through an os.Root over dest: the kernel follows links inside it and refuses
	// any path that leaves it, however the links chain. The checks below catch the obvious cases with
	// a clear message; the Root is what holds.
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer r.Close()
	switch {
	case strings.HasSuffix(archive, ".tar.gz"), strings.HasSuffix(archive, ".tgz"):
		return confined(untarGz(archive, root, r))
	case strings.HasSuffix(archive, ".zip"):
		return confined(unzip(archive, root, r))
	}
	return fmt.Errorf("%s is not an archive this knows how to unpack", filepath.Base(archive))
}

// confined names an os.Root refusal for what it is: the archive tried to leave its folder.
func confined(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) && pe.Err != nil && pe.Err.Error() == "path escapes from parent" {
		return fmt.Errorf("%w: %q", ErrUnsafeArchive, pe.Path)
	}
	return err
}

// inside resolves name under root and refuses anything that climbs out. It returns the path relative
// to root, for the os.Root the entry is written through.
func inside(root, name string) (string, error) {
	if filepath.IsAbs(name) || strings.HasPrefix(name, "/") || strings.HasPrefix(name, `\`) {
		return "", fmt.Errorf("%w: %q is an absolute path", ErrUnsafeArchive, name)
	}
	p := filepath.Join(root, name)
	if p != root && !strings.HasPrefix(p, root+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q", ErrUnsafeArchive, name)
	}
	return filepath.Rel(root, p)
}

func untarGz(archive, root string, r *os.Root) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("not a gzip archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("a damaged archive: %w", err)
		}
		p, err := inside(root, h.Name)
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := r.MkdirAll(p, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := writeFile(r, p, tr, os.FileMode(h.Mode)&0o777); err != nil {
				return err
			}
		case tar.TypeSymlink:
			// THE TARGET IS CHECKED AS WELL AS THE LINK. A link inside the tree that points out of
			// it is the quieter escape: everything written through it later lands wherever it aims.
			if filepath.IsAbs(h.Linkname) {
				return fmt.Errorf("%w: %q links to an absolute path", ErrUnsafeArchive, h.Name)
			}
			if _, err := inside(root, filepath.Join(filepath.Dir(h.Name), h.Linkname)); err != nil {
				return fmt.Errorf("%w: %q links out of the tree", ErrUnsafeArchive, h.Name)
			}
			if err := r.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			_ = r.Remove(p)
			if err := r.Symlink(h.Linkname, p); err != nil {
				return err
			}
		default:
			// Hard links, devices, fifos: a runtime build has no use for them and they are exactly
			// the entry types an attack would use, so they are skipped rather than honoured.
		}
	}
}

func unzip(archive, root string, r *os.Root) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return fmt.Errorf("not a zip archive: %w", err)
	}
	defer zr.Close()
	for _, zf := range zr.File {
		p, err := inside(root, zf.Name)
		if err != nil {
			return err
		}
		if zf.FileInfo().IsDir() {
			if err := r.MkdirAll(p, 0o755); err != nil {
				return err
			}
			continue
		}
		if zf.Mode()&os.ModeSymlink != 0 {
			continue // a zip symlink is not something a runtime build needs; not honoured
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		mode := zf.Mode().Perm()
		if mode == 0 {
			mode = 0o755 // zips made on Windows often carry no mode; an .exe must still run
		}
		err = writeFile(r, p, rc, mode)
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func writeFile(root *os.Root, p string, r io.Reader, mode os.FileMode) error {
	// Open first, so a parent that is a link out of the folder is refused as the escape it is; make
	// the parent only when it does not exist yet.
	out, err := root.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode|0o200)
	if errors.Is(err, os.ErrNotExist) {
		if err := root.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		out, err = root.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode|0o200)
	}
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(r, maxEntryBytes+1))
	cerr := out.Close()
	if err != nil {
		return err
	}
	if n > maxEntryBytes {
		return fmt.Errorf("%w: %s is larger than any runtime file should be", ErrUnsafeArchive, filepath.Base(p))
	}
	return cerr
}
