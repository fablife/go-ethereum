package storage

import (
	"crypto/ecdsa"
	"fmt"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/contracts/ens"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rpc"
)

// Implements Mutable Resources as offchain ENS resolvers
//
// The data part of the update is forced to be a valid ENS content hash
//
// Also, the ENSResourceHandler only allows creation and update of
// Resources from the ENS owner's address
//
type ENSResourceHandler struct {
	*RawResourceHandler
	addr   common.Address
	ensapi *ens.ENS
}

func NewENSResourceHandler(privKey *ecdsa.PrivateKey, datadir string, cloudStore CloudStore, rpcClient *rpc.Client, backend bind.ContractBackend, ensAddr common.Address) (*ENSResourceHandler, error) {
	transactOpts := bind.NewKeyedTransactor(privKey)
	ensinstance, err := ens.NewENS(transactOpts, ensAddr, backend)
	if err != nil {
		return nil, err
	}
	rh, err := NewRawResourceHandler(privKey, datadir, cloudStore, rpcClient, ens.EnsNode)
	if err != nil {
		return nil, err
	}
	rh.nameHashFunc = func(name string) common.Hash {
		return ens.EnsNode(name)
	}

	return &ENSResourceHandler{
		RawResourceHandler: rh,
		addr:               crypto.PubkeyToAddress(privKey.PublicKey),
		ensapi:             ensinstance,
	}, nil
}

func (self *ENSResourceHandler) NewResource(name string, frequency uint64) (*resource, error) {
	ok, err := self.IsOwner(name)
	if err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("Not Owner")
	}
	return self.RawResourceHandler.NewResource(name, frequency)
}

func (self *ENSResourceHandler) Update(name string, data []byte) (Key, error) {
	ok, err := self.IsOwner(name)
	if err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("Not Owner")
	}
	return self.RawResourceHandler.Update(name, data)
}

func (self *ENSResourceHandler) IsOwner(name string) (bool, error) {
	owneraddr, err := self.ensapi.Owner(self.RawResourceHandler.nameHashFunc(name))
	if err != nil {
		return false, fmt.Errorf("ENS error: %v", err)
	}
	return owneraddr == self.addr, nil
}
