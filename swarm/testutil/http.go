// Copyright 2017 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package testutil

import (
	"fmt"
	"io/ioutil"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/swarm/api"
	httpapi "github.com/ethereum/go-ethereum/swarm/api/http"
	"github.com/ethereum/go-ethereum/swarm/storage"
)

//an interface to mock ENS
type EnsMocker interface {
	Resolve(name string) (common.Hash, error)
}

type ensMocker struct{}

//a couple of addresses which can be used for successful resolve test
var EnsSuccessResolveNames = map[string]common.Hash{
	"test.eth":     common.HexToHash("81a7cbc7fa2467e3a4efd0c9f590f5c4c1abaf396e50bb3bd0ca7e4aba7f22da"),
	"theswarm.eth": common.HexToHash("0ebde5e74b5ccb4810653a0941b8fa643941b49bb027bd9c03922ce28a67a835"),
	"enstest.test": common.HexToHash("74697facce2b0f3bdddb9474f1a6256f4f6e8e73c55c7b3545efb12778f1f067"),
}

//use these to test for unexisting ENS names
var EnsFailResolveNames = []string{
	"failtest.eth",
	"failtest.test",
}

//mocker implementation of ENS resolution
func (mocker *ensMocker) Resolve(name string) (resolved common.Hash, err error) {
	//check if the passed name should successfully resolve
	for n, h := range EnsSuccessResolveNames {
		if n == name {
			return h, nil
		}
	}
	//check if the passed name should fail to resolve
	for _, el := range EnsFailResolveNames {
		if el == name {
			return resolved, fmt.Errorf("This name does not exist in the ENS test")
		}
	}
	//fallback
	return resolved, fmt.Errorf("Unexpected situation during test resolve")
}

func NewMockEns() EnsMocker {
	return &ensMocker{}
}

func NewTestSwarmServerWithEns(t *testing.T) *TestSwarmServer {
	dpa, dir := NewTestDpa(t)
	dns := NewMockEns()
	a := api.NewApi(dpa, dns)
	srv := httptest.NewServer(httpapi.NewServer(a))
	return &TestSwarmServer{
		Server: srv,
		Dpa:    dpa,
		dir:    dir,
	}
}

func NewTestSwarmServer(t *testing.T) *TestSwarmServer {
	dpa, dir := NewTestDpa(t)
	a := api.NewApi(dpa, nil)
	srv := httptest.NewServer(httpapi.NewServer(a))
	return &TestSwarmServer{
		Server: srv,
		Dpa:    dpa,
		dir:    dir,
	}
}

type TestSwarmServer struct {
	*httptest.Server

	Dpa *storage.DPA
	dir string
}

func (t *TestSwarmServer) Close() {
	t.Server.Close()
	t.Dpa.Stop()
	os.RemoveAll(t.dir)
}

func NewTestDpa(t *testing.T) (*storage.DPA, string) {
	dir, err := ioutil.TempDir("", "swarm-storage-test")
	if err != nil {
		t.Fatal(err)
	}
	storeparams := &storage.StoreParams{
		ChunkDbPath:   dir,
		DbCapacity:    5000000,
		CacheCapacity: 5000,
		Radius:        0,
	}
	localStore, err := storage.NewLocalStore(storage.MakeHashFunc("SHA3"), storeparams)
	if err != nil {
		os.RemoveAll(dir)
		t.Fatal(err)
	}
	chunker := storage.NewTreeChunker(storage.NewChunkerParams())
	dpa := &storage.DPA{
		Chunker:    chunker,
		ChunkStore: localStore,
	}

	dpa.Start()
	return dpa, dir
}
