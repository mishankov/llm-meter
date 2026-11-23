package main

import "github.com/keybase/go-keychain"

const keychainService = "com.mishankov.llm-meter-relay"

type macKeychain struct{}

func platformKeyStore(_ string) keyStore { return macKeychain{} }

func (macKeychain) get(id string) (string, error) {
	data, err := keychain.GetGenericPassword(keychainService, id, appDirName, "")
	return string(data), err
}

func (macKeychain) add(id, key string) error {
	item := keychain.NewGenericPassword(keychainService, id, appDirName, []byte(key), "")
	return keychain.AddItem(item)
}

func (macKeychain) remove(id string) error {
	return keychain.DeleteGenericPasswordItem(keychainService, id)
}
