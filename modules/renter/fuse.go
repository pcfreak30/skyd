package renter

import (
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hanwen/go-fuse/fuse"
	"github.com/hanwen/go-fuse/fuse/nodefs"
	"github.com/hanwen/go-fuse/fuse/pathfs"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/errors"
)

// MountInfo returns the list of currently mounted FUSE filesystems.
func (r *Renter) MountInfo() []modules.MountInfo {
	return r.staticFUSEManager.mountInfo()
}

// Mount mounts the files under the specified siapath under the 'mountPoint' folder on
// the local filesystem.
func (r *Renter) Mount(mountPoint string, sp modules.SiaPath, opts modules.MountOptions) error {
	return r.staticFUSEManager.mount(mountPoint, sp, opts)
}

// Unmount unmounts the FUSE filesystem currently mounted at mountPoint.
func (r *Renter) Unmount(mountPoint string) error {
	return r.staticFUSEManager.unmount(mountPoint)
}

// A fuseManager manages mounted FUSE filesystems.
type fuseManager struct {
	mountPoints map[string]*fuseFS
	r           *Renter
	mu          sync.Mutex
}

// mountInfo returns the list of currently mounted FUSE filesystems.
func (fm *fuseManager) mountInfo() []modules.MountInfo {
	if err := fm.r.tg.Add(); err != nil {
		return nil
	}
	defer fm.r.tg.Done()
	fm.mu.Lock()
	defer fm.mu.Unlock()
	var infos []modules.MountInfo
	for mountPoint, fs := range fm.mountPoints {
		infos = append(infos, modules.MountInfo{
			MountPoint: mountPoint,
			SiaPath:    fs.root,
		})
	}
	return infos
}

// Close unmounts all currently-mounted filesystems.
func (fm *fuseManager) Close() error {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	// unmount any mounted FUSE filesystems
	for path, fs := range fm.mountPoints {
		delete(fm.mountPoints, path)
		fs.srv.Unmount()
	}
	return nil
}

// newFUSEManager returns a new fuseManager.
func newFUSEManager(r *Renter) *fuseManager {
	return &fuseManager{
		mountPoints: make(map[string]*fuseFS),
		r:           r,
	}
}

// mount mounts the files under the specified siapath under the 'mountPoint' folder on
// the local filesystem.
func (fm *fuseManager) mount(mountPoint string, sp modules.SiaPath, opts modules.MountOptions) error {
	if err := fm.r.tg.Add(); err != nil {
		return err
	}
	defer fm.r.tg.Done()
	fm.mu.Lock()
	_, ok := fm.mountPoints[mountPoint]
	fm.mu.Unlock()
	if ok {
		return errors.New("already mounted")
	}
	if !opts.ReadOnly {
		return errors.New("writable FUSE is not supported")
	}

	fs := &fuseFS{
		FileSystem: pathfs.NewDefaultFileSystem(),
		root:       sp,
		renter:     fm.r,
		opts:       opts,
		cacheState: make(map[modules.SiaPath]cacheInfo),
		shutdown:   make(chan struct{}),
	}
	if opts.CachePath != "" {
		go fs.fillCache()
	}
	nfs := pathfs.NewPathNodeFs(fs, nil)
	mountOpts := &fuse.MountOptions{
		// Allow non-permissioned users to access the FUSE mount. This makes
		// things easier when using Docker.
		AllowOther: true,
		// By default, the kernel will "helpfully" read additional data beyond
		// what the syscall requested. This *might* be helpful, except that it
		// does so by requesting multiple sections in parallel...which ends up
		// invalidating the Streamer cache. Apparently the only way to disable
		// this is to set a very small MaxReadAhead value. (We can't set it to
		// 0, or the fuse package will assume we want the default; and we can't
		// set it to -1, because the fuse package converts it to a uint32 before
		// checking.)
		MaxReadAhead: 1,
	}
	server, _, err := nodefs.Mount(mountPoint, nfs.Root(), mountOpts, nil)
	if err != nil {
		return err
	}
	go func() {
		server.Serve()
		close(fs.shutdown) // kills fillCache goroutine
	}()
	fs.srv = server

	fm.mu.Lock()
	fm.mountPoints[mountPoint] = fs
	fm.mu.Unlock()
	return nil
}

// unmount unmounts the FUSE filesystem currently mounted at mountPoint.
func (fm *fuseManager) unmount(mountPoint string) error {
	if err := fm.r.tg.Add(); err != nil {
		return err
	}
	defer fm.r.tg.Done()

	fm.mu.Lock()
	f, ok := fm.mountPoints[mountPoint]
	delete(fm.mountPoints, mountPoint)
	fm.mu.Unlock()

	if !ok {
		return errors.New("nothing mounted at that path")
	}
	return errors.AddContext(f.srv.Unmount(), "failed to unmount filesystem")
}

type cacheInfo struct {
	size int64 // TODO: split into multiple dynamic ranges
}

// fuseFS implements pathfs.FileSystem using a modules.Renter.
type fuseFS struct {
	pathfs.FileSystem
	srv        *fuse.Server
	renter     *Renter
	root       modules.SiaPath
	opts       modules.MountOptions
	cacheState map[modules.SiaPath]cacheInfo
	shutdown   chan struct{}
	mu         sync.Mutex
}

// path converts name to a siapath.
func (fs *fuseFS) path(name string) (modules.SiaPath, bool) {
	if strings.HasPrefix(name, ".") {
		// opening a "hidden" siafile results in a panic
		return modules.SiaPath{}, false
	}
	sp, err := modules.NewSiaPath(name)
	return sp, err == nil
}

// errToStatus converts a Go error to a fuse.Status code and returns it. The Go
// error is written to the renter's log.
func (fs *fuseFS) errToStatus(op, name string, err error) fuse.Status {
	if err == nil {
		return fuse.OK
	} else if os.IsNotExist(err) {
		return fuse.ENOENT
	}
	fs.renter.log.Printf("%v %v: %v", op, name, err)
	return fuse.EIO
}

// stat returns the os.FileInfo for the named file.
func (fs *fuseFS) stat(path modules.SiaPath) (os.FileInfo, error) {
	fi, err := fs.renter.File(path)
	if err != nil {
		// not a file; might be a directory
		return fs.renter.staticDirSet.DirInfo(path)
	}
	return fi, nil
}

// fillCache continuously fills the on-disk cache with file data.
func (fs *fuseFS) fillCache() {
	// ensure that cache dir exists
	_ = os.MkdirAll(fs.opts.CachePath, 0700)

	// determine initial cache state
	err := filepath.Walk(fs.opts.CachePath, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			name := strings.TrimPrefix(path, fs.opts.CachePath+string(filepath.Separator))
			sp, ok := fs.path(name)
			if ok {
				fs.mu.Lock()
				fs.cacheState[sp] = cacheInfo{size: info.Size()}
				fs.mu.Unlock()
			}
		}
		return nil
	})
	if err != nil {
		fs.renter.log.Println("Failed to initialize cache:", err)
		return
	}

	for {
		// check for uncached files every 15 seconds
		select {
		case <-time.After(15 * time.Second):
		case <-fs.shutdown:
			return
		}
		infos, err := fs.renter.FileList(fs.root, true, false)
		if err != nil {
			fs.renter.log.Printf("Could not check cache: %v", err)
			continue
		}
		for _, info := range infos {
			err = func() error {
				cacheTarget := int64(90e6 + 10e6) // 100 MB
				if info.Size() < cacheTarget {
					cacheTarget = info.Size()
				}
				fs.mu.Lock()
				ci := fs.cacheState[info.SiaPath]
				fs.mu.Unlock()
				if ci.size >= cacheTarget {
					return nil
				}
				fs.renter.log.Printf("Caching %v bytes of %v (%v bytes already cached)", cacheTarget-ci.size, info.SiaPath, ci.size)
				cacheFilePath := filepath.Join(fs.opts.CachePath, info.SiaPath.String())
				os.MkdirAll(filepath.Dir(cacheFilePath), 0700)
				cacheFile, err := os.OpenFile(cacheFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
				if err != nil {
					return err
				}
				defer cacheFile.Close()
				_, s, err := fs.renter.Streamer(info.SiaPath)
				if err != nil {
					return err
				}
				defer s.Close()
				if _, err := s.Seek(ci.size, io.SeekStart); err != nil {
					return err
				}
				if _, err := io.CopyN(cacheFile, s, 90e6-ci.size); err != nil {
					return err
				}
				if _, err := s.Seek(-10e6, io.SeekEnd); err != nil {
					return err
				}
				if _, err := io.Copy(cacheFile, s); err != nil {
					return err
				}
				ci.size = cacheTarget
				fs.mu.Lock()
				fs.cacheState[info.SiaPath] = ci
				fs.mu.Unlock()
				return nil
			}()
			if err != nil {
				fs.renter.log.Printf("Failed to cache file %v: %v", info.SiaPath, err)
			}
		}
	}
}

// GetAttr implements pathfs.FileSystem.
func (fs *fuseFS) GetAttr(name string, _ *fuse.Context) (*fuse.Attr, fuse.Status) {
	if name == "" {
		name = fs.root.String()
	}
	sp, ok := fs.path(name)
	if !ok {
		return nil, fuse.ENOENT
	}
	stat, err := fs.stat(sp)
	if err != nil {
		return nil, fs.errToStatus("GetAttr", name, err)
	}
	var mode uint32
	if stat.IsDir() {
		mode = fuse.S_IFDIR
	} else {
		mode = fuse.S_IFREG
	}
	return &fuse.Attr{
		Size:  uint64(stat.Size()),
		Mode:  mode | uint32(stat.Mode()),
		Mtime: uint64(stat.ModTime().Unix()),
	}, fuse.OK
}

// OpenDir implements pathfs.FileSystem.
func (fs *fuseFS) OpenDir(name string, _ *fuse.Context) ([]fuse.DirEntry, fuse.Status) {
	sp, ok := fs.path(name)
	if !ok {
		return nil, fuse.ENOENT
	}
	fis, err := fs.renter.FileList(sp, false, true)
	if err != nil {
		return nil, fs.errToStatus("OpenDir", name, err)
	}
	dis, err := fs.renter.DirList(sp)
	if err != nil {
		return nil, fs.errToStatus("OpenDir", name, err)
	}

	entries := make([]fuse.DirEntry, 0, len(fis)+len(dis))
	for _, f := range fis {
		entries = append(entries, fuse.DirEntry{
			Name: path.Base(f.Name()),
			Mode: uint32(f.Mode()) | fuse.S_IFREG,
		})
	}
	for _, d := range dis {
		entries = append(entries, fuse.DirEntry{
			Name: path.Base(d.Name()),
			Mode: uint32(d.Mode()) | fuse.S_IFDIR,
		})
	}
	return entries, fuse.OK
}

// Open implements pathfs.FileSystem.
func (fs *fuseFS) Open(name string, flags uint32, _ *fuse.Context) (file nodefs.File, code fuse.Status) {
	if int(flags&fuse.O_ANYWRITE) != os.O_RDONLY {
		return nil, fuse.EROFS // read-only filesystem
	}
	sp, ok := fs.path(name)
	if !ok {
		return nil, fuse.ENOENT
	}
	stat, err := fs.stat(sp)
	if err != nil {
		return nil, fs.errToStatus("Open", name, err)
	} else if stat.IsDir() {
		return nil, fuse.EISDIR
	}
	_, s, err := fs.renter.Streamer(sp)
	if err != nil {
		return nil, fs.errToStatus("Open", name, err)
	}
	ff := &fuseFile{
		File:   nodefs.NewDefaultFile(),
		path:   sp,
		fs:     fs,
		stream: s,
	}
	// add cache file, if it exists
	if fs.opts.CachePath != "" {
		fs.mu.Lock()
		ci, ok := fs.cacheState[sp]
		fs.mu.Unlock()
		if ok {
			f, err := os.Open(filepath.Join(fs.opts.CachePath, name))
			if err == nil {
				ff.cf = cacheFile{
					f:    f,
					ci:   ci,
					size: stat.Size(),
				}
			}
		}
	}
	return ff, fuse.OK
}

type cacheFile struct {
	f    *os.File
	ci   cacheInfo
	size int64
}

func (cf *cacheFile) Close() error {
	if cf.f == nil {
		return nil
	}
	return cf.f.Close()
}

func (cf *cacheFile) ReadAt(p []byte, off int64) (int, error) {
	if cf.f == nil {
		return 0, errors.New("no cache file")
	}
	const cacheStartSize = 90e6
	startCached := cf.ci.size
	if startCached > cacheStartSize {
		startCached = cacheStartSize
	}
	endCached := cf.ci.size - cacheStartSize
	if endCached < 0 {
		endCached = 0
	}
	endOff := cf.size - endCached
	if off < startCached {
		// start of file
		if spill := off + int64(len(p)) - startCached; spill > 0 {
			p = p[:len(p)-int(spill)]
		}
		return cf.f.ReadAt(p, off)
	} else if off > endOff {
		// end of file
		return cf.f.ReadAt(p, cacheStartSize+(off-endOff))
	}
	return 0, errors.New("data not in cache")
}

// fuseFile implements nodefs.File using a modules.Renter.
type fuseFile struct {
	nodefs.File
	path   modules.SiaPath
	fs     *fuseFS
	stream modules.Streamer
	cf     cacheFile
	mu     sync.Mutex
}

// Read implements nodefs.File.
func (f *fuseFile) Read(p []byte, off int64) (fuse.ReadResult, fuse.Status) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// read from cache if possible
	if n, _ := f.cf.ReadAt(p, off); n > 0 {
		return fuse.ReadResultData(p[:n]), fuse.OK
	}
	// cache miss
	if _, err := f.stream.Seek(off, io.SeekStart); err != nil {
		return nil, f.fs.errToStatus("Read", f.path.String(), err)
	}
	n, err := f.stream.Read(p)
	if err != nil && err != io.EOF {
		return nil, f.fs.errToStatus("Read", f.path.String(), err)
	}
	return fuse.ReadResultData(p[:n]), fuse.OK
}

func (f *fuseFile) Release() {
	if err := f.stream.Close(); err != nil {
		_ = f.fs.errToStatus("Release", f.path.String(), err)
	}
	if err := f.cf.Close(); err != nil {
		_ = f.fs.errToStatus("Release", f.path.String(), err)
	}
}
