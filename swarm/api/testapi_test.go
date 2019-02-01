// Copyright 2019 The go-ethereum Authors
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

package api

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"testing"

	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/simulations"
	"github.com/ethereum/go-ethereum/p2p/simulations/adapters"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/ethereum/go-ethereum/swarm/storage"
)

type testSvc struct {
	ls *storage.NetStore
}

func newTestSvc() *testSvc {
	svc := &testSvc{}
	var err error
	datadir, err := ioutil.TempDir("", "storage")
	if err != nil {
		//t.Fatal(err)
	}

	params := &storage.LocalStoreParams{
		StoreParams: storage.NewStoreParams(uint64(10), uint(10), nil, nil),
	}
	params.Init(datadir)

	lstore, err := storage.NewLocalStore(params, nil)
	if err != nil {
		_ = os.RemoveAll(datadir)
		//t.Fatal(err)
	}
	svc.ls, err = storage.NewNetStore(lstore, nil)
	if err != nil {
		//return nil, err
	}
	return svc
}

/////////////////////////////////////////////////////////////////////
// SECTION: node.Service interface
/////////////////////////////////////////////////////////////////////

func (ts *testSvc) Start(srv *p2p.Server) error {
	fmt.Println("Startin...")
	return nil
}

func (ts *testSvc) Stop() error {
	fmt.Println("Stopping...")
	ts.ls.Close()
	return nil
}

func (ts *testSvc) Protocols() []p2p.Protocol {
	return []p2p.Protocol{
		{
			Name:    "testsvc",
			Version: 1.0,
			Length:  1024,
			Run:     ts.Run,
		},
	}
}

func (ts *testSvc) Run(peer *p2p.Peer, rw p2p.MsgReadWriter) error {
	fmt.Println("running")
	return nil
}

func (ts *testSvc) APIs() []rpc.API {
	apis := []rpc.API{
		GetDebugAPIDesc(ts.ls),
	}
	return apis
}

func TestDebugAPI(t *testing.T) {
	testS := newTestSvc()
	svc := adapters.Services{
		"testsvc": func(ctx *adapters.ServiceContext) (node.Service, error) {
			return testS, nil
		},
	}
	nodeconf := adapters.RandomNodeConfig()
	nodeconf.Services = []string{"testsvc"}
	adapter := adapters.NewSimAdapter(svc)
	net := simulations.NewNetwork(adapter, &simulations.NetworkConfig{
		ID:             "0",
		DefaultService: "testsvc",
	})
	node, err := net.NewNodeWithConfig(nodeconf)
	if err != nil {
		t.Fatalf("net create fail %v", err)
	}
	err = net.Start(node.ID())
	if err != nil {
		t.Fatalf("error starting node 1: %v", err)
	}
	fmt.Println("started")

	client, err := node.Client()
	if err != nil {
		t.Fatalf("create node 1 rpc client fail: %v", err)
	}
	fmt.Println("queried")

	addr := "1234"
	var has bool
	err = client.Call(&has, "debugapi_hasChunk", addr)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println(has)
	ch := storage.GenerateRandomChunk(128)
	testS.ls.Put(context.Background(), ch)
	err = client.Call(&has, "debugapi_hasChunk", ch.Address())
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println(has)
	fmt.Println("finish")
}
