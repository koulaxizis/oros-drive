//go:build windows && (amd64 || arm64)

package cloudfiles

// The Windows side: the folder is registered as a Cloud Files sync
// root (cldapi.dll, Windows 10 1709 and later), every file of the disk
// is a placeholder, and Windows calls back when a program reads a
// placeholder (FETCH_DATA: the engine downloads it), deletes or renames
// one (the change goes to orOS before Windows carries it out). Pure Go:
// the structures below follow cfapi.h for 64-bit Windows.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/koulaxizis/oros-drive/mirror"
	"github.com/koulaxizis/oros-drive/vault"
	"github.com/koulaxizis/oros-drive/vdisk"
)

// Supported reports whether this Windows has the Cloud Files API.
func Supported() bool { return procRegister.Find() == nil }

const providerName = "orOS Drive"

// The provider's id; the same for every install.
var providerID = windows.GUID{Data1: 0x6f0b3c43, Data2: 0x0e4a, Data3: 0x4b0a, Data4: [8]byte{0x9a, 0x57, 0x6f, 0x72, 0x4f, 0x53, 0x44, 0x72}}

var (
	cldapi         = windows.NewLazySystemDLL("cldapi.dll")
	procRegister   = cldapi.NewProc("CfRegisterSyncRoot")
	procUnregister = cldapi.NewProc("CfUnregisterSyncRoot")
	procConnect    = cldapi.NewProc("CfConnectSyncRoot")
	procDisconnect = cldapi.NewProc("CfDisconnectSyncRoot")
	procCreate     = cldapi.NewProc("CfCreatePlaceholders")
	procExecute    = cldapi.NewProc("CfExecute")
	procConvert    = cldapi.NewProc("CfConvertToPlaceholder")
	procUpdate     = cldapi.NewProc("CfUpdatePlaceholder")
	procRevert     = cldapi.NewProc("CfRevertPlaceholder")
	procInfo       = cldapi.NewProc("CfGetPlaceholderInfo")
	procStateOf    = cldapi.NewProc("CfGetPlaceholderStateFromAttributeTag")
	procRootInfo   = cldapi.NewProc("CfGetSyncRootInfoByPath")
)

// cfapi.h values used here.
const (
	cbFetchData       = 0
	cbCancelFetchData = 2
	cbNotifyDelete    = 9
	cbNotifyRename    = 11
	cbNone            = -1

	opTransferData = 0
	opAckDelete    = 6
	opAckRename    = 7

	connectFullPath = 4

	hydrationFull            = 2
	populationAlwaysFull     = 3
	insyncTrackFileLastWrite = 0x100

	registerUpdate         = 1
	registerNoPopulateRoot = 2
	registerMarkInSyncRoot = 4

	createMarkInSync     = 2
	createNoPopulate     = 1
	convertMarkInSync    = 1
	updateMarkInSync     = 2
	updateDehydrate      = 4
	updateVerifyInSync   = 1
	placeholderInfoBasic = 0
	syncRootInfoProvider = 2

	statePlaceholder = 0x1
	stateInSync      = 0x8
	statePartial     = 0x10
	stateInvalid     = 0xFFFFFFFF

	renameSourceIn = 2
	renameTargetIn = 4
	deleteUndelete = 2

	statusUnsuccessful = -0x3FFFFFFF // 0xC0000001

	volumeNameNone = 0x4
)

type cfCallbackInfo struct {
	StructSize             uint32
	ConnectionKey          int64
	CallbackContext        uintptr
	VolumeGuidName         *uint16
	VolumeDosName          *uint16
	VolumeSerialNumber     uint32
	SyncRootFileID         int64
	SyncRootIdentity       *byte
	SyncRootIdentityLength uint32
	FileID                 int64
	FileSize               int64
	FileIdentity           *byte
	FileIdentityLength     uint32
	NormalizedPath         *uint16
	TransferKey            int64
	PriorityHint           uint8
	CorrelationVector      *byte
	ProcessInfo            *byte
	RequestKey             int64
}

// CF_CALLBACK_PARAMETERS: ParamSize, then the union at offset 8.
type fetchParams struct {
	ParamSize          uint32
	_                  uint32
	Flags              int32
	_                  int32
	RequiredFileOffset int64
	RequiredLength     int64
}

type deleteParams struct {
	ParamSize uint32
	_         uint32
	Flags     int32
}

type renameParams struct {
	ParamSize  uint32
	_          uint32
	Flags      int32
	_          int32
	TargetPath *uint16
}

type cfOperationInfo struct {
	StructSize        uint32
	Type              int32
	ConnectionKey     int64
	TransferKey       int64
	CorrelationVector *byte
	SyncStatus        *byte
	RequestKey        int64
}

// CF_OPERATION_PARAMETERS is 48 bytes; ParamSize names the member.
type transferParams struct {
	ParamSize        uint32
	_                uint32
	Flags            int32
	CompletionStatus int32
	Buffer           *byte
	Offset           int64
	Length           int64
	_                [8]byte
}

type ackParams struct {
	ParamSize        uint32
	_                uint32
	Flags            int32
	CompletionStatus int32
	_                [32]byte
}

type fileBasicInfo struct {
	CreationTime, LastAccessTime, LastWriteTime, ChangeTime int64
	FileAttributes                                          uint32
}

type cfFsMetadata struct {
	BasicInfo fileBasicInfo
	FileSize  int64
}

type cfPlaceholderCreateInfo struct {
	RelativeFileName   *uint16
	FsMetadata         cfFsMetadata
	FileIdentity       *byte
	FileIdentityLength uint32
	Flags              int32
	Result             int32
	CreateUsn          int64
}

type cfSyncRegistration struct {
	StructSize             uint32
	ProviderName           *uint16
	ProviderVersion        *uint16
	SyncRootIdentity       *byte
	SyncRootIdentityLength uint32
	FileIdentity           *byte
	FileIdentityLength     uint32
	ProviderID             windows.GUID
}

type cfSyncPolicies struct {
	StructSize            uint32
	HydrationPrimary      uint16
	HydrationModifier     uint16
	PopulationPrimary     uint16
	PopulationModifier    uint16
	InSync                uint32
	HardLink              int32
	PlaceholderManagement int32
}

type cfCallbackRegistration struct {
	Type     int32
	Callback uintptr
}

func hrErr(r uintptr) error {
	h := uint32(r)
	if int32(h) >= 0 {
		return nil
	}
	if h&0xFFFF0000 == 0x80070000 {
		return busy(windows.Errno(h & 0xFFFF))
	}
	return fmt.Errorf("cloud files error 0x%08X", h)
}

func busy(err error) error {
	if errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return fmt.Errorf("%w (%v)", ErrBusy, err)
	}
	return err
}

// long gives a path the \\?\ form, for paths past 260 characters.
func long(p string) string {
	if strings.HasPrefix(p, `\\`) {
		return p
	}
	return `\\?\` + p
}

func utf16(s string) *uint16 {
	p, err := windows.UTF16PtrFromString(s)
	if err != nil {
		p, _ = windows.UTF16PtrFromString("")
	}
	return p
}

func open(p string, access, share uint32) (windows.Handle, error) {
	h, err := windows.CreateFile(utf16(long(p)), access, share, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return 0, &os.PathError{Op: "open", Path: p, Err: busy(err)}
	}
	return h, nil
}

func placeholderState(attrs, tag uint32) uint32 {
	r, _, _ := procStateOf.Call(uintptr(attrs), uintptr(tag))
	return uint32(r)
}

func handleState(h windows.Handle) (attrs, state uint32, err error) {
	var info struct{ Attrs, Tag uint32 }
	if err := windows.GetFileInformationByHandleEx(h, windows.FileAttributeTagInfo, (*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return 0, 0, err
	}
	return info.Attrs, placeholderState(info.Attrs, info.Tag), nil
}

func filetime(t time.Time) int64 {
	if t.IsZero() {
		t = time.Now()
	}
	return t.UnixNano()/100 + 116444736000000000
}

func metadata(i vdisk.Info, attrs uint32) cfFsMetadata {
	ft := filetime(i.MTime)
	return cfFsMetadata{BasicInfo: fileBasicInfo{CreationTime: ft, LastAccessTime: ft, LastWriteTime: ft, ChangeTime: ft, FileAttributes: attrs}, FileSize: i.Size}
}

// ---------- the folder (Local) ----------

type winLocal struct {
	root  string
	trash mirror.Trash
	quiet sync.Map // rel → true: our own deletions, not to be sent to orOS
}

func (w *winLocal) abs(rel string) string { return filepath.Join(w.root, filepath.FromSlash(rel)) }

func (w *winLocal) Scan() (map[string]Entry, error) {
	if fi, err := os.Stat(w.root); err != nil || !fi.IsDir() {
		if err == nil {
			err = errors.New("not a folder")
		}
		return nil, err
	}
	out := map[string]Entry{}
	var walk func(dir, prefix string) error
	walk = func(dir, prefix string) error {
		var fd windows.Win32finddata
		h, err := windows.FindFirstFile(utf16(long(dir)+`\*`), &fd)
		if err != nil {
			if dir == w.root {
				return err
			}
			return nil // gone meanwhile
		}
		defer windows.FindClose(h)
		for {
			name := windows.UTF16ToString(fd.FileName[:])
			if name != "." && name != ".." {
				var tag uint32
				if fd.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
					tag = fd.Reserved0
				}
				st := placeholderState(fd.FileAttributes, tag)
				ph := st != stateInvalid && st&statePlaceholder != 0
				if tag == 0 || ph { // symlinks and junctions are left alone
					rel := prefix + name
					isDir := fd.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
					out[rel] = Entry{
						Dir:         isDir,
						Placeholder: ph,
						InSync:      ph && st&stateInSync != 0,
						Size:        int64(fd.FileSizeHigh)<<32 | int64(fd.FileSizeLow),
						MTime:       time.Unix(0, fd.LastWriteTime.Nanoseconds()),
					}
					if isDir {
						if err := walk(filepath.Join(dir, name), rel+"/"); err != nil {
							return err
						}
					}
				}
			}
			if err := windows.FindNextFile(h, &fd); err != nil {
				if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
					return nil
				}
				return err
			}
		}
	}
	return out, walk(w.root, "")
}

func (w *winLocal) Identity(rel string) (string, error) {
	h, err := open(w.abs(rel), windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	buf := make([]byte, 28+4096)
	var n uint32
	if err := hrErr(callProc(procInfo, uintptr(h), placeholderInfoBasic, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&n)))); err != nil {
		return "", err
	}
	l := *(*uint32)(unsafe.Pointer(&buf[24]))
	if int(l) > len(buf)-28 {
		return "", errors.New("identity too long")
	}
	return string(buf[28 : 28+l]), nil
}

// callProc calls into cldapi.dll; like LazyProc.Call it keeps memory
// passed as uintptr alive for the call.
//
//go:uintptrescapes
func callProc(p *windows.LazyProc, args ...uintptr) uintptr {
	r, _, _ := p.Call(args...)
	return r
}

func (w *winLocal) create(rel string, meta cfFsMetadata, id string, flags int32) error {
	abs := w.abs(rel)
	idb := []byte(id)
	info := cfPlaceholderCreateInfo{
		RelativeFileName:   utf16(filepath.Base(abs)),
		FsMetadata:         meta,
		FileIdentity:       &idb[0],
		FileIdentityLength: uint32(len(idb)),
		Flags:              flags,
	}
	var done uint32
	err := hrErr(callProc(procCreate, uintptr(unsafe.Pointer(utf16(long(filepath.Dir(abs))))), uintptr(unsafe.Pointer(&info)), 1, 0, uintptr(unsafe.Pointer(&done))))
	if err == nil {
		err = hrErr(uintptr(uint32(info.Result)))
	}
	if err != nil {
		return &os.PathError{Op: "create placeholder", Path: abs, Err: err}
	}
	return nil
}

func (w *winLocal) CreateFile(rel string, i vdisk.Info) error {
	return w.create(rel, metadata(i, windows.FILE_ATTRIBUTE_NORMAL), i.Hash, createMarkInSync)
}

func (w *winLocal) CreateDir(rel string) error {
	return w.create(rel, metadata(vdisk.Info{}, windows.FILE_ATTRIBUTE_DIRECTORY), "/", createMarkInSync|createNoPopulate)
}

func (w *winLocal) Update(rel string, i vdisk.Info) error {
	h, err := open(w.abs(rel), windows.GENERIC_READ|windows.GENERIC_WRITE, 0) // exclusive: dehydrating needs it
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	_, st, err := handleState(h)
	if err != nil {
		return err
	}
	if st == stateInvalid || st&stateInSync == 0 {
		return ErrChanged
	}
	meta := metadata(i, windows.FILE_ATTRIBUTE_NORMAL)
	idb := []byte(i.Hash)
	return hrErr(callProc(procUpdate, uintptr(h), uintptr(unsafe.Pointer(&meta)), uintptr(unsafe.Pointer(&idb[0])), uintptr(len(idb)),
		0, 0, updateVerifyInSync|updateDehydrate|updateMarkInSync, 0, 0))
}

func (w *winLocal) Snapshot(rel, tmp string) error {
	h, err := open(w.abs(rel), windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE)
	if err != nil {
		return err
	}
	src := os.NewFile(uintptr(h), w.abs(rel))
	defer src.Close()
	dst, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, err = io.Copy(dst, src)
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	return err
}

func (w *winLocal) MarkInSync(rel, hash string) error {
	h, err := open(w.abs(rel), windows.GENERIC_READ|windows.GENERIC_WRITE, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(h), w.abs(rel))
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	if vault.SHA256Hex(b) != hash {
		return ErrChanged
	}
	_, st, err := handleState(h)
	if err != nil {
		return err
	}
	idb := []byte(hash)
	if st != stateInvalid && st&statePlaceholder != 0 {
		return hrErr(callProc(procUpdate, uintptr(h), 0, uintptr(unsafe.Pointer(&idb[0])), uintptr(len(idb)), 0, 0, updateMarkInSync, 0, 0))
	}
	return hrErr(callProc(procConvert, uintptr(h), uintptr(unsafe.Pointer(&idb[0])), uintptr(len(idb)), convertMarkInSync, 0, 0))
}

func (w *winLocal) MarkDirInSync(rel string) error {
	h, err := open(w.abs(rel), windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	idb := []byte("/")
	return hrErr(callProc(procConvert, uintptr(h), uintptr(unsafe.Pointer(&idb[0])), 1, convertMarkInSync, 0, 0))
}

func (w *winLocal) Remove(rel string, dir bool) error {
	abs := w.abs(rel)
	if dir {
		w.quiet.Store(rel, true)
		defer w.quiet.Delete(rel)
		return os.Remove(abs)
	}
	h, err := open(abs, windows.GENERIC_READ|windows.DELETE, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(h), abs)
	defer f.Close()
	attrs, st, err := handleState(h)
	if err != nil {
		return err
	}
	if st == stateInvalid || st&stateInSync == 0 {
		return ErrChanged
	}
	dehydrated := st&statePartial != 0 || attrs&(windows.FILE_ATTRIBUTE_OFFLINE|windows.FILE_ATTRIBUTE_RECALL_ON_DATA_ACCESS) != 0
	if !dehydrated && w.trash != nil {
		// Downloaded content goes to the trash first.
		tmp, err := os.CreateTemp("", "orosdrive-trash-*")
		if err != nil {
			return err
		}
		_, err = io.Copy(tmp, f)
		tmp.Close()
		if err == nil {
			err = w.trash.Move(tmp.Name(), "orOS", rel)
		}
		if err != nil {
			os.Remove(tmp.Name())
			return err
		}
	}
	w.quiet.Store(rel, true)
	defer w.quiet.Delete(rel)
	del := byte(1)
	return windows.SetFileInformationByHandle(h, windows.FileDispositionInfo, &del, 1)
}

// ---------- registration ----------

// Registered reports whether dir is an orOS sync root already; err when
// it is inside another provider's (OneDrive's) folder.
func Registered(dir string) (bool, error) {
	if !Supported() {
		return false, errors.New("this Windows has no Cloud Files API")
	}
	var info struct {
		Status  uint32
		Name    [256]uint16
		Version [256]uint16
	}
	var n uint32
	r := callProc(procRootInfo, uintptr(unsafe.Pointer(utf16(long(dir)))), syncRootInfoProvider, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info), uintptr(unsafe.Pointer(&n)))
	if hrErr(r) != nil {
		return false, nil
	}
	if name := windows.UTF16ToString(info.Name[:]); name != providerName {
		return false, fmt.Errorf("the folder is inside %s's folder", name)
	}
	return true, nil
}

func register(dir string, update bool) error {
	name, version := utf16(providerName), utf16("1")
	ident, fid := []byte("orOS"), []byte("/")
	reg := cfSyncRegistration{
		StructSize:             uint32(unsafe.Sizeof(cfSyncRegistration{})),
		ProviderName:           name,
		ProviderVersion:        version,
		SyncRootIdentity:       &ident[0],
		SyncRootIdentityLength: uint32(len(ident)),
		FileIdentity:           &fid[0],
		FileIdentityLength:     uint32(len(fid)),
		ProviderID:             providerID,
	}
	pol := cfSyncPolicies{
		StructSize:        uint32(unsafe.Sizeof(cfSyncPolicies{})),
		HydrationPrimary:  hydrationFull,
		PopulationPrimary: populationAlwaysFull,
		InSync:            insyncTrackFileLastWrite,
	}
	flags := uintptr(registerNoPopulateRoot | registerMarkInSyncRoot)
	if update {
		flags |= registerUpdate
	}
	return hrErr(callProc(procRegister, uintptr(unsafe.Pointer(utf16(long(dir)))), uintptr(unsafe.Pointer(&reg)), uintptr(unsafe.Pointer(&pol)), flags))
}

// Unregister ends the folder's life as the orOS disk (the setting was
// turned off; call it after Unmount): downloaded files stay as normal
// files, files never downloaded are removed (they are in orOS), and
// Windows forgets the folder.
func Unregister(dir string, statePath string) error {
	if ok, err := Registered(dir); err != nil || !ok {
		return err
	}
	w := &winLocal{root: dir}
	ents, err := w.Scan()
	if err != nil {
		return err
	}
	rels := make([]string, 0, len(ents))
	for rel := range ents {
		rels = append(rels, rel)
	}
	// Deepest first, so folders are empty when their turn comes.
	sortDeepFirst(rels)
	for _, rel := range rels {
		e := ents[rel]
		if !e.Placeholder {
			continue
		}
		abs := w.abs(rel)
		if !e.Dir {
			if h, err := open(abs, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE); err == nil {
				attrs, st, _ := handleState(h)
				windows.CloseHandle(h)
				if st&statePartial != 0 || attrs&(windows.FILE_ATTRIBUTE_OFFLINE|windows.FILE_ATTRIBUTE_RECALL_ON_DATA_ACCESS) != 0 {
					os.Remove(abs)
					continue
				}
			}
		}
		if h, err := open(abs, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE); err == nil {
			callProc(procRevert, uintptr(h), 0, 0)
			windows.CloseHandle(h)
		}
		if e.Dir {
			os.Remove(abs) // only when empty
		}
	}
	if statePath != "" {
		os.Remove(statePath)
	}
	return hrErr(callProc(procUnregister, uintptr(unsafe.Pointer(utf16(long(dir))))))
}

func sortDeepFirst(rels []string) {
	for i := 1; i < len(rels); i++ {
		for j := i; j > 0 && less(rels[j], rels[j-1]); j-- {
			rels[j], rels[j-1] = rels[j-1], rels[j]
		}
	}
}

func less(a, b string) bool {
	da, db := strings.Count(a, "/"), strings.Count(b, "/")
	if da != db {
		return da > db
	}
	return a > b
}

// ---------- the connection and callbacks ----------

// Options for Mount.
type Options struct {
	StatePath string        // the engine's state (see Engine.StatePath)
	TrashDir  string        // where downloaded files removed for orOS go
	Interval  time.Duration // passes; 0 = 5 s
	Settle    time.Duration // Engine.Settle
	Logf      func(kind, rel, detail string)
}

// Server is a connected folder.
type Server struct {
	Root string
	e    *Engine
	w    *winLocal
	norm string // the root relative to its volume, as callbacks name it
	key  int64
	id   uintptr

	passMu   sync.Mutex
	mu       sync.Mutex
	last     Report
	lastErr  error
	kick     chan struct{}
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

var (
	cbOnce    sync.Once
	cbTable   []cfCallbackRegistration
	serversMu sync.Mutex
	servers   = map[uintptr]*Server{}
	nextID    uintptr
)

func callbacks() []cfCallbackRegistration {
	cbOnce.Do(func() {
		cbTable = []cfCallbackRegistration{
			{cbFetchData, syscall.NewCallback(onFetch)},
			{cbCancelFetchData, syscall.NewCallback(onCancel)},
			{cbNotifyDelete, syscall.NewCallback(onDelete)},
			{cbNotifyRename, syscall.NewCallback(onRename)},
			{cbNone, 0},
		}
	})
	return cbTable
}

func serverOf(info *cfCallbackInfo) *Server {
	serversMu.Lock()
	defer serversMu.Unlock()
	return servers[info.CallbackContext]
}

// Mount registers dir (empty, or an orOS sync root already) and
// connects it to d until Unmount.
func Mount(d *vdisk.Disk, dir string, opt Options) (*Server, error) {
	if !Supported() {
		return nil, errors.New("this Windows has no Cloud Files API (Windows 10 1709 or later)")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	ours, err := Registered(dir)
	if err != nil {
		return nil, err
	}
	if !ours {
		if ents, err := os.ReadDir(dir); err != nil || len(ents) > 0 {
			if err == nil {
				err = errors.New("the folder must be empty")
			}
			return nil, err
		}
	}
	if err := register(dir, ours); err != nil {
		return nil, fmt.Errorf("register the folder: %w", err)
	}
	norm, err := volumeRelative(dir)
	if err != nil {
		return nil, err
	}
	w := &winLocal{root: dir}
	if opt.TrashDir != "" {
		w.trash = &mirror.DirTrash{Root: opt.TrashDir}
	}
	s := &Server{
		Root: dir, w: w, norm: norm,
		e:    &Engine{Disk: d, Local: w, StatePath: opt.StatePath, Settle: opt.Settle, Logf: opt.Logf},
		kick: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}),
	}
	serversMu.Lock()
	nextID++
	s.id = nextID
	servers[s.id] = s
	serversMu.Unlock()
	table := callbacks()
	if err := hrErr(callProc(procConnect, uintptr(unsafe.Pointer(utf16(long(dir)))), uintptr(unsafe.Pointer(&table[0])), s.id, connectFullPath, uintptr(unsafe.Pointer(&s.key)))); err != nil {
		serversMu.Lock()
		delete(servers, s.id)
		serversMu.Unlock()
		return nil, fmt.Errorf("connect the folder: %w", err)
	}
	interval := opt.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	go s.loop(interval)
	return s, nil
}

func volumeRelative(dir string) (string, error) {
	h, err := open(dir, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, 1024)
	n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), volumeNameNone)
	if err != nil {
		return "", err
	}
	if int(n) > len(buf) {
		buf = make([]uint16, n)
		if n, err = windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), volumeNameNone); err != nil {
			return "", err
		}
	}
	return strings.TrimRight(windows.UTF16ToString(buf[:n]), `\`), nil
}

func (s *Server) loop(interval time.Duration) {
	defer close(s.done)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		s.Sync()
		select {
		case <-s.stop:
			return
		case <-t.C:
		case <-s.kick:
		}
	}
}

// Sync runs one pass now (never two at once).
func (s *Server) Sync() (Report, error) {
	s.passMu.Lock()
	defer s.passMu.Unlock()
	rep, err := s.e.Pass()
	s.mu.Lock()
	s.last, s.lastErr = rep, err
	s.mu.Unlock()
	return rep, err
}

// Status is the last pass's report.
func (s *Server) Status() (Report, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last, s.lastErr
}

func (s *Server) poke() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// Unmount disconnects the folder. Its placeholders stay; files not
// downloaded open again once it is connected again.
func (s *Server) Unmount() error {
	s.stopOnce.Do(func() {
		close(s.stop)
		select {
		case <-s.done:
		case <-time.After(10 * time.Second): // a long upload finishes on its own
		}
		callProc(procDisconnect, uintptr(s.key))
		serversMu.Lock()
		delete(servers, s.id)
		serversMu.Unlock()
		s.e.Save()
	})
	return nil
}

// rel turns a path a callback names into a path in the folder.
func (s *Server) rel(p *uint16) (string, bool) {
	if p == nil {
		return "", false
	}
	full := strings.TrimRight(windows.UTF16PtrToString(p), `\`)
	if strings.EqualFold(full, s.norm) {
		return "", true
	}
	if len(full) <= len(s.norm)+1 || !strings.EqualFold(full[:len(s.norm)+1], s.norm+`\`) {
		return "", false
	}
	return filepath.ToSlash(full[len(s.norm)+1:]), true
}

func (s *Server) execute(op int32, key, transfer, request int64, params unsafe.Pointer) error {
	info := cfOperationInfo{StructSize: uint32(unsafe.Sizeof(cfOperationInfo{})), Type: op, ConnectionKey: key, TransferKey: transfer, RequestKey: request}
	return hrErr(callProc(procExecute, uintptr(unsafe.Pointer(&info)), uintptr(params)))
}

func onFetch(info *cfCallbackInfo, p *fetchParams) uintptr {
	s := serverOf(info)
	if s == nil {
		return 0
	}
	id := ""
	if info.FileIdentity != nil && info.FileIdentityLength > 0 {
		id = string(unsafe.Slice(info.FileIdentity, info.FileIdentityLength))
	}
	key, transfer, request, size := info.ConnectionKey, info.TransferKey, info.RequestKey, info.FileSize
	off, length := p.RequiredFileOffset, p.RequiredLength
	go s.fill(id, key, transfer, request, size, off, length)
	return 0
}

// fill hands Windows a placeholder's content, from the required offset
// to the end, in aligned blocks.
func (s *Server) fill(id string, key, transfer, request, size, off, length int64) {
	failed := func(err error) {
		if s.e.Logf != nil {
			s.e.Logf("error", id, "download: "+err.Error())
		}
		tp := transferParams{ParamSize: 40, CompletionStatus: statusUnsuccessful, Offset: off, Length: length}
		s.execute(opTransferData, key, transfer, request, unsafe.Pointer(&tp))
	}
	local, err := s.e.Fetch(id)
	if err != nil {
		failed(err)
		return
	}
	f, err := os.Open(local)
	if err != nil {
		failed(err)
		return
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || fi.Size() != size {
		failed(fmt.Errorf("content is not the size listed"))
		return
	}
	const block = 1 << 20
	buf := make([]byte, block)
	for pos := off &^ 4095; pos < size; {
		n := int64(block)
		if size-pos < n {
			n = size - pos
		}
		if _, err := f.ReadAt(buf[:n], pos); err != nil && err != io.EOF {
			failed(err)
			return
		}
		tp := transferParams{ParamSize: 40, Buffer: &buf[0], Offset: pos, Length: n}
		if err := s.execute(opTransferData, key, transfer, request, unsafe.Pointer(&tp)); err != nil {
			return // cancelled by Windows (the program gave up)
		}
		pos += n
	}
}

func onCancel(info *cfCallbackInfo, p uintptr) uintptr { return 0 }

func onDelete(info *cfCallbackInfo, p *deleteParams) uintptr {
	s := serverOf(info)
	if s == nil {
		return 0
	}
	rel, in := s.rel(info.NormalizedPath)
	key, transfer, request := info.ConnectionKey, info.TransferKey, info.RequestKey
	undelete := p.Flags&deleteUndelete != 0
	go func() {
		var err error
		if _, ours := s.w.quiet.Load(rel); in && rel != "" && !undelete && !ours {
			err = s.e.OnDelete(rel)
		}
		ap := ackParams{ParamSize: 16}
		if err != nil {
			ap.CompletionStatus = statusUnsuccessful
			if s.e.Logf != nil {
				s.e.Logf("error", rel, "delete: "+err.Error())
			}
		}
		s.execute(opAckDelete, key, transfer, request, unsafe.Pointer(&ap))
		s.poke()
	}()
	return 0
}

func onRename(info *cfCallbackInfo, p *renameParams) uintptr {
	s := serverOf(info)
	if s == nil {
		return 0
	}
	from, fromIn := s.rel(info.NormalizedPath)
	to, toIn := s.rel(p.TargetPath)
	fromIn = fromIn && p.Flags&renameSourceIn != 0 && from != ""
	toIn = toIn && p.Flags&renameTargetIn != 0 && to != ""
	key, transfer, request := info.ConnectionKey, info.TransferKey, info.RequestKey
	go func() {
		err := s.e.OnRename(from, to, fromIn, toIn)
		ap := ackParams{ParamSize: 16}
		if err != nil {
			ap.CompletionStatus = statusUnsuccessful
			if s.e.Logf != nil {
				s.e.Logf("error", from, "rename: "+err.Error())
			}
		}
		s.execute(opAckRename, key, transfer, request, unsafe.Pointer(&ap))
		s.poke()
	}()
	return 0
}

// The layouts cfapi.h has on 64-bit Windows; the build fails on any
// difference.
var (
	_ = [1]int{}[unsafe.Sizeof(cfCallbackInfo{})-152]
	_ = [1]int{}[unsafe.Offsetof(cfCallbackInfo{}.FileIdentity)-88]
	_ = [1]int{}[unsafe.Offsetof(cfCallbackInfo{}.NormalizedPath)-104]
	_ = [1]int{}[unsafe.Offsetof(cfCallbackInfo{}.RequestKey)-144]
	_ = [1]int{}[unsafe.Offsetof(fetchParams{}.RequiredFileOffset)-16]
	_ = [1]int{}[unsafe.Offsetof(renameParams{}.TargetPath)-16]
	_ = [1]int{}[unsafe.Sizeof(cfOperationInfo{})-48]
	_ = [1]int{}[unsafe.Sizeof(transferParams{})-48]
	_ = [1]int{}[unsafe.Offsetof(transferParams{}.Length)-32]
	_ = [1]int{}[unsafe.Sizeof(ackParams{})-48]
	_ = [1]int{}[unsafe.Sizeof(cfFsMetadata{})-48]
	_ = [1]int{}[unsafe.Sizeof(cfPlaceholderCreateInfo{})-88]
	_ = [1]int{}[unsafe.Offsetof(cfPlaceholderCreateInfo{}.Result)-72]
	_ = [1]int{}[unsafe.Sizeof(cfSyncRegistration{})-72]
	_ = [1]int{}[unsafe.Offsetof(cfSyncRegistration{}.ProviderID)-52]
	_ = [1]int{}[unsafe.Sizeof(cfSyncPolicies{})-24]
	_ = [1]int{}[unsafe.Sizeof(cfCallbackRegistration{})-16]
)
