package discovery_test

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/simulations"
	"github.com/ethereum/go-ethereum/p2p/simulations/adapters"
	"github.com/ethereum/go-ethereum/swarm/network"
)

// serviceName is used with the exec adapter so the exec'd binary knows which
// service to execute
const serviceName = "discovery"
const testMinProxBinSize = 2

var services = adapters.Services{
	serviceName: newService,
}

func init() {
	// register the discovery service which will run as a devp2p
	// protocol when using the exec adapter
	adapters.RegisterServices(services)

	// log.Root().SetHandler(log.LvlFilterHandler(log.LvlError, log.StreamHandler(os.Stderr, log.TerminalFormat(false))))
	log.Root().SetHandler(log.LvlFilterHandler(log.LvlTrace, log.StreamHandler(os.Stderr, log.TerminalFormat(false))))
}

func TestDiscoverySimulationDockerAdapter(t *testing.T) {
	adapter, err := adapters.NewDockerAdapter()
	if err != nil {
		t.Fatal(err)
	}
	testDiscoverySimulation(t, adapter)
}

func TestDiscoverySimulationExecAdapter(t *testing.T) {
	baseDir, err := ioutil.TempDir("", "swarm-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(baseDir)
	testDiscoverySimulation(t, adapters.NewExecAdapter(baseDir))
}

func TestDiscoverySimulationSimAdapter(t *testing.T) {
	testDiscoverySimulation(t, adapters.NewSimAdapter(services))
}

func testDiscoverySimulation(t *testing.T, adapter adapters.NodeAdapter) {
	// create 10 node network
	nodeCount := 10
	net := simulations.NewNetwork(adapter, &simulations.NetworkConfig{
		Id:             "0",
		DefaultService: serviceName,
	})
	defer net.Shutdown()
	trigger := make(chan *adapters.NodeId)
	ids := make([]*adapters.NodeId, nodeCount)
	for i := 0; i < nodeCount; i++ {
		node, err := net.NewNode()
		if err != nil {
			t.Fatalf("error starting node: %s", err)
		}
		if err := net.Start(node.ID()); err != nil {
			t.Fatalf("error starting node %s: %s", node.ID().Label(), err)
		}
		if err := triggerChecks(trigger, net, node.ID()); err != nil {
			t.Fatal("error triggering checks for node %s: %s", node.ID().Label(), err)
		}
		ids[i] = node.ID()
	}

	// run a simulation which connects the 10 nodes in a ring and waits
	// for full peer discovery
	action := func(ctx context.Context) error {
		for i, id := range ids {
			var peerId *adapters.NodeId
			if i == 0 {
				peerId = ids[len(ids)-1]
			} else {
				peerId = ids[i-1]
			}
			if err := net.Connect(id, peerId); err != nil {
				return err
			}
		}
		return nil
	}
	nnmap := network.NewPeerPot(testMinProxBinSize, ids...)
	check := func(ctx context.Context, id *adapters.NodeId) (bool, error) {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		default:
		}

		node := net.GetNode(id)
		if node == nil {
			return false, fmt.Errorf("unknown node: %s", id)
		}
		client, err := node.Client()
		if err != nil {
			return false, fmt.Errorf("error getting node client: %s", err)
		}
		var healthy bool
		if err := client.Call(&healthy, "hive_healthy", nnmap[id.NodeID]); err != nil {
			return false, fmt.Errorf("error getting node health: %s", err)
		}
		return healthy, nil
	}

	timeout := 30 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	result := simulations.NewSimulation(net).Run(ctx, &simulations.Step{
		Action:  action,
		Trigger: trigger,
		Expect: &simulations.Expectation{
			Nodes: ids,
			Check: check,
		},
	})
	if result.Error != nil {
		t.Fatalf("simulation failed: %s", result.Error)
	}

	t.Log("Simulation Passed:")
	t.Logf("Duration: %s", result.FinishedAt.Sub(result.StartedAt))
	for _, id := range ids {
		t.Logf("Node %s passed in %s", id.Label(), result.Passes[id].Sub(result.StartedAt))
	}
	t.Logf("Events:")
	for _, event := range result.NetworkEvents {
		t.Log(event)
	}
}

// triggerChecks triggers a simulation step check whenever a peer is added or
// removed from the given node
func triggerChecks(trigger chan *adapters.NodeId, net *simulations.Network, id *adapters.NodeId) error {
	node := net.GetNode(id)
	if node == nil {
		return fmt.Errorf("unknown node: %s", id)
	}
	client, err := node.Client()
	if err != nil {
		return err
	}
	events := make(chan *p2p.PeerEvent)
	sub, err := client.Subscribe(context.Background(), "admin", events, "peerEvents")
	if err != nil {
		return fmt.Errorf("error getting peer events for node %v: %s", id, err)
	}
	go func() {
		defer sub.Unsubscribe()
		for {
			select {
			case <-events:
				trigger <- id
			case err := <-sub.Err():
				if err != nil {
					log.Error(fmt.Sprintf("error getting peer events for node %v", id), "err", err)
				}
				return
			}
		}
	}()
	return nil
}

func newService(id *adapters.NodeId, snapshot []byte) node.Service {
	addr := network.NewAddrFromNodeId(id)

	kp := network.NewKadParams()
	kp.MinProxBinSize = testMinProxBinSize
	kp.MaxBinSize = 3
	kp.MinBinSize = 1
	kp.MaxRetries = 1000
	kp.RetryExponent = 2
	kp.RetryInterval = 1000000
	kad := network.NewKademlia(addr.Over(), kp)

	hp := network.NewHiveParams()
	hp.KeepAliveInterval = time.Second

	config := &network.BzzConfig{
		OverlayAddr:  addr.Over(),
		UnderlayAddr: addr.Under(),
		HiveParams:   hp,
	}

	return network.NewBzz(config, kad, nil)
}
