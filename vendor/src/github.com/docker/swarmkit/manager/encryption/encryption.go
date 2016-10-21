package encryption

import (
	"fmt"

	"github.com/docker/swarmkit/api"
	"github.com/gogo/protobuf/proto"
)

// This package defines the interfaces and encryption package

// ErrCannotDecode is the type of error returned when some data cannot be decoded as plaintext
type ErrCannotDecode struct {
	msg string
}

func (e ErrCannotDecode) Error() string {
	return e.msg
}

// A Decoder can decrypt an encrypted record
type Decoder interface {
	Decode(api.EncryptedRecord) ([]byte, error)
}

// A Encoder can encrypt some bytes into an encrypted record
type Encoder interface {
	Encode(data []byte) (*api.EncryptedRecord, error)
}

type noopCoder struct{}

func (n noopCoder) Decode(e api.EncryptedRecord) ([]byte, error) {
	if e.Algorithm != n.Algorithm() {
		return nil, fmt.Errorf("not an unencrypted record")
	}
	return e.Data, nil
}

func (n noopCoder) Encode(data []byte) (*api.EncryptedRecord, error) {
	return &api.EncryptedRecord{
		Algorithm: n.Algorithm(),
		Data:      data,
	}, nil
}

func (n noopCoder) Algorithm() api.EncryptedRecord_Algorithm {
	return api.NotEncrypted
}

// NoopCoder is just a pass-through coder - it does not actually encode or
// decode any data
var NoopCoder = noopCoder{}

// Decode turns a slice of bytes serialized as an EncryptedRecord into a slice of plaintext bytes
func Decode(encoded []byte, decoder Decoder) ([]byte, error) {
	if decoder == nil {
		decoder = NoopCoder
	}
	r := api.EncryptedRecord{}
	if err := proto.Unmarshal(encoded, &r); err != nil || r.Size() != len(encoded) {
		// nope, this wasn't marshalled as a EncryptedRecord
		return nil, ErrCannotDecode{msg: "unable to unmarshal as EncryptedRecord"}
	}
	plaintext, err := decoder.Decode(r)
	if err != nil {
		return nil, ErrCannotDecode{msg: err.Error()}
	}
	return plaintext, nil
}

// Encode turns a slice of bytes into a serialized EncryptedRecord slice of bytes
func Encode(plaintext []byte, encoder Encoder) ([]byte, error) {
	if encoder == nil {
		encoder = NoopCoder
	}

	encryptedRecord, err := encoder.Encode(plaintext)
	if err != nil {
		return nil, fmt.Errorf("unable to encode entry data: %s", err.Error())
	}

	data, err := proto.Marshal(encryptedRecord)
	if err != nil {
		return nil, fmt.Errorf("unable to encode entry data: %s", err.Error())
	}

	return data, nil
}

// GetDefaults returns a default encoder and decoder
func GetDefaults(key []byte) (Encoder, Decoder) {
	n := NewNACLSecretbox(key)
	return n, n
}
