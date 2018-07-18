// Copyright 2018 The go-ethereum Authors
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

package stream

import (
	"context"
	crand "crypto/rand"
	"fmt"
	"io"
	"math"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/discover"
	"github.com/ethereum/go-ethereum/p2p/simulations/adapters"
	"github.com/ethereum/go-ethereum/swarm/log"
	"github.com/ethereum/go-ethereum/swarm/network"
	"github.com/ethereum/go-ethereum/swarm/network/simulation"
	"github.com/ethereum/go-ethereum/swarm/state"
	"github.com/ethereum/go-ethereum/swarm/storage"
)

const dataChunkCount = 200

func TestSyncerSimulation(t *testing.T) {
	testSyncBetweenNodes(t, 2, 1, dataChunkCount, true, 1)
	testSyncBetweenNodes(t, 4, 1, dataChunkCount, true, 1)
	testSyncBetweenNodes(t, 8, 1, dataChunkCount, true, 1)
	testSyncBetweenNodes(t, 16, 1, dataChunkCount, true, 1)
}

func createMockStore(id discover.NodeID, addr *network.BzzAddr) (storage.ChunkStore, error) {
	var err error
	address := common.BytesToAddress(id.Bytes())
	mockStore := globalStore.NewNodeStore(address)
	params := storage.NewDefaultLocalStoreParams()
	//TODO: FIX global here
	//datadirs[id], err = ioutil.TempDir("", "localMockStore-"+id.TerminalString())
	if err != nil {
		return nil, err
	}
	//params.Init(datadirs[id])
	params.BaseKey = addr.Over()
	lstore, err := storage.NewLocalStore(params, mockStore)
	return lstore, nil
}

func testSyncBetweenNodes(t *testing.T, nodes, conns, chunkCount int, skipCheck bool, po uint8) {
	defer setDefaultSkipCheck(defaultSkipCheck)
	defaultSkipCheck = skipCheck
	//data directories for each node and store
	//datadirs = make(map[discover.NodeID]string)
	//TODO fix mockstore stuff
	/*
		if *useMockStore {
			createStoreFunc = createMockStore
			createGlobalStore()
		} else {
			createStoreFunc = createTestLocalStorageFromSim
		}
	*/

	sim := simulation.New(map[string]simulation.ServiceFunc{
		"streamer": func(ctx *adapters.ServiceContext, bucket *sync.Map) (s node.Service, cleanup func(), err error) {

			id := ctx.Config.ID
			addr := network.NewAddrFromNodeID(id)
			//hack to put addresses in same space
			addr.OAddr[0] = byte(0)

			store, datadir, err := createTestLocalStorageForId(id, addr)
			if err != nil {
				return nil, nil, err
			}
			bucket.Store(bucketKeyStore, store)
			cleanup = func() {
				store.Close()
				os.RemoveAll(datadir)
			}
			localStore := store.(*storage.LocalStore)
			db := storage.NewDBAPI(localStore)
			bucket.Store(bucketKeyDB, db)
			kad := network.NewKademlia(addr.Over(), network.NewKadParams())
			delivery := NewDelivery(kad, db)
			bucket.Store(bucketKeyDelivery, delivery)

			r := NewRegistry(addr, delivery, db, state.NewInmemoryStore(), &RegistryOptions{
				SkipCheck:  defaultSkipCheck,
				DoRetrieve: false,
			})
			RegisterSwarmSyncerServer(r, db)
			RegisterSwarmSyncerClient(r, db)

			fileStore := storage.NewFileStore(storage.NewNetStore(localStore, nil), storage.NewFileStoreParams())
			bucket.Store(bucketKeyFileStore, fileStore)
			testRegistry := &TestRegistry{Registry: r, fileStore: fileStore}
			bucket.Store(bucketKeyRegistry, testRegistry)

			return testRegistry, cleanup, nil

		},
	})
	defer sim.Close()

	// create context for simulation run
	timeout := 30 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	// defer cancel should come before defer simulation teardown
	defer cancel()

	_, err := sim.AddNodesAndConnectFull(nodes)
	if err != nil {
		t.Fatal(err)
	}
	result := sim.Run(ctx, func(ctx context.Context, sim *simulation.Simulation) error {
		nodeIDs := sim.UpNodeIDs()

		nodeIndex := make(map[discover.NodeID]int)
		for i, id := range nodeIDs {
			nodeIndex[id] = i
		}

		// collect hashes in po 1 bin for each node
		hashes := make([][]storage.Address, nodes)
		totalHashes := 0
		hashCounts := make([]int, nodes)
		for i := nodes - 1; i >= 0; i-- {
			if i < nodes-1 {
				hashCounts[i] = hashCounts[i+1]
			}
			item, ok := sim.NodeItem(nodeIDs[i], bucketKeyDB)
			if !ok {
				return fmt.Errorf("No DB")
			}
			db := item.(*storage.DBAPI)
			db.Iterator(0, math.MaxUint64, po, func(addr storage.Address, index uint64) bool {
				hashes[i] = append(hashes[i], addr)
				totalHashes++
				hashCounts[i]++
				return true
			})
		}

		disconnections := sim.PeerEvents(
			context.Background(),
			sim.NodeIDs(),
			simulation.NewPeerEventsFilter().Type(p2p.PeerEventTypeDrop),
		)

		go func() {
			for d := range disconnections {
				if d.Error != nil {
					log.Error("peer drop", "node", d.NodeID, "peer", d.Event.Peer)
					t.Fatal(d.Error)
				}
			}
		}()

		// each node Subscribes to each other's swarmChunkServerStreamName
		for j := 0; j < nodes-1; j++ {
			id := nodeIDs[j]
			client, err := sim.Net.GetNode(id).Client()
			if err != nil {
				t.Fatal(err)
			}
			sid := nodeIDs[j+1]
			client.CallContext(ctx, nil, "stream_subscribeStream", sid, NewStream("SYNC", FormatSyncBinKey(1), false), NewRange(0, 0), Top)
			if err != nil {
				return err
			}
			if j > 0 {
				item, ok := sim.NodeItem(nodeIDs[j], bucketKeyFileStore)
				if !ok {
					return fmt.Errorf("No filestore")
				}
				fileStore := item.(*storage.FileStore)
				size := chunkCount * chunkSize
				_, wait, err := fileStore.Store(ctx, io.LimitReader(crand.Reader, int64(size)), int64(size), false)
				if err != nil {
					t.Fatal(err.Error())
				}
				wait(ctx)
			}
		}
		// here we distribute chunks of a random file into stores 1...nodes
		if *waitKademlia {
			if _, err := sim.WaitTillHealthy(ctx, 2); err != nil {
				return err
			}
		}

		var total, found int
		for _, node := range nodeIDs {
			i := nodeIndex[node]

			for j := i; j < nodes; j++ {
				total += len(hashes[j])
				for _, key := range hashes[j] {
					item, ok := sim.NodeItem(nodeIDs[j], bucketKeyDB)
					if !ok {
						return fmt.Errorf("No DB")
					}
					db := item.(*storage.DBAPI)
					chunk, err := db.Get(key)
					if err == storage.ErrFetching {
						<-chunk.ReqC
					} else if err != nil {
						continue
					}
					// needed for leveldb not to be closed?
					// chunk.WaitToStore()
					found++
				}
			}
			log.Debug("sync check", "node", node, "index", i, "bin", po, "found", found, "total", total)
		}
		if total == found {
			return nil
		}
		return fmt.Errorf("Total not equallying found")
	})

	if result.Error != nil {
		t.Fatal(result.Error)
	}
}
