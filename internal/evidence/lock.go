package evidence

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"syscall"
)

type lockMode int

const (
	lockShared lockMode = iota
	lockExclusive
)

var (
	errLockBusy = errors.New("POSIX byte lock is busy")
	// RISK(race): POSIX process lock은 같은 process의 goroutine을 직렬화하지 않으므로 이 보조 lock이 필요하다.
	processLocks sync.Map
)

func openLockFile(path string, create bool) (*os.File, error) {
	flags := syscall.O_RDWR
	if create {
		flags |= syscall.O_CREAT
	}
	return openOwnerFile(path, flags, 0o600)
}

func lockByte(file *os.File, mode lockMode) (func() error, error) {
	lockType := int16(syscall.F_RDLCK)
	if mode == lockExclusive {
		lockType = int16(syscall.F_WRLCK)
	}
	lock := byteRange(lockType)
	for {
		err := syscall.FcntlFlock(file.Fd(), syscall.F_SETLKW, &lock)
		if !errors.Is(err, syscall.EINTR) {
			if err != nil {
				return nil, fmt.Errorf("acquire POSIX byte lock: %w", err)
			}
			break
		}
	}
	return func() error {
		unlock := byteRange(int16(syscall.F_UNLCK))
		if err := syscall.FcntlFlock(file.Fd(), syscall.F_SETLK, &unlock); err != nil {
			return fmt.Errorf("release POSIX byte lock: %w", err)
		}
		return nil
	}, nil
}

func tryExclusiveByteLock(file *os.File) error {
	lock := byteRange(int16(syscall.F_WRLCK))
	err := syscall.FcntlFlock(file.Fd(), syscall.F_SETLK, &lock)
	if errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EAGAIN) {
		return errLockBusy
	}
	if err != nil {
		return fmt.Errorf("try POSIX byte lock: %w", err)
	}
	unlock := byteRange(int16(syscall.F_UNLCK))
	return syscall.FcntlFlock(file.Fd(), syscall.F_SETLK, &unlock)
}

func byteRange(lockType int16) syscall.Flock_t {
	return syscall.Flock_t{Type: lockType, Whence: int16(io.SeekStart), Start: 0, Len: 1}
}

type processLock struct {
	mutex sync.RWMutex
}

func localProcessLock(path string) *processLock {
	value, _ := processLocks.LoadOrStore(path, &processLock{})
	return value.(*processLock)
}

func withProcessLock(path string, mode lockMode, action func() error) error {
	local := localProcessLock(path)
	if mode == lockExclusive {
		local.mutex.Lock()
		defer local.mutex.Unlock()
	} else {
		local.mutex.RLock()
		defer local.mutex.RUnlock()
	}
	file, err := openLockFile(path, false)
	if err != nil {
		return err
	}
	defer file.Close()
	unlock, err := lockByte(file, mode)
	if err != nil {
		return err
	}
	actionErr := action()
	unlockErr := unlock()
	if actionErr != nil {
		return actionErr
	}
	return unlockErr
}
