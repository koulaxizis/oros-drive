package app

import "runtime"

// MaxFileMBLimit is the largest "Largest file to upload (MB)" the
// settings take (4 GB).
const MaxFileMBLimit = 4096

// DefaultMaxFileMB is the limit when the person set none: the largest
// allowed (Chris 2026-11-27: «θέλουμε το μέγιστο επιτρεπόμενο ως
// προεπιλογή»). Android keeps 500 MB, because a file is read whole into
// memory to be encrypted, and a phone that runs out of memory would stop
// the app on every pass at the same file.
func DefaultMaxFileMB() int {
	if runtime.GOOS == "android" {
		return 500
	}
	return MaxFileMBLimit
}

// maxFileMB is the limit in use.
func maxFileMB(set int) int {
	if set > 0 {
		return set
	}
	return DefaultMaxFileMB()
}
