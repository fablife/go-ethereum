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

package network

import (
	"bytes"
	"flag"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/p2p/discover"
	"github.com/ethereum/go-ethereum/p2p/simulations"
	"github.com/ethereum/go-ethereum/p2p/simulations/adapters"
	"github.com/ethereum/go-ethereum/swarm/state"
)

var (
	vmodule          = flag.String("vmodule", "", "log filters for logger via Vmodule")
	verbosity        = flag.Int("verbosity", 0, "log filters for logger via Vmodule")
	currentNetworkId int
	cnt              int
	nodeMap          map[int][]discover.NodeID
	kademlias        map[discover.NodeID]*Kademlia
)

const numberOfNets = 4

func init() {
	flag.Parse()
	rand.Seed(time.Now().Unix())
}

/*
Run the network ID test.
The test creates a const amount of networks,
a number of nodes, then connects nodes with each other.

Each node gets a network ID assigned according to the number of networks.
Having more network IDs is just arbitrary in order to exclude
fortuitous results.

Nodes should only connect with other nodes with the same network ID
*/
func TestNetworkId(t *testing.T) {
	log.Debug("Start test")
	numNodes := 24
	//the nodeMap maps all nodes (slice value) with the same network ID (key)
	nodeMap = make(map[int][]discover.NodeID)
	//set up the network and connect nodes
	net, err := setupNetwork(numNodes)
	if err != nil {
		t.Fatalf("Error setting up network: %v", err)
	}
	defer net.Shutdown()
	//let's sleep to ensure all nodes are connected
	time.Sleep(4 * time.Second)
	//for each group sharing the same network ID...
	for _, netIdGroup := range nodeMap {
		log.Trace("netIdGroup size", "size", len(netIdGroup))
		//...check that their size of the kademlia is of the expected size
		//the assumption is that it should be the size of the group minus 1 (the node itself)
		for _, node := range netIdGroup {
			if kademlias[node].addrs.Size() != len(netIdGroup)-1 {
				t.Fatalf("Kademlia size has not expected peer size. Kademlia size: %d, expected size: %d", kademlias[node].addrs.Size(), len(netIdGroup)-1)
			}
			kademlias[node].EachAddr(nil, 0, func(addr OverlayAddr, _ int, _ bool) bool {
				found := false
				for _, nd := range netIdGroup {
					p := ToOverlayAddr(nd.Bytes())
					if bytes.Equal(p, addr.Address()) {
						found = true
					}
				}
				if !found {
					t.Fatalf("Expected node not found for node %s", node.String())
				}
				return true
			})
		}
	}
	log.Info("Test terminated successfully")
}

// setup simulated network with bzz/discovery and pss services.
// connects nodes in a circle
// if allowRaw is set, omission of builtin pss encryption is enabled (see PssParams)
func setupNetwork(numnodes int) (net *simulations.Network, err error) {
	log.Debug("Setting up network")
	nodes := make([]*simulations.Node, numnodes)
	if numnodes < 16 {
		return nil, fmt.Errorf("Minimum two nodes in network")
	}
	adapter := adapters.NewSimAdapter(newServices())
	//create the network
	net = simulations.NewNetwork(adapter, &simulations.NetworkConfig{
		ID:             "NetworkIdTestNet",
		DefaultService: "bzz",
	})
	log.Debug("Creating networks and nodes")
	//create nodes and connect them to each other
	for i := 0; i < numnodes; i++ {
		log.Trace("iteration: ", "i", i)
		nodeconf := adapters.RandomNodeConfig()
		nodeconf.Services = []string{"bzz"}
		nodes[i], err = net.NewNodeWithConfig(nodeconf)
		if err != nil {
			return nil, fmt.Errorf("error creating node 1: %v", err)
		}
		err = net.Start(nodes[i].ID())
		if err != nil {
			return nil, fmt.Errorf("error starting node 1: %v", err)
		}
		//on every iteration we connect to all previous ones
		for k := i - 1; k >= 0; k-- {
			log.Debug(fmt.Sprintf("Connecting node %d with node %d", i, k))
			err = net.Connect(nodes[i].ID(), nodes[k].ID())
			if err != nil {
				return nil, fmt.Errorf("error connecting nodes: %v", err)
			}
		}
	}
	log.Debug("Network setup phase terminated")
	return net, nil
}

func newServices() adapters.Services {
	stateStore := state.NewInmemoryStore()
	kademlias = make(map[discover.NodeID]*Kademlia)
	kademlia := func(id discover.NodeID) *Kademlia {
		if k, ok := kademlias[id]; ok {
			return k
		}
		addr := NewAddrFromNodeID(id)
		params := NewKadParams()
		params.MinProxBinSize = 2
		params.MaxBinSize = 3
		params.MinBinSize = 1
		params.MaxRetries = 1000
		params.RetryExponent = 2
		params.RetryInterval = 1000000
		kademlias[id] = NewKademlia(addr.Over(), params)
		return kademlias[id]
	}
	return adapters.Services{
		"bzz": func(ctx *adapters.ServiceContext) (node.Service, error) {
			addr := NewAddrFromNodeID(ctx.Config.ID)
			hp := NewHiveParams()
			hp.Discovery = false
			cnt++
			//assign the network ID
			currentNetworkId = cnt % numberOfNets
			if ok := nodeMap[currentNetworkId]; ok == nil {
				nodeMap[currentNetworkId] = make([]discover.NodeID, 0)
			}
			//add this node to the group sharing the same network ID
			nodeMap[currentNetworkId] = append(nodeMap[currentNetworkId], ctx.Config.ID)
			log.Debug("current network ID:", "id", currentNetworkId)
			config := &BzzConfig{
				OverlayAddr:  addr.Over(),
				UnderlayAddr: addr.Under(),
				HiveParams:   hp,
				NetworkID:    uint64(currentNetworkId),
			}
			return NewBzz(config, kademlia(ctx.Config.ID), stateStore, nil, nil), nil
		},
	}
}
