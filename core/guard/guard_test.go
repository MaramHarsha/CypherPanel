package guard

import (
	"errors"
	"testing"
)

func TestCheckDiskHeadroom(t *testing.T) {
	cases := map[string]struct {
		free    uint64
		statErr error
		minFree uint64
		wantErr bool
	}{
		"plenty of room":        {free: 10 << 30, minFree: 1 << 30, wantErr: false},
		"exactly at minimum":    {free: 1 << 30, minFree: 1 << 30, wantErr: false},
		"below minimum":         {free: 512 << 20, minFree: 1 << 30, wantErr: true},
		"zero free":             {free: 0, minFree: 1, wantErr: true},
		"disabled by zero min":  {free: 0, minFree: 0, wantErr: false},
		"stat failure surfaces": {statErr: errors.New("boom"), minFree: 1 << 30, wantErr: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			stat := func(string) (uint64, error) { return tc.free, tc.statErr }
			err := CheckDiskHeadroom("/data", tc.minFree, stat)
			if (err != nil) != tc.wantErr {
				t.Fatalf("CheckDiskHeadroom(free=%d, min=%d) err = %v, wantErr %v", tc.free, tc.minFree, err, tc.wantErr)
			}
			if tc.statErr != nil && err != nil && !errors.Is(err, tc.statErr) {
				t.Fatalf("stat error not wrapped: %v", err)
			}
		})
	}
}

// TestFreeBytesOnRealFilesystem is a smoke test of the platform stat: the
// working directory's filesystem must report a nonzero size (skipped where
// unsupported).
func TestFreeBytesOnRealFilesystem(t *testing.T) {
	free, err := FreeBytes(".")
	if errors.Is(err, ErrUnsupported) {
		t.Skip("platform without disk statistics")
	}
	if err != nil {
		t.Fatalf("FreeBytes: %v", err)
	}
	if free == 0 {
		t.Fatal("FreeBytes reported 0 free bytes on a live filesystem")
	}
}
