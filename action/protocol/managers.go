package protocol

import (
	"github.com/iotexproject/go-pkgs/hash"
	"github.com/pkg/errors"

	"github.com/iotexproject/iotex-core/db"
)

// NamespaceOption creates an option for given namesapce
func NamespaceOption(ns string) StateOption {
	return func(sc *StateConfig) error {
		sc.Namespace = ns
		return nil
	}
}

// BlockHeightOption creates an option for given namesapce
func BlockHeightOption(height uint64) StateOption {
	return func(sc *StateConfig) error {
		sc.AtHeight = true
		sc.Height = height
		return nil
	}
}

// KeyOption sets the key for call
func KeyOption(key []byte) StateOption {
	return func(cfg *StateConfig) error {
		cfg.Key = make([]byte, len(key))
		copy(cfg.Key, key)
		return nil
	}
}

// LegacyKeyOption sets the key for call with legacy key
func LegacyKeyOption(key hash.Hash160) StateOption {
	return func(cfg *StateConfig) error {
		cfg.Key = make([]byte, len(key[:]))
		copy(cfg.Key, key[:])
		return nil
	}
}

// CreateStateConfig creates a config for accessing stateDB
func CreateStateConfig(opts ...StateOption) (*StateConfig, error) {
	cfg := StateConfig{AtHeight: false}
	for _, opt := range opts {
		if err := opt(&cfg); err != nil {
			return nil, errors.Wrap(err, "failed to execute state option")
		}
	}
	return &cfg, nil
}

type (
	// StateConfig is the config for accessing stateDB
	StateConfig struct {
		Namespace string // namespace used by state's storage
		AtHeight  bool
		Height    uint64
		Key       []byte
	}

	// StateOption sets parameter for access state
	StateOption func(*StateConfig) error

	// StateReader defines an interface to read stateDB
	StateReader interface {
		Height() (uint64, error)
		State(interface{}, ...StateOption) (uint64, error)
	}

	// StateManager defines the stateDB interface atop IoTeX blockchain
	StateManager interface {
		StateReader
		// Accounts
		Snapshot() int
		Revert(int) error
		// General state
		PutState(interface{}, ...StateOption) (uint64, error)
		DelState(...StateOption) (uint64, error)
		GetDB() db.KVStore
	}
)
