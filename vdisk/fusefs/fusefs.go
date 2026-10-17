//go:build linux

// Package fusefs mounts a vdisk.Disk with FUSE (Linux): the orOS disk
// appears as a folder, files download when opened and upload when they
// are closed after a change. Pure Go (go-fuse); it needs the kernel's
// FUSE and, for a normal user, fusermount3 (package fuse3).
package fusefs

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/koulaxizis/oros-drive/vault"
	"github.com/koulaxizis/oros-drive/vdisk"
)

// Mount shows d at dir until the returned server is unmounted.
func Mount(d *vdisk.Disk, dir string) (*fuse.Server, error) {
	sec := time.Second
	root := &node{d: d}
	return fs.Mount(dir, root, &fs.Options{
		EntryTimeout: &sec,
		AttrTimeout:  &sec,
		MountOptions: fuse.MountOptions{
			FsName:      "orOS",
			Name:        "orosdrive",
			DirectMount: true, // as root without fusermount; others fall back to it
		},
		UID: uint32(os.Getuid()),
		GID: uint32(os.Getgid()),
	})
}

type node struct {
	fs.Inode
	d *vdisk.Disk

	mu sync.Mutex
	w  *handle // the open writer, while a write is in progress
}

var (
	_ fs.NodeLookuper  = (*node)(nil)
	_ fs.NodeReaddirer = (*node)(nil)
	_ fs.NodeGetattrer = (*node)(nil)
	_ fs.NodeSetattrer = (*node)(nil)
	_ fs.NodeOpener    = (*node)(nil)
	_ fs.NodeCreater   = (*node)(nil)
	_ fs.NodeMkdirer   = (*node)(nil)
	_ fs.NodeUnlinker  = (*node)(nil)
	_ fs.NodeRmdirer   = (*node)(nil)
	_ fs.NodeRenamer   = (*node)(nil)
	_ fs.NodeStatfser  = (*node)(nil)
)

// diskPath is this node's path on the orOS disk ("/internal/…").
func (n *node) diskPath() string {
	return vdisk.Clean(n.Path(n.Root()))
}

func (n *node) child(name string) string {
	return vdisk.Clean(n.Path(n.Root()) + "/" + name)
}

func errno(err error) syscall.Errno {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, vdisk.ErrNotExist):
		return syscall.ENOENT
	case errors.Is(err, vdisk.ErrExist):
		return syscall.EEXIST
	case errors.Is(err, vdisk.ErrNotEmpty):
		return syscall.ENOTEMPTY
	case errors.Is(err, vdisk.ErrIsDir):
		return syscall.EISDIR
	case errors.Is(err, vdisk.ErrNotDir):
		return syscall.ENOTDIR
	case errors.Is(err, vdisk.ErrTooBig):
		return syscall.EFBIG
	case errors.Is(err, vdisk.ErrBadName):
		return syscall.EINVAL
	case errors.Is(err, vault.ErrLocked), errors.Is(err, vault.ErrNoKey):
		return syscall.EACCES
	}
	return syscall.EIO
}

func fill(a *fuse.Attr, i vdisk.Info) {
	if i.Dir {
		a.Mode = syscall.S_IFDIR | 0o755
		a.Nlink = 2
		return
	}
	a.Mode = syscall.S_IFREG | 0o644
	a.Nlink = 1
	a.Size = uint64(i.Size)
	a.Blocks = (a.Size + 511) / 512
	if !i.MTime.IsZero() {
		a.SetTimes(nil, &i.MTime, &i.MTime)
	}
}

func (n *node) newChild(ctx context.Context, i vdisk.Info) *fs.Inode {
	mode := uint32(syscall.S_IFREG)
	if i.Dir {
		mode = syscall.S_IFDIR
	}
	return n.NewInode(ctx, &node{d: n.d}, fs.StableAttr{Mode: mode})
}

func (n *node) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	p := n.child(name)
	if p == "" {
		return nil, syscall.ENOENT
	}
	i, err := n.d.Stat(p)
	if err != nil {
		return nil, errno(err)
	}
	fill(&out.Attr, i)
	return n.newChild(ctx, i), 0
}

func (n *node) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	list, err := n.d.List(n.diskPath())
	if err != nil {
		return nil, errno(err)
	}
	out := make([]fuse.DirEntry, 0, len(list))
	for _, i := range list {
		mode := uint32(syscall.S_IFREG)
		if i.Dir {
			mode = syscall.S_IFDIR
		}
		out = append(out, fuse.DirEntry{Name: i.Name, Mode: mode})
	}
	return fs.NewListDirStream(out), 0
}

func (n *node) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	n.mu.Lock()
	w := n.w
	n.mu.Unlock()
	if w != nil {
		// A file being written: its size is the scratch copy's.
		if fi, err := w.f.Stat(); err == nil {
			fill(&out.Attr, vdisk.Info{Size: fi.Size(), MTime: fi.ModTime()})
			return 0
		}
	}
	i, err := n.d.Stat(n.diskPath())
	if err != nil {
		return errno(err)
	}
	fill(&out.Attr, i)
	return 0
}

// Setattr: a size change (truncate) is a write; times and modes are
// not kept by the vault and are accepted as they are.
func (n *node) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	if size, ok := in.GetSize(); ok {
		h, _ := f.(*handle)
		if h == nil || !h.writable {
			var e syscall.Errno
			if h, e = n.openWrite(false); e != 0 {
				return e
			}
			defer h.Release(ctx)
		}
		if err := h.f.Truncate(int64(size)); err != nil {
			return syscall.EIO
		}
		h.dirty = true
		if f == nil || f != fs.FileHandle(h) {
			if e := h.Flush(ctx); e != 0 {
				return e
			}
		}
	}
	return n.Getattr(ctx, f, out)
}

func (n *node) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	if flags&(syscall.O_WRONLY|syscall.O_RDWR) != 0 || flags&syscall.O_TRUNC != 0 {
		h, e := n.openWrite(flags&syscall.O_TRUNC != 0)
		if e != 0 {
			return nil, 0, e
		}
		return h, fuse.FOPEN_DIRECT_IO, 0
	}
	local, _, err := n.d.Fetch(n.diskPath())
	if err != nil {
		return nil, 0, errno(err)
	}
	f, err := os.Open(local)
	if err != nil {
		return nil, 0, syscall.EIO
	}
	return &handle{n: n, f: f}, fuse.FOPEN_KEEP_CACHE, 0
}

// openWrite gives a scratch copy to write into: the current content
// (unless truncated), uploaded on flush.
func (n *node) openWrite(trunc bool) (*handle, syscall.Errno) {
	n.mu.Lock()
	if n.w != nil {
		w := n.w
		w.refs++
		n.mu.Unlock()
		return w, 0
	}
	n.mu.Unlock()
	p := n.diskPath()
	i, err := n.d.Stat(p)
	if err != nil {
		return nil, errno(err)
	}
	if i.Dir {
		return nil, syscall.EISDIR
	}
	tmp, err := n.d.TempFile()
	if err != nil {
		return nil, syscall.EIO
	}
	h := &handle{n: n, f: tmp, writable: true, base: i.Hash, refs: 1}
	if !trunc {
		local, _, err := n.d.Fetch(p)
		if err == nil {
			err = copyInto(tmp, local)
		}
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			return nil, errno(err)
		}
	} else {
		h.dirty = true
	}
	n.mu.Lock()
	n.w = h
	n.mu.Unlock()
	return h, 0
}

func copyInto(dst *os.File, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	_, err = io.Copy(dst, in)
	return err
}

func (n *node) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	p := n.child(name)
	if p == "" {
		return nil, nil, 0, syscall.EINVAL
	}
	if _, err := n.d.Stat(p); err == nil {
		return nil, nil, 0, syscall.EEXIST
	}
	tmp, err := n.d.TempFile()
	if err != nil {
		return nil, nil, 0, syscall.EIO
	}
	child := &node{d: n.d}
	in := n.NewInode(ctx, child, fs.StableAttr{Mode: syscall.S_IFREG})
	h := &handle{n: child, f: tmp, writable: true, dirty: true, refs: 1}
	child.w = h
	fill(&out.Attr, vdisk.Info{})
	return in, h, fuse.FOPEN_DIRECT_IO, 0
}

func (n *node) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	p := n.child(name)
	if err := n.d.Mkdir(p); err != nil {
		return nil, errno(err)
	}
	i := vdisk.Info{Path: p, Name: name, Dir: true}
	fill(&out.Attr, i)
	return n.newChild(ctx, i), 0
}

func (n *node) Unlink(ctx context.Context, name string) syscall.Errno {
	p := n.child(name)
	i, err := n.d.Stat(p)
	if err != nil {
		return errno(err)
	}
	if i.Dir {
		return syscall.EISDIR
	}
	return errno(n.d.Remove(p, false))
}

func (n *node) Rmdir(ctx context.Context, name string) syscall.Errno {
	p := n.child(name)
	i, err := n.d.Stat(p)
	if err != nil {
		return errno(err)
	}
	if !i.Dir {
		return syscall.ENOTDIR
	}
	return errno(n.d.Remove(p, false))
}

const renameNoReplace = 1 // RENAME_NOREPLACE

func (n *node) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	from := n.child(name)
	np, ok := newParent.(*node)
	if !ok {
		return syscall.EXDEV
	}
	to := np.child(newName)
	if flags&renameNoReplace != 0 {
		if _, err := n.d.Stat(to); err == nil {
			return syscall.EEXIST
		}
	}
	return errno(n.d.Rename(from, to))
}

func (n *node) Statfs(ctx context.Context, out *fuse.StatfsOut) syscall.Errno {
	// The vault has no fixed size: report plenty of room.
	out.Bsize = 4096
	out.Frsize = 4096
	out.Blocks = 1 << 30
	out.Bfree = 1 << 29
	out.Bavail = 1 << 29
	out.NameLen = 255
	return 0
}

// handle is one open file: a read-only cache file, or a scratch copy
// being written (shared by every writer of the node).
type handle struct {
	n        *node
	f        *os.File
	writable bool

	mu    sync.Mutex
	dirty bool
	base  string // hash the write started from
	refs  int
}

var (
	_ fs.FileReader   = (*handle)(nil)
	_ fs.FileWriter   = (*handle)(nil)
	_ fs.FileFlusher  = (*handle)(nil)
	_ fs.FileFsyncer  = (*handle)(nil)
	_ fs.FileReleaser = (*handle)(nil)
)

func (h *handle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	h.mu.Lock()
	defer h.mu.Unlock()
	n, err := h.f.ReadAt(dest, off)
	if err != nil && err != io.EOF {
		return nil, syscall.EIO
	}
	return fuse.ReadResultData(dest[:n]), 0
}

func (h *handle) Write(ctx context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	if !h.writable {
		return 0, syscall.EBADF
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	n, err := h.f.WriteAt(data, off)
	if err != nil {
		return uint32(n), syscall.EIO
	}
	h.dirty = true
	return uint32(n), 0
}

// Flush runs on every close(2): a changed file is uploaded then, so a
// program that closed it sees an error if the upload failed.
func (h *handle) Flush(ctx context.Context) syscall.Errno {
	if !h.writable {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.dirty {
		return 0
	}
	if err := h.f.Sync(); err != nil {
		return syscall.EIO
	}
	i, err := h.n.d.Put(h.n.diskPath(), h.f.Name(), h.base)
	if err != nil {
		return errno(err)
	}
	h.base, h.dirty = i.Hash, false
	return 0
}

func (h *handle) Fsync(ctx context.Context, flags uint32) syscall.Errno { return h.Flush(ctx) }

func (h *handle) Release(ctx context.Context) syscall.Errno {
	if !h.writable {
		h.f.Close()
		return 0
	}
	e := h.Flush(ctx) // a dirty file is never dropped
	h.n.mu.Lock()
	h.refs--
	last := h.refs <= 0
	if last && h.n.w == h {
		h.n.w = nil
	}
	h.n.mu.Unlock()
	if last {
		h.f.Close()
		os.Remove(h.f.Name())
	}
	return e
}
