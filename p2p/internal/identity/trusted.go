package identity

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

// ReadTrustedKeys reads one .pub file, or every .pub file in a directory:
// the members of a network. The key except (our own, in a shared
// directory) is left out.
func ReadTrustedKeys(path string, except []byte) ([][]byte, error) {
	paths := []string{path}
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		paths, _ = filepath.Glob(filepath.Join(path, "*.pub"))
	}
	var keys [][]byte
	for _, p := range paths {
		key, err := ReadPublicKey(p)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(key, except) {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("%s: no public keys of other peers", path)
	}
	return keys, nil
}
