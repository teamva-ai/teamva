package runtime

import (
	"errors"
	"os"
)

func fileBridgePlatformSupported() bool { return false }

func ensureRuntimeSubdirectory(parent *os.File, name string) (*os.File, error) {
	return nil, errors.New("safe file bridge directories are unavailable on Windows")
}

func createRuntimeSubdirectory(parent *os.File, name string) (*os.File, error) {
	return nil, errors.New("safe file bridge directories are unavailable on Windows")
}

func readBridgeRequestAt(directory *os.File, name string, limit int64) ([]byte, error) {
	return nil, errors.New("safe file bridge request reads are unavailable on Windows")
}

func bridgeResponseExistsAt(directory *os.File, name string) (bool, error) {
	return false, errors.New("safe file bridge response reads are unavailable on Windows")
}

func writeBridgeResponseAt(directory *os.File, name string, payload []byte) error {
	return errors.New("safe file bridge response writes are unavailable on Windows")
}

func openRuntimeDirectory(path string) (*os.File, error) {
	return nil, errors.New("safe runtime directory access is unavailable on Windows")
}
