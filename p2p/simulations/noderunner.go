// Package simulations simulates p2p networks.
// A NodeRunner simulates starting and stopping real nodes in a network.
package simulations

import (
	"fmt"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p/discover"
	"github.com/julienschmidt/httprouter"
)

type NodeRunner interface {
	Run()
}

var quit chan struct{}

type nodeRunner struct {
	router    *httprouter.Router
	nodes     []discover.NodeID
	network   *Network
	nodeCount int
}

type startStopRunner struct {
	nodeRunner
}

type probabilisticRunner struct {
	nodeRunner
}

type bootRunner struct {
	nodeRunner
}

func NewNodeRunner(network *Network, runnerId string, nodeCount int) {
	var runner NodeRunner
	nr := nodeRunner{
		nodeCount: nodeCount,
		network:   network,
	}
	quit = make(chan struct{}, 1)
	switch runnerId {
	case "startStop":
		runner = &startStopRunner{nr}
	case "probabilistic":
		runner = &probabilisticRunner{nr}
	case "boot":
		runner = &bootRunner{nr}
	default:
		runner = nil
	}
	if runner == nil {
		panic("No runner assigned")
	}
	nr.setupRoutes(runner)
	http.ListenAndServe(":8889", nil)
}

func (r *nodeRunner) setupRoutes(runner NodeRunner) {
	http.HandleFunc("/runSim", func(w http.ResponseWriter, req *http.Request) {
		log.Info("Starting node simulation...")
		go runner.Run()
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.WriteHeader(http.StatusOK)
	})
	http.HandleFunc("/stopSim", func(w http.ResponseWriter, req *http.Request) {
		log.Info("Stopping node simulation...")
		r.stopSim()
		r.network.StopAll()
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.WriteHeader(http.StatusOK)
	})
}

func (r nodeRunner) stopSim() {
	quit <- struct{}{}
}

func (r *bootRunner) Run() {
  r.nodeRunner.connectNodesInRing()
}

func (r *startStopRunner) Run() {
	nodes := r.nodeRunner.connectNodesInRing()
	net := r.nodeRunner.network
	for range time.Tick(10 * time.Second) {
		id := nodes[rand.Intn(len(nodes))]
		go func() {
			log.Error("stopping node", "id", id)
			if err := net.Stop(id); err != nil {
				log.Error("error stopping node", "id", id, "err", err)
				return
			}

			time.Sleep(3 * time.Second)

			log.Error("starting node", "id", id)
			if err := net.Start(id); err != nil {
				log.Error("error starting node", "id", id, "err", err)
				return
			}
		}()
	}
}

func (r *probabilisticRunner) Run() {
	nodes := r.nodeRunner.connectNodesInRing()
	net := r.nodeRunner.network
	for {
		log.Error("Simulation cycle")
		select {
		case <-quit:
			log.Info("Terminating simulation loop")
			return
		default:
		}
		var lowid, highid int
		var wg sync.WaitGroup
		randWait := rand.Intn(5000) + 1000
		rand1 := rand.Intn(9)
		rand2 := rand.Intn(9)
		if rand1 < rand2 {
			lowid = rand1
			highid = rand2
		} else if rand1 > rand2 {
			highid = rand1
			lowid = rand2
		} else {
			if rand1 == 0 {
				rand2 = 9
			} else if rand1 == 9 {
				rand1 = 0
			}
			lowid = rand1
			highid = rand2
		}
		var steps = highid - lowid
		wg.Add(steps)
		for i := lowid; i < highid; i++ {
			select {
			case <-quit:
				log.Info("Terminating simulation loop")
				return
			default:
			}
			log.Info(fmt.Sprintf("node %v shutting down", nodes[i]))
			net.Stop(nodes[i])
			go func(id discover.NodeID) {
				time.Sleep(time.Duration(randWait) * time.Millisecond)
				net.Start(id)
				wg.Done()
			}(nodes[i])
			time.Sleep(time.Duration(randWait) * time.Millisecond)
		}
		wg.Wait()
	}

}

func (r *nodeRunner) connectNodesInRing() []discover.NodeID {
	ids := make([]discover.NodeID, r.nodeCount)
	net := r.network
	for i := 0; i < r.nodeCount; i++ {
		node, err := net.NewNode()
		if err != nil {
			panic(err.Error())
		}
		ids[i] = node.ID()
	}

	for _, id := range ids {
		if err := net.Start(id); err != nil {
			panic(err.Error())
		}
		log.Debug(fmt.Sprintf("node %v starting up", id))
	}
	for i, id := range ids {
		var peerID discover.NodeID
		if i == 0 {
			peerID = ids[len(ids)-1]
		} else {
			peerID = ids[i-1]
		}
		if err := net.Connect(id, peerID); err != nil {
			panic(err.Error())
		}
	}

	return ids
}
