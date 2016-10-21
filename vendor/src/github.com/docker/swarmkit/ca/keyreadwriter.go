package ca

import (
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/docker/swarmkit/ioutils"
)

const (
	// keyPerms are the permissions used to write the TLS keys
	keyPerms = 0600
	// certPerms are the permissions used to write TLS certificates
	certPerms = 0644
)

// KeyHeaderUpdater is a function which takes a set of PEM heaaders, the old
// kek when writing, and the new kek when writing, and updates the PEM header
// given both
type KeyHeaderUpdater func(map[string]string, []byte, []byte) error

// KeyReader reads a TLS cert and key from disk
type KeyReader interface {
	Read() ([]byte, []byte, error)
	ReadHeaders() (map[string]string, []byte, error)
}

// KeyWriter writes a TLS key and cert to disk
type KeyWriter interface {
	Write([]byte, []byte, *KEKUpdate) error
	UpdateHeaders(cb func(map[string]string, []byte) error) error
	RotateKEK(newKek []byte) error
}

// KEKUpdate provides an optional update to the kek when writing.  The structure
// is needed so that we can tell the difference between "do not encrypt anymore"
// and there is "no update".
type KEKUpdate struct {
	KEK []byte
}

// KeyReadWriter is an object that knows how to read and write TLS keys and certs to disk,
// optionally encrypted and while preserving existing PEM headers.
type KeyReadWriter struct {
	mu            sync.Mutex
	kek           []byte
	paths         CertPaths
	headerUpdater KeyHeaderUpdater
}

// NewKeyReadWriter creates a new KeyReadWriter
func NewKeyReadWriter(paths CertPaths, kek []byte, headerUpdater KeyHeaderUpdater) *KeyReadWriter {
	return &KeyReadWriter{
		kek:           kek,
		paths:         paths,
		headerUpdater: headerUpdater,
	}
}

// Read will read a TLS cert and key from the given paths
func (k *KeyReadWriter) Read() ([]byte, []byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	keyBlock, err := k.readKey()
	if err != nil {
		return nil, nil, err
	}
	cert, err := ioutil.ReadFile(k.paths.Cert)
	if err != nil {
		return nil, nil, err
	}
	return cert, pem.EncodeToMemory(keyBlock), err
}

// ReadHeaders reads headers from the key
func (k *KeyReadWriter) ReadHeaders() (map[string]string, []byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	keyBlock, err := k.readKeyblock()
	if err != nil {
		return nil, nil, err
	}
	return keyBlock.Headers, k.kek, nil
}

// RotateKEK re-encrypts the key with a new KEK
func (k *KeyReadWriter) RotateKEK(newKek []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	keyBlock, err := k.readKey()
	if err != nil {
		return err
	}

	if k.headerUpdater != nil {
		if err := k.headerUpdater(keyBlock.Headers, k.kek, newKek); err != nil {
			return err
		}
	}

	if err := k.writeKey(keyBlock, newKek); err != nil {
		return err
	}

	k.kek = newKek
	return nil
}

// UpdateHeaders reads headers from the key, and takes a callback that will update it
func (k *KeyReadWriter) UpdateHeaders(cb func(map[string]string, []byte) error) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	keyBlock, err := k.readKeyblock()
	if err != nil {
		return err
	}
	if err := cb(keyBlock.Headers, k.kek); err != nil {
		return err
	}
	if err := ioutils.AtomicWriteFile(k.paths.Key, pem.EncodeToMemory(keyBlock), keyPerms); err != nil {
		return err
	}
	return nil
}

// Write attempts write a cert and key to text.  It preserves existing PEM headers on
// the original key.  This can also optionally update the KEK while writing, if an updated
// KEK is provided.  If the pointer to the update KEK is nil, then we don't update.
// If the updated KEK itself is nil, then we update the KEK to be nil (data should be
// unencrypted).
func (k *KeyReadWriter) Write(certBytes, plaintextKeyBytes []byte, kekUpdate *KEKUpdate) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	// current assumption is that the cert and key will be in the same directory
	if err := os.MkdirAll(filepath.Dir(k.paths.Key), 0755); err != nil {
		return err
	}

	keyBlock, _ := pem.Decode(plaintextKeyBytes)
	if keyBlock == nil {
		return errors.New("invalid PEM-encoded private key")
	}

	// see if a key exists already - if so, pull out the headers
	oldKeyBlock, err := k.readKeyblock()
	if err == nil {
		mergePEMHEaders(keyBlock.Headers, oldKeyBlock.Headers)
	}

	useKEK := k.kek
	if kekUpdate != nil {
		useKEK = kekUpdate.KEK
	}
	if k.headerUpdater != nil {
		if err := k.headerUpdater(keyBlock.Headers, k.kek, useKEK); err != nil {
			return err
		}
	}

	if err := k.writeKey(keyBlock, useKEK); err != nil {
		return err
	}
	k.kek = useKEK
	return ioutils.AtomicWriteFile(k.paths.Cert, certBytes, certPerms)
}

func (k *KeyReadWriter) readKeyblock() (*pem.Block, error) {
	key, err := ioutil.ReadFile(k.paths.Key)
	if err != nil {
		return nil, err
	}

	// Decode the PEM private key
	keyBlock, _ := pem.Decode(key)
	if keyBlock == nil {
		return nil, errors.New("invalid PEM-encoded private key")
	}

	return keyBlock, nil
}

// readKey returns the decrypted key pem bytes, and enforces the KEK if applicable
// (writes it back with the correct encryption if it is not correctly encrypted)
func (k *KeyReadWriter) readKey() (*pem.Block, error) {
	keyBlock, err := k.readKeyblock()
	if err != nil {
		return nil, err
	}

	if !x509.IsEncryptedPEMBlock(keyBlock) {
		return keyBlock, nil
	}

	derBytes, err := x509.DecryptPEMBlock(keyBlock, k.kek)
	if err != nil {
		return nil, err
	}
	// preserve headers minus PEM encryption headers
	headers := make(map[string]string)
	mergePEMHEaders(headers, keyBlock.Headers)
	return &pem.Block{
		Type:    keyBlock.Type, // the key type doesn't change
		Bytes:   derBytes,
		Headers: headers,
	}, nil
}

// writeKey takes an unencrypted keyblock and, if the kek is not nil, encrypts it before
// writing it to disk.  If the kek is nil, writes it to disk unencrypted.
func (k *KeyReadWriter) writeKey(keyBlock *pem.Block, writeKEK []byte) error {
	if writeKEK != nil {
		encryptedPEMBlock, err := x509.EncryptPEMBlock(rand.Reader,
			keyBlock.Type,
			keyBlock.Bytes,
			writeKEK,
			x509.PEMCipherAES256)
		if err != nil {
			return err
		}
		if encryptedPEMBlock.Headers == nil {
			return errors.New("unable to encrypt key - invalid PEM file produced")
		}
		mergePEMHEaders(encryptedPEMBlock.Headers, keyBlock.Headers)
		keyBlock = encryptedPEMBlock
	}
	return ioutils.AtomicWriteFile(k.paths.Key, pem.EncodeToMemory(keyBlock), keyPerms)
}

// merges one set of PEM headers onto another, excepting for key encryption value
// "proc-type" and "dek-info"
func mergePEMHEaders(original, newSet map[string]string) {
	for key, value := range newSet {
		normalizedKey := strings.TrimSpace(strings.ToLower(key))
		if normalizedKey != "proc-type" && normalizedKey != "dek-info" {
			original[key] = value
		}
	}
}
