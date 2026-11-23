//go:build !darwin

package main

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/mishankov/llm-meter/internal/relay"
)

type fileKeys string

func platformKeyStore(dir string) keyStore { return fileKeys(dir) }

func (store fileKeys) path(id string) (string, error) {
	if id == "" || !filepath.IsLocal(id) || filepath.Base(id) != id {
		return "", errors.New("invalid key identifier")
	}
	return filepath.Join(string(store), "key-"+id), nil
}

func (store fileKeys) get(id string) (string, error) {
	path, err := store.path(id)
	if err != nil {
		return "", err
	}
	return relay.ReadKeyFile(path)
}

func (store fileKeys) add(id, key string) error {
	path, err := store.path(id)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, []byte(key), 0o600)
}

func (store fileKeys) remove(id string) error {
	path, err := store.path(id)
	if err != nil {
		return err
	}
	return os.Remove(path)
}
