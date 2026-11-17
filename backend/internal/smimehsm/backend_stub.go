//go:build !pkcs11 || !cgo

package smimehsm

import "fmt"

func openBackend(_ Config, _ IdentityConfig) (tokenSession, error) {
	return nil, fmt.Errorf("this server was built without PKCS#11 support; rebuild with -tags pkcs11 and cgo enabled")
}
