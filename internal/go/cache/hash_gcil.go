package cache

import (
	"io"
	"os"
	"sync"
)

func SetSalt(b []byte) {
	hashSalt = b
}

// sharedSalt salts hashes of packages identified by their build ID, which
// already covers everything else that makes their analysis differ.
var sharedSalt []byte

// SetSharedSalt sets the salt for NewSharedHash.
func SetSharedSalt(b []byte) {
	sharedSalt = b
}

// NewSharedHash is NewHash salted with the shared salt, or with the regular
// salt when no shared salt is set.
func NewSharedHash(name string) (*Hash, error) {
	if sharedSalt == nil {
		return NewHash(name)
	}

	return newHash(name, sharedSalt)
}

var copyBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 32*1024)
		return &b
	},
}

// copyFile copies f into w through a pooled buffer.
// Hiding (*os.File).WriteTo makes io.CopyBuffer use the buffer: its generic
// fallback allocates 32 KiB per call, once per hashed source file.
func copyFile(w io.Writer, f *os.File) (int64, error) {
	buf := copyBufPool.Get().(*[]byte)
	defer copyBufPool.Put(buf)

	return io.CopyBuffer(w, fileReader{f}, *buf)
}

type fileReader struct{ f *os.File }

func (r fileReader) Read(p []byte) (int, error) { return r.f.Read(p) }
