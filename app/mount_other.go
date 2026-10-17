//go:build !linux || android

package app

// The orOS disk as a folder: Linux only for now (Windows: wave 6).
const mountSupported = false

func (s *Service) syncMount() {}
func (s *Service) unmount()   {}
