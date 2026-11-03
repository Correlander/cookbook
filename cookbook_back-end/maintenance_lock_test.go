package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestMaintenanceLockSharedBlocksExclusiveAndLeavesStableFile(t *testing.T) {
	database := filepath.Join(t.TempDir(), "cookbook.db")
	shared, err := acquireMaintenanceLock(database, maintenanceLockShared)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireMaintenanceLock(database, maintenanceLockExclusive); err != ErrMaintenanceLockHeld {
		t.Fatalf("exclusive lock error=%v, want ErrMaintenanceLockHeld", err)
	}
	lockPath, err := maintenanceLockPath(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := shared.Close(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(lockPath); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("lock file mode=%v err=%v, want persistent mode 0600 file", info, err)
	}
	exclusive, err := acquireMaintenanceLock(database, maintenanceLockExclusive)
	if err != nil {
		t.Fatalf("exclusive lock after shared release: %v", err)
	}
	if err := exclusive.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("lock file should persist after unlock: %v", err)
	}
}

func TestMaintenanceLockSharedAndExclusiveRaceHasOneWinner(t *testing.T) {
	database := filepath.Join(t.TempDir(), "cookbook.db")
	start := make(chan struct{})
	type result struct {
		mode maintenanceLockMode
		lock *maintenanceLock
		err  error
	}
	results := make(chan result, 2)
	for _, mode := range []maintenanceLockMode{maintenanceLockShared, maintenanceLockExclusive} {
		go func(mode maintenanceLockMode) {
			<-start
			lock, err := acquireMaintenanceLock(database, mode)
			results <- result{mode: mode, lock: lock, err: err}
		}(mode)
	}
	close(start)
	first := <-results
	if first.mode == maintenanceLockExclusive && first.err == nil {
		select {
		case second := <-results:
			if second.err == nil {
				_ = first.lock.Close()
				_ = second.lock.Close()
				t.Fatal("shared server lock overlapped an exclusive CLI lock")
			}
			t.Fatalf("shared attempt failed while CLI lock held: %+v", second)
		case <-time.After(20 * time.Millisecond):
		}
		if err := first.lock.Close(); err != nil {
			t.Fatal(err)
		}
		first.lock = nil
		second := <-results
		if second.err != nil || second.mode != maintenanceLockShared {
			t.Fatalf("server lock after CLI release = %+v", second)
		}
		defer second.lock.Close()
		return
	}
	second := <-results
	if first.mode == maintenanceLockShared && first.err == nil {
		defer first.lock.Close()
		if second.mode != maintenanceLockExclusive || second.err != ErrMaintenanceLockHeld {
			t.Fatalf("CLI lock while server lock held = %+v", second)
		}
		return
	}
	if first.mode == maintenanceLockExclusive && first.err == ErrMaintenanceLockHeld {
		if second.mode != maintenanceLockShared || second.err != nil {
			t.Fatalf("server lock after CLI attempt = %+v", second)
		}
		defer second.lock.Close()
		return
	}
	t.Fatalf("unexpected race results: first=%+v second=%+v", first, second)
}

func TestMaintenanceLockProcessDeathReleasesSharedLock(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("process lock test exercises Linux flock")
	}
	if database := os.Getenv("COOKBOOK_LOCK_HELPER_DB"); database != "" {
		lock, err := acquireMaintenanceLock(database, maintenanceLockShared)
		if err != nil {
			t.Fatal(err)
		}
		_ = lock
		ready := os.Getenv("COOKBOOK_LOCK_HELPER_READY")
		if err := os.WriteFile(ready, []byte("ready"), 0600); err != nil {
			t.Fatal(err)
		}
		select {}
	}

	dir := t.TempDir()
	database := filepath.Join(dir, "cookbook.db")
	ready := filepath.Join(dir, "ready")
	command := exec.Command(os.Args[0], "-test.run=^TestMaintenanceLockProcessDeathReleasesSharedLock$")
	command.Env = append(os.Environ(), "COOKBOOK_LOCK_HELPER_DB="+database, "COOKBOOK_LOCK_HELPER_READY="+ready)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if command.Process != nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if command.ProcessState != nil {
			t.Fatalf("lock-holder process exited early: %v", command.ProcessState)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(ready); err != nil {
		t.Fatalf("lock-holder process did not become ready: %v", err)
	}
	if _, err := acquireMaintenanceLock(database, maintenanceLockExclusive); err != ErrMaintenanceLockHeld {
		t.Fatalf("exclusive lock while child holds shared lock: %v", err)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatalf("wait for killed lock-holder: %v", err)
		}
	}
	command.Process = nil
	exclusive, err := acquireMaintenanceLock(database, maintenanceLockExclusive)
	if err != nil {
		t.Fatalf("exclusive lock after process death: %v", err)
	}
	if err := exclusive.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMaintenanceLockRejectsNonFilesystemDatabase(t *testing.T) {
	for _, path := range []string{":memory:", "file:memory?mode=memory&cache=shared", ""} {
		if _, err := acquireMaintenanceLock(path, maintenanceLockExclusive); err == nil {
			t.Errorf("acquire lock for %q unexpectedly succeeded", path)
		}
	}
}
