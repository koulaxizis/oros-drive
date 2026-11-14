//go:build !(linux && !android) && !(windows && (amd64 || arm64))

package app

// The orOS disk as a folder: Linux (FUSE) and Windows (Cloud Files) only.
const mountSupported = false

func (s *Service) syncMount() {}
func (s *Service) unmount()   {}

// eraseMountLocked: nothing is mounted here (SOS-1).
func (s *Service) eraseMountLocked(dir string) {}

func (s *Service) forgetMountLocked(old, now string) error { return nil }
