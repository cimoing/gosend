package transfer

import (
	"testing"

	"gosend/internal/localsend"
)

func FuzzSafeFileName(f *testing.F) {
	for _, seed := range []string{"file.txt", "../escape", `C:\outside`, "", "合法文件.pdf"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, name string) {
		_ = safeFileName(name)
	})
}

func FuzzValidatePrepare(f *testing.F) {
	f.Add("id", "file.txt", int64(10))
	f.Fuzz(func(t *testing.T, id, name string, size int64) {
		_ = validatePrepare(localsend.PrepareUploadRequest{
			Info: localsend.DeviceInfo{Alias: "Peer", Fingerprint: "fingerprint"},
			Files: map[string]localsend.FileInfo{
				id: {ID: id, FileName: name, Size: size},
			},
		})
	})
}
