package encryption

import (
	"crypto/rand"
	"fmt"
	"io"

	"github.com/docker/swarmkit/api"

	"golang.org/x/crypto/nacl/secretbox"
)

// This provides the default implementation of an encoder and decoder, as well
// as the default KDF function.

// NACLSecretbox is an implementation of an encoder/decoder.  Encoding
// generates random Nonces.
type NACLSecretbox struct {
	key [32]byte
}

// NewNACLSecretbox returns a new NACL secretbox encoder/decoder with the given key
func NewNACLSecretbox(key []byte) NACLSecretbox {
	var lengthed [32]byte
	copy(lengthed[:], key)
	return NACLSecretbox{
		key: lengthed,
	}
}

// Algorithm returns the type of algorhtm this is (NACL Secretbox using XSalsa20 and Poly1305)
func (n NACLSecretbox) Algorithm() api.EncryptedRecord_Algorithm {
	return api.NaclSecretboxSalsa20Poly1305
}

// Encode encrypts some bytes and returns an encrypted record
func (n NACLSecretbox) Encode(data []byte) (*api.EncryptedRecord, error) {
	var nonce [24]byte
	if _, err := io.ReadFull(rand.Reader, nonce[:]); err != nil {
		return nil, err
	}

	encrypted := secretbox.Seal(nil, data, &nonce, &n.key)
	return &api.EncryptedRecord{
		Algorithm: n.Algorithm(),
		Data:      encrypted,
		Nonce:     nonce[:],
	}, nil
}

// Decode decrypts a EncryptedRecord and returns some bytes
func (n NACLSecretbox) Decode(record api.EncryptedRecord) ([]byte, error) {
	if record.Algorithm != n.Algorithm() {
		return nil, fmt.Errorf("not a NACL secretbox record")
	}

	var decryptNonce [24]byte
	copy(decryptNonce[:], record.Nonce[:24])
	decrypted, ok := secretbox.Open(nil, record.Data, &decryptNonce, &n.key)
	if !ok {
		return nil, fmt.Errorf("decryption error using NACL secretbox")
	}
	return decrypted, nil
}
