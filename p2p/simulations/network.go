// Package simulations simulates p2p networks.
//
// Network
//  - has nodes
//  - has connections
//  - has triggers (input eventer, triggers things like start and stop of nodes, connecting them)
//  - has output eventer, where stuff that happens during simulation is sent
//  - the adapter of new nodes is assigned by the Node Adapter Function.
//
// Sources of Trigger events
// - UI (click of button)
// - Journal (replay captured events)
// - Mocker (generate random events)
//
// Adapters
// - each node has an adapter
// - contains methods to connect to another node using the same adapter type
// - models communication too (sending and receiving messages)
//
// REST API
// - Session Controller: handles Networks
// - Network Controller
//   - handles one Network
//   - has sub controller for triggering events
//   - get output events
//
package simulations

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sync"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/adapters"
	"github.com/ethereum/go-ethereum/p2p/discover"
	"github.com/ethereum/go-ethereum/rpc"
)

type NetworkConfig struct {
	// Type   NetworkType
	// Config json.RawMessage // type-specific configs
	// type
	// Events []string
	Id                  string
	DefaultMockerConfig *MockerConfig
	Backend             bool
}

type NetworkControl interface {
	Events() *event.TypeMux
	Config() *NetworkConfig
	Subscribe(*event.TypeMux, ...interface{})
}

// event types related to connectivity, i.e., nodes coming on dropping off
// and connections established and dropped
var ConnectivityControlEvents = []interface{}{&NodeControlEvent{}, &ConnControlEvent{}, &MsgControlEvent{}}
var ConnectivityLiveEvents = []interface{}{&NodeEvent{}, &ConnEvent{}, &MsgEvent{}}
var ConnectivityAllEvents = append(ConnectivityControlEvents, ConnectivityLiveEvents...)

type NetworkController struct {
	*ResourceController

	events *event.TypeMux
}

// ServeStream subscribes to network events and sends them to the client as a
// stream of Server-Sent-Events, with each event being a JSON encoded SimUpdate
// object
func (n *NetworkController) ServeStream(w http.ResponseWriter, req *http.Request) {
	sub := n.events.Subscribe(ConnectivityAllEvents...)
	defer sub.Unsubscribe()

	// stop the stream if the client goes away
	var clientGone <-chan bool
	if cn, ok := w.(http.CloseNotifier); ok {
		clientGone = cn.CloseNotify()
	}

	// write writes the given event and data to the stream like:
	//
	// event: <event>
	// data: <data>
	//
	write := func(event, data string) {
		fmt.Fprintf(w, "event: %s\n", event)
		fmt.Fprintf(w, "data: %s\n\n", data)
		if fw, ok := w.(http.Flusher); ok {
			fw.Flush()
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	ch := sub.Chan()
	for {
		select {
		case event := <-ch:
			// convert the event to a SimUpdate
			update, err := NewSimUpdate(event)
			if err != nil {
				write("error", err.Error())
				return
			}
			data, err := json.Marshal(update)
			if err != nil {
				write("error", err.Error())
				return
			}
			write("simupdate", string(data))
		case <-clientGone:
			return
		}
	}
}

// NewNetworkController creates a ResourceController responding to GET and DELETE methods
// it embeds a mockers controller, a journal player, node and connection contollers.
//
// Events from the eventer go into the provided journal. The content of the journal can be
// accessed through the HTTP API.
// the events are generated by mockers and replayed journals
func NewNetworkController(net NetworkControl) Controller {
	journal := NewJournal()
	eventer := &event.TypeMux{}
	conf := net.Config()
	if conf.Backend {
		journal.Subscribe(net.Events(), ConnectivityAllEvents...)
		// the network can subscribe to the eventer fed by mockers and players
		net.Subscribe(eventer, ConnectivityAllEvents...)
	} else {
		// alternatively  mocked and replayed events bypass the simulation network backend
		journal.Subscribe(eventer, ConnectivityAllEvents...)
	}
  network := net.(*Network)
  mocker:= NewNodeMocker(network)
  var empty = struct{}{}
	self  := NewResourceContoller(
		&ResourceHandlers{
			// PUT /<networkId>/
			Update: &ResourceHandler {
				Handle: func(msg interface{}, parent *ResourceController) (interface{}, error) {
					_, ok := msg.(*SimConfig)
					if ok {
						  mocker.RunSimulation()
              return empty, nil
					}
          return nil, fmt.Errorf("invalid json body: expected SimConfig")
        },
				Type: reflect.TypeOf(&SimConfig{}),
      },
			// GET /<networkId>/
			Retrieve: &ResourceHandler{
				Handle: func(msg interface{}, parent *ResourceController) (interface{}, error) {
					log.Trace(fmt.Sprintf("msg: %v", msg))
					simConfig, ok := msg.(*SimConfig)
					if ok {
						return UpdateSim(simConfig, journal)
					}
					snapshotConfig, ok := msg.(*SnapshotConfig)
					if ok {
						return Snapshot(snapshotConfig, journal)
					}
					return nil, fmt.Errorf("invalid json body: must be SimConfig or SnapshotConfig")
				},
				Type: reflect.TypeOf(&SimConfig{}),
			},
			// DELETE /<networkId>/
			Destroy: &ResourceHandler{
				Handle: func(msg interface{}, parent *ResourceController) (interface{}, error) {
					parent.DeleteResource(conf.Id)
					return nil, nil
				},
			},
		},
	)

	self.SetResource("nodes", NewNodesController(network, mocker))
	self.SetResource("debug", NewDebugController(journal))
	self.SetResource("mockevents", NewMockersController(eventer, conf.DefaultMockerConfig))
	self.SetResource("journals", NewJournalPlayersController(eventer))

	return &NetworkController{ResourceController: self, events: net.Events()}
}

func NewNodesController(net *Network, mocker *NodeMocker) *ResourceController {
  ids     := mocker.CreateMockNodes(net.Config().DefaultMockerConfig.NodeCount)
  net.init(ids)
	nc      := NewResourceContoller(
		&ResourceHandlers{
			//GET /<networkId>/nodes  -- returns all nodes' kademlia table
			Retrieve: &ResourceHandler{
				Handle: func(msg interface{}, parent *ResourceController) (interface{}, error) {
					var results []string
					for _, id := range ids {
						//pp := net.hives[id.NodeID]
            pp := net.GetNodeAdapter(id)
						results = append(results, pp.Info())
					}
					return results, nil
				},
				//Type: reflect.TypeOf([]string{}), // this is input not output param structure
			},
		})

	for _, id := range ids {
		idc := NewResourceContoller(
			&ResourceHandlers{
				//GET /<networkId>/nodes/<nodeId>  -- returns <nodeId>'s kademlia table
				Retrieve: &ResourceHandler{
					Handle: func(msg interface{}, parent *ResourceController) (interface{}, error) {
						//pp := net.hives[id.NodeID]
            pp := net.GetNodeAdapter(id)
						if pp != nil {
							return pp.Info(), nil
						}
						//this shouldn't happen anymore, but just in case
						return nil, fmt.Errorf("Node not found")
					},
					//Type: reflect.TypeOf([]string{}), // this is input not output param structure
				},
			})
		nc.SetResource(id.String(), idc)
	}
  return nc
}

func NewDefaultNodesController(net *Network) *ResourceController {
	return NewResourceContoller(
		&ResourceHandlers{
			Create: &ResourceHandler{
				Handle: func(msg interface{}, parent *ResourceController) (interface{}, error) {
					return net.NewNode()
				},
			},
			Retrieve: &ResourceHandler{
				Handle: func(msg interface{}, parent *ResourceController) (interface{}, error) {
					ids := msg.([]string)
					var result []interface{}
					for _, id := range ids {
						node := net.GetNode(adapters.NewNodeIdFromHex(id))
						if node != nil {
							result = append(result, interface{}(node.String()))
						}
					}
					return result, nil
				},
				Type: reflect.TypeOf([]string{}), // this is input not output param structure
			},
			Update: &ResourceHandler{
				Handle: func(msg interface{}, parent *ResourceController) (interface{}, error) {
					args := msg.(*NodeIF)
					oneId := adapters.NewNodeIdFromHex(args.One)
					if len(args.Other) == 0 {
						if net.Start(oneId) != nil {
							net.Stop(oneId)
						}
						return empty, nil
					} else {
						otherId := adapters.NewNodeIdFromHex(args.Other)
						err := net.Connect(oneId, otherId)
						return empty, err
					}
				},
				Type: reflect.TypeOf(&NodeIF{}), // this is input not output param structure
			},
		},
	)
}

func NewDebugController(journal *Journal) Controller {
	return NewResourceContoller(
		&ResourceHandlers{
			Create: &ResourceHandler{
				Handle: func(msg interface{}, parent *ResourceController) (interface{}, error) {
					journaldump := []string{}
					eventfmt := func(e *event.TypeMuxEvent) bool {
						journaldump = append(journaldump, fmt.Sprintf("%v", e))
						return true
					}
					journal.Read(eventfmt)
					return struct{ Results []string }{Results: journaldump}, nil
				},
			},
		},
	)
}

// Network models a p2p network
// the actual logic of bringing nodes and connections up and down and
// messaging is implemented in the particular NodeAdapter interface
type Network struct {
	// input trigger events and other events
	events  *event.TypeMux // generated events a journal can subsribe to
	lock    sync.RWMutex
	nodeMap map[discover.NodeID]int
	connMap map[string]int
	Nodes   []*Node `json:"nodes"`
	Conns   []*Conn `json:"conns"`
	quitc   chan bool
	conf    *NetworkConfig
	//
	// adapters.Messenger
	// node adapter function that creates the node model for
	// the particular type of network from a config
	naf func(*NodeConfig) adapters.NodeAdapter
  init func(ids []*adapters.NodeId)
}

func NewNetwork(conf *NetworkConfig) *Network {
	return &Network{
		conf:    conf,
		events:  &event.TypeMux{},
		nodeMap: make(map[discover.NodeID]int),
		connMap: make(map[string]int),
		quitc:   make(chan bool),
	}
}

func (self *Network) SetNaf(naf func(*NodeConfig) adapters.NodeAdapter) {
	self.naf = naf
}

func (self *Network) SetInit(init func([]*adapters.NodeId)) {
	self.init = init
}

// Subscribe takes an event.TypeMux and subscibes to types
// and launches a goroutine that reads control events from an eventer Subsription channel
// and executes the events
func (self *Network) Subscribe(eventer *event.TypeMux, types ...interface{}) {
	log.Info("network subscribe")
	sub := eventer.Subscribe(types...)
	go func() {
		defer sub.Unsubscribe()
		for {
			select {
			case ev := <-sub.Chan():
				self.execute(ev)
			case <-self.quitc:
				return
			}
		}
	}()
}

func (self *Network) executeNodeEvent(ne *NodeControlEvent) {
	if ne.Up {
		err := self.NewNodeWithConfig(&ne.Node.NodeConfig)
		if err != nil {
			log.Trace(fmt.Sprintf("error execute event %v: %v", ne, err))
		}
		err = self.Start(ne.Node.Id)
		if err != nil {
			log.Trace(fmt.Sprintf("error execute event %v: %v", ne, err))
		}
	} else {
		err := self.Stop(ne.Node.Id)
		if err != nil {
			log.Trace(fmt.Sprintf("error execute event %v: %v", ne, err))
		}
	}
	ne.Node.controlFired = ne.Up
}

func (self *Network) executeConnEvent(ce *ConnControlEvent) {
	if ce.Up {
		err := self.Connect(ce.Connection.One, ce.Connection.Other)
		if err != nil {
			log.Trace(fmt.Sprintf("error execute event %v: %v", ce, err))
		}
	} else {
		err := self.Disconnect(ce.Connection.One, ce.Connection.Other)
		if err != nil {
			log.Trace(fmt.Sprintf("error execute event %v: %v", ce, err))
		}
	}
	ce.Connection.controlFired = ce.Up
}

func (self *Network) execute(in *event.TypeMuxEvent) {
	log.Trace(fmt.Sprintf("execute event %v", in))
	ev := in.Data
	if ne, ok := ev.(*NodeEvent); ok {
		if ne.Up && ne.Node.controlFired || (!ne.Up && !ne.Node.controlFired) {
			log.Trace(fmt.Sprintf("Got NodeEvent %v, but Control Event has already been applied for : %v", ne, ne.Node))
			//ignore this real event; control event already took care of this
		} else {
			self.executeNodeEvent(ne.ToControlEvent())
		}
	} else if ce, ok := ev.(*ConnEvent); ok {
		if ce.Up && ce.Connection.controlFired || (!ce.Up && !ce.Connection.controlFired) {
			log.Trace(fmt.Sprintf("Got ConnEvent %v, but Control Event has already been applied for : %v", ce, ce.Connection))
			//ignore this real event; control event already took care of this
		} else {
			self.executeConnEvent(ce.ToControlEvent())
		}
	} else if ne, ok := ev.(*NodeControlEvent); ok {
		self.executeNodeEvent(ne)
	} else if ce, ok := ev.(*ConnControlEvent); ok {
		self.executeConnEvent(ce)
	} else {
		log.Trace(fmt.Sprintf("event: %#v", ev))
		panic("unhandled event")
	}
}

// Events returns the output eventer of the Network.
func (self *Network) Events() *event.TypeMux {
	return self.events
}

type EventType int

const (
	ControlEvent EventType = iota
	LiveEvent
)

type EventEmitter interface {
	EmitEvent()
}

type LiveEventer interface {
	ToControlEvent()
}

type Node struct {
	NodeConfig

	Up bool

	na           adapters.NodeAdapter
	controlFired bool
}

func (self *Node) String() string {
	return fmt.Sprintf("Node %v", self.Id.Label())
}

// active connections are represented by the Node entry object so that
// you journal updates could filter if passive knowledge about peers is
// irrelevant
type Conn struct {
	One        *adapters.NodeId `json:"one"`
	Other      *adapters.NodeId `json:"other"`
	one, other *Node
	// connection down by default
	Up bool `json:"up"`
	// reverse is false by default (One dialled/dropped the Other)
	Reverse bool `json:"reverse"`
	// Info
	// average throughput, recent average throughput etc
	controlFired bool
}

func (self *Conn) String() string {
	return fmt.Sprintf("Conn %v->%v", self.One.Label(), self.Other.Label())
}

type Msg struct {
	One          *adapters.NodeId `json:"one"`
	Other        *adapters.NodeId `json:"other"`
	Code         uint64           `json:"conn"`
	controlFired bool
}

func (self *Msg) String() string {
	return fmt.Sprintf("Msg(%d) %v->%v", self.Code, self.One.Label(), self.Other.Label())
}

type NodeEvent struct {
	Node *Node
	Up   bool
}

type ConnEvent struct {
	Connection *Conn
	Up         bool
	Reverse    bool
}

type MsgEvent struct {
	Message *Msg
}

type NodeControlEvent struct {
	*NodeEvent
}

type ConnControlEvent struct {
	*ConnEvent
}

type MsgControlEvent struct {
	*MsgEvent
}

func (self *NodeEvent) String() string {
	return fmt.Sprintf("<Up: %v, Data: %v>\n", self.Up, self.Node)
}

func (self *ConnEvent) String() string {
	return fmt.Sprintf("<Up: %v, Reverse: %v, Data: %v>\n", self.Up, self.Reverse, self.Connection)
}

func (self *MsgEvent) String() string {
	return fmt.Sprintf("<Msg: %v>\n", self.Message)
}

func (self *Node) EmitEvent(eventType EventType) interface{} {
	evt := &NodeEvent{
		Node: self,
		Up:   self.Up,
	}
	if eventType == ControlEvent {
		return &NodeControlEvent{
			evt,
		}
	} else {
		return evt
	}
}

func (self *Conn) EmitEvent(eventType EventType) interface{} {
	evt := &ConnEvent{
		Connection: self,
		Up:         self.Up,
		Reverse:    self.Reverse,
	}

	if eventType == ControlEvent {
		return &ConnControlEvent{
			evt,
		}
	} else {
		return evt
	}
}

func (self *Msg) EmitEvent(eventType EventType) interface{} {
	evt := &MsgEvent{
		Message: self,
	}
	if eventType == ControlEvent {
		return &MsgControlEvent{
			evt,
		}
	} else {
		return evt
	}
}

func (self *MsgEvent) ToControlEvent() *MsgControlEvent {
	return &MsgControlEvent{self}
}

func (self *ConnEvent) ToControlEvent() *ConnControlEvent {
	return &ConnControlEvent{self}
}

func (self *NodeEvent) ToControlEvent() *NodeControlEvent {
	return &NodeControlEvent{self}
}

func (self *Node) Adapter() adapters.NodeAdapter {
	return self.na
}

type NodeConfig struct {
	Id         *adapters.NodeId
	PrivateKey *ecdsa.PrivateKey
}

type nodeConfigJSON struct {
	Id         string `json:"id"`
	PrivateKey string `json:"private_key"`
}

func (n *NodeConfig) MarshalJSON() ([]byte, error) {
	return json.Marshal(nodeConfigJSON{
		n.Id.String(),
		hex.EncodeToString(crypto.FromECDSA(n.PrivateKey)),
	})
}

func (n *NodeConfig) UnmarshalJSON(data []byte) error {
	var confJSON nodeConfigJSON
	if err := json.Unmarshal(data, &confJSON); err != nil {
		return err
	}

	nodeID, err := discover.HexID(confJSON.Id)
	if err != nil {
		return err
	}
	n.Id = &adapters.NodeId{NodeID: nodeID}

	key, err := hex.DecodeString(confJSON.PrivateKey)
	if err != nil {
		return err
	}
	n.PrivateKey = crypto.ToECDSA(key)

	return nil
}

func RandomNodeConfig() *NodeConfig {
	key, err := crypto.GenerateKey()
	if err != nil {
		panic("unable to generate key")
	}
	var id discover.NodeID
	pubkey := crypto.FromECDSAPub(&key.PublicKey)
	copy(id[:], pubkey[1:])
	return &NodeConfig{
		Id:         &adapters.NodeId{NodeID: id},
		PrivateKey: key,
	}
}

// TODO: ignored for now
type QueryConfig struct {
	Format string // "sim.update", "journal",
}

type Know struct {
	Subject *adapters.NodeId `json:"subject"`
	Object  *adapters.NodeId `json:"object"`
	// Into
	// number of attempted connections
	// time of attempted connections
	// number of active connections during the session
	// number of active connections since records began
	// swap balance
}

// NewNode adds a new node to the network with a random ID
func (self *Network) NewNode() (*NodeConfig, error) {
	conf := RandomNodeConfig()
	if err := self.NewNodeWithConfig(conf); err != nil {
		return nil, err
	}
	return conf, nil
}

// NewNodeWithConfig adds a new node to the network with the given config
// errors if a node by the same id already exist
func (self *Network) NewNodeWithConfig(conf *NodeConfig) error {
	self.lock.Lock()
	defer self.lock.Unlock()
	id := conf.Id

	_, found := self.nodeMap[id.NodeID]
	if found {
		return fmt.Errorf("node %v already added", id)
	}
	self.nodeMap[id.NodeID] = len(self.Nodes)
	na := self.naf(conf)
	node := &Node{
		NodeConfig: *conf,
		na:         na,
	}
	self.Nodes = append(self.Nodes, node)
	log.Trace(fmt.Sprintf("node %v created", id))
	return nil
}

func (self *Network) Config() *NetworkConfig {
	return self.conf
}

// newConn adds a new connection to the network
// it errors if the respective nodes do not exist
func (self *Network) newConn(oneId, otherId *adapters.NodeId) (*Conn, error) {
	one := self.getNode(oneId)
	if one == nil {
		return nil, fmt.Errorf("one %v does not exist", one)
	}
	other := self.getNode(otherId)
	if other == nil {
		return nil, fmt.Errorf("other %v does not exist", other)
	}
	return &Conn{
		One:   oneId,
		Other: otherId,
		one:   one,
		other: other,
	}, nil
}

func (self *Conn) nodesUp() error {
	if !self.one.Up {
		return fmt.Errorf("one %v is not up", self.One)
	}
	if !self.other.Up {
		return fmt.Errorf("other %v is not up", self.Other)
	}
	return nil
}

// Start(id) starts up the node (relevant only for instance with own p2p or remote)
func (self *Network) Start(id *adapters.NodeId) error {
	node := self.GetNode(id)
	if node == nil {
		return fmt.Errorf("node %v does not exist", id)
	}
	if node.Up {
		return fmt.Errorf("node %v already up", id)
	}
	log.Trace(fmt.Sprintf("starting node %v: %v adapter %v", id, node.Up, node.Adapter()))
	if err := node.Adapter().Start(); err != nil {
		return err
	}
	node.Up = true
	log.Info(fmt.Sprintf("started node %v: %v", id, node.Up))

	self.events.Post(node.EmitEvent(ControlEvent))

	// subscribe to peer events
	client, err := node.Adapter().Client()
	if err != nil {
		return fmt.Errorf("error getting rpc client  for node %v: %s", id, err)
	}
	events := make(chan *p2p.PeerEvent)
	sub, err := client.EthSubscribe(context.Background(), events, "peerEvents")
	if err != nil {
		return fmt.Errorf("error getting peer events for node %v: %s", id, err)
	}
	go self.watchPeerEvents(id, events, sub)
	return nil
}

func (self *Network) watchPeerEvents(id *adapters.NodeId, events chan *p2p.PeerEvent, sub event.Subscription) {
	defer sub.Unsubscribe()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return
			}
			peer := &adapters.NodeId{NodeID: event.Peer}
			switch event.Type {
			case p2p.PeerEventTypeAdd:
				if err := self.DidConnect(id, peer); err != nil {
					log.Error(fmt.Sprintf("error generating connection up event %s => %s", id.Label(), peer.Label()), "err", err)
				}
			case p2p.PeerEventTypeDrop:
				if err := self.DidDisconnect(id, peer); err != nil {
					log.Error(fmt.Sprintf("error generating connection down event %s => %s", id.Label(), peer.Label()), "err", err)
				}
			}
		case err := <-sub.Err():
			if err != nil {
				log.Error(fmt.Sprintf("error getting peer events for node %v", id), "err", err)
			}
			return
		}
	}
}

// Stop(id) shuts down the node (relevant only for instance with own p2p or remote)
func (self *Network) Stop(id *adapters.NodeId) error {
	node := self.GetNode(id)
	if node == nil {
		return fmt.Errorf("node %v does not exist", id)
	}
	if !node.Up {
		return fmt.Errorf("node %v already down", id)
	}
	if err := node.Adapter().Stop(); err != nil {
		return err
	}
	node.Up = false
	log.Info(fmt.Sprintf("stop node %v: %v", id, node.Up))

	self.events.Post(node.EmitEvent(ControlEvent))
	return nil
}

// Connect(i, j) attempts to connect nodes i and j (args given as nodeId)
// calling the node's nodadapters Connect method
// connection is established (as if) the first node dials out to the other
func (self *Network) Connect(oneId, otherId *adapters.NodeId) error {
	log.Debug(fmt.Sprintf("connecting %s to %s", oneId, otherId))
	conn, err := self.GetOrCreateConn(oneId, otherId)
	if err != nil {
		return err
	}
	if conn.Up {
		return fmt.Errorf("%v and %v already connected", oneId, otherId)
	}
	err = conn.nodesUp()
	if err != nil {
		return err
	}
	var rev bool
	if conn.One.NodeID != oneId.NodeID {
		rev = true
	}
	// if Connect is called because of external trigger, it needs to call
	// the actual adaptor's connect method
	// any other way of connection (like peerpool) will need to call back
	// to this method with connect = false to avoid infinite recursion
	// this is not relevant for nodes starting up (which can only be externally triggered)
	var addr []byte
	var client *rpc.Client
	if rev {
		addr = conn.one.na.Addr()
		client, err = conn.other.na.Client()
	} else {
		addr = conn.other.na.Addr()
		client, err = conn.one.na.Client()
	}
	if err != nil {
		return err
	}
	self.events.Post(conn.EmitEvent(ControlEvent))
	return client.Call(nil, "admin_addPeer", string(addr))
}

// Disconnect(i, j) attempts to disconnect nodes i and j (args given as nodeId)
// calling the node's nodadapters Disconnect method
// sets the Conn model to Down
// the disconnect will be initiated (the connection is dropped by) the first node
// it errors if either of the nodes is down (or does not exist)
func (self *Network) Disconnect(oneId, otherId *adapters.NodeId) error {
	conn := self.GetConn(oneId, otherId)
	if conn == nil {
		return fmt.Errorf("connection between %v and %v does not exist", oneId, otherId)
	}
	if !conn.Up {
		return fmt.Errorf("%v and %v already disconnected", oneId, otherId)
	}
	var rev bool
	if conn.One.NodeID != oneId.NodeID {
		rev = true
	}
	var addr []byte
	var client *rpc.Client
	var err error
	if rev {
		addr = conn.one.na.Addr()
		client, err = conn.other.na.Client()
	} else {
		addr = conn.other.na.Addr()
		client, err = conn.one.na.Client()
	}
	if err != nil {
		return err
	}
	self.events.Post(conn.EmitEvent(ControlEvent))
	return client.Call(nil, "admin_removePeer", string(addr))
}

func (self *Network) DidConnect(one, other *adapters.NodeId) error {
	conn, err := self.GetOrCreateConn(one, other)
	if err != nil {
		return fmt.Errorf("connection between %v and %v does not exist", one, other)
	}
	if conn.Up {
		return fmt.Errorf("%v and %v already connected", one, other)
	}
	conn.Reverse = conn.One.NodeID != one.NodeID
	conn.Up = true
	// connection event posted
	self.events.Post(conn.EmitEvent(LiveEvent))
	return nil
}

func (self *Network) DidDisconnect(one, other *adapters.NodeId) error {
	conn, err := self.GetOrCreateConn(one, other)
	if err != nil {
		return fmt.Errorf("connection between %v and %v does not exist", one, other)
	}
	if !conn.Up {
		return fmt.Errorf("%v and %v already disconnected", one, other)
	}
	conn.Reverse = conn.One.NodeID != one.NodeID
	conn.Up = false
	self.events.Post(conn.EmitEvent(LiveEvent))
	return nil
}

// Send(senderid, receiverid) sends a message from one node to another
func (self *Network) Send(senderid, receiverid *adapters.NodeId, msgcode uint64, protomsg interface{}) {
	msg := &Msg{
		One:   senderid,
		Other: receiverid,
		Code:  msgcode,
	}
	//self.GetNode(senderid).na.(*adapters.SimNode).GetPeer(receiverid).SendMsg(msgcode, protomsg) // phew!
	self.events.Post(msg.EmitEvent(ControlEvent))
}

// GetNodeAdapter(id) returns the NodeAdapter for node with id
// returns nil if node does not exist
func (self *Network) GetNodeAdapter(id *adapters.NodeId) adapters.NodeAdapter {
	self.lock.Lock()
	defer self.lock.Unlock()
	node := self.getNode(id)
	if node == nil {
		return nil
	}
	return node.na
}

// GetNode retrieves the node model for the id given as arg
// returns nil if the node does not exist
func (self *Network) GetNode(id *adapters.NodeId) *Node {
	self.lock.Lock()
	defer self.lock.Unlock()
	return self.getNode(id)
}

func (self *Network) getNode(id *adapters.NodeId) *Node {
	i, found := self.nodeMap[id.NodeID]
	if !found {
		return nil
	}
	return self.Nodes[i]
}

// GetConn(i, j) retrieves the connectiton model for the connection between
// the order of nodes does not matter, i.e., GetConn(i,j) == GetConn(j, i)
// returns nil if the node does not exist
func (self *Network) GetConn(oneId, otherId *adapters.NodeId) *Conn {
	self.lock.Lock()
	defer self.lock.Unlock()
	return self.getConn(oneId, otherId)
}

// GetConn(i, j) retrieves the connectiton model for the connection between
// i and j, or creates a new one if it does not exist
// the order of nodes does not matter, i.e., GetConn(i,j) == GetConn(j, i)
func (self *Network) GetOrCreateConn(oneId, otherId *adapters.NodeId) (*Conn, error) {
	self.lock.Lock()
	defer self.lock.Unlock()
	conn := self.getConn(oneId, otherId)
	if conn != nil {
		return conn, nil
	}
	conn, err := self.newConn(oneId, otherId)
	if err != nil {
		return nil, err
	}
	label := ConnLabel(oneId, otherId)
	self.connMap[label] = len(self.Conns)
	self.Conns = append(self.Conns, conn)
	return conn, nil
}

func (self *Network) getConn(oneId, otherId *adapters.NodeId) *Conn {
	label := ConnLabel(oneId, otherId)
	i, found := self.connMap[label]
	if !found {
		return nil
	}
	return self.Conns[i]
}

func (self *Network) Shutdown() {
	// disconnect all nodes
	for _, conn := range self.Conns {
		log.Debug(fmt.Sprintf("disconnecting %s from %s", conn.One.Label(), conn.Other.Label()))
		if err := self.Disconnect(conn.One, conn.Other); err != nil {
			log.Warn(fmt.Sprintf("error disconnecting %s from %s", conn.One.Label(), conn.Other.Label()), "err", err)
		}
	}

	// stop all nodes
	for _, node := range self.Nodes {
		log.Debug(fmt.Sprintf("stopping node %s", node.Id.Label()))
		if err := node.na.Stop(); err != nil {
			log.Warn(fmt.Sprintf("error stopping node %s", node.Id.Label()), "err", err)
		}
	}
}
