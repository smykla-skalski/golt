package revgrep

import (
	"context"
	"os"
	"testing"
)

func TestGitPatch_nonGitDir(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("could not get current working dir: %v", err)
	}

	// Change to non-git dir
	err = os.Chdir(t.TempDir())
	if err != nil {
		t.Fatalf("could not chdir: %v", err)
	}

	t.Cleanup(func() { _ = os.Chdir(wd) })

	patch, newFiles, err := GitPatch(context.Background(), patchOption{})
	if err != nil {
		t.Errorf("error expected nil, got: %v", err)
	}

	if patch != nil {
		t.Errorf("patch expected nil, got: %v", patch)
	}

	if newFiles != nil {
		t.Errorf("newFiles expected nil, got: %v", newFiles)
	}
}
