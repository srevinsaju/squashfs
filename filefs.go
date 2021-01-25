package squashfs

import (
	"errors"
	"io/fs"
	"path"
	"strings"
)

//FileFS is a fs.FS of a File
type FileFS struct {
	f *File
}

//Open returns an fs.File at the given name. If name == "", it returns the File of this FileFS
func (f FileFS) Open(name string) (fs.File, error) {
	if name == "" {
		return f.f, nil
	}
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{
			Op:   "open",
			Path: name,
			Err:  fs.ErrInvalid,
		}
	}
	if strings.HasPrefix(name, "/") {
		name = strings.TrimPrefix(name, "/")
	}
	name = path.Clean(name)
	if name == "" || name == "." {
		return f.f, nil
	}
	if strings.HasPrefix(name, "../") {
		return nil, &fs.PathError{
			Op:   "open",
			Path: name,
			Err:  fs.ErrInvalid,
		}
	}
	split := strings.Split(name, "/")
	dirs, err := f.f.ReadDir(0)
	if err != nil {
		return nil, &fs.PathError{
			Op:   "open",
			Path: name,
			Err:  err,
		}
	}
	for i := 0; i < len(dirs); i++ {
		var match bool
		if match, err = path.Match(split[0], dirs[i].Name()); match {
			var info fs.FileInfo
			info, err = dirs[i].Info()
			if err != nil {
				return nil, &fs.PathError{
					Op:   "open",
					Path: name,
					Err:  err,
				}
			}
			fil, ok := info.(*File)
			if !ok {
				return nil, &fs.PathError{
					Op:   "open",
					Path: name,
					Err:  errors.New("Cannot get underlying data"),
				}
			}
			if len(split) == 1 {
				return fil, nil
			} else if fil.IsDir() {
				var filFS *FileFS
				filFS, err = fil.AsFS()
				if err != nil {
					return nil, &fs.PathError{
						Op:   "open",
						Path: name,
						Err:  fs.ErrNotExist,
					}
				}
				return filFS.Open(strings.Join(split[1:], "/"))
			} else {
				return nil, &fs.PathError{
					Op:   "open",
					Path: name,
					Err:  fs.ErrNotExist,
				}
			}
		} else if err != nil {
			return nil, &fs.PathError{
				Op:   "open",
				Path: name,
				Err:  err,
			}
		}
	}
	return nil, &fs.PathError{
		Op:   "open",
		Path: name,
		Err:  fs.ErrNotExist,
	}
}

//Sub returns an fs.FS at the given dir.
func (f *FileFS) Sub(dir string) (fs.FS, error) {
	fil, err := f.Open(dir)
	if err != nil {
		if pathErr, ok := err.(*fs.PathError); ok {
			pathErr.Op = "sub"
			pathErr.Path = dir
			return nil, pathErr
		}
		return nil, err
	}
	if f, ok := fil.(*File); ok {
		return f.AsFS()
	}
	return nil, &fs.PathError{
		Op:   "open",
		Path: dir,
		Err:  errors.New("Cannot get underlying data"),
	}
}

//ExtractTo extract to dir with default extraction options.
func (f *FileFS) ExtractTo(dir string) []error {
	return f.f.ExtractTo(dir)
}

//ExtractSymlink extract to dir while replacing all symlinks with their target.
func (f *FileFS) ExtractSymlink(dir string) []error {
	return f.f.ExtractSymlink(dir)
}

//ExtractWithOptions extract to dir with customized options.
func (f *FileFS) ExtractWithOptions(dir string, op ExtractionOptions) []error {
	return f.f.ExtractWithOptions(dir, op)
}
