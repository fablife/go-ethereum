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

// Package simulations simulates p2p networks.
// A mokcer simulates starting and stopping real nodes in a network.
package simulations

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"
)

func TestMocker(t *testing.T) {
	//start the simulation HTTP server
	_, s := testHTTPServer(t)
	defer s.Close()

	//create a client
	client := NewClient(s.URL)

	//start the network
	err := client.StartNetwork()
	if err != nil {
		t.Fatalf("Could not start test network: %s", err)
	}

	//get the list of available mocker types
	resp, err := http.Get(s.URL + "/mocker")
	if err != nil {
		t.Fatalf("Could not get mocker list: %s", err)
	}

	if resp.StatusCode != 200 {
		t.Fatalf("Invalid Status Code received, expected 200, got %d", resp.StatusCode)
	}

	//check the list is at least 1 in size
	mockerlist := make([]string, 10)
	json.NewDecoder(resp.Body).Decode(&mockerlist)
	if len(mockerlist) < 1 {
		t.Fatalf("No mockers available", resp.StatusCode)
	}
	resp.Body.Close()

	nodeCount := 10

	//start the mocker with nodeCount number of nodes, take the last element of the mockerlist as the mocker-type
	_, err = http.PostForm(s.URL+"/mocker/start", url.Values{"mocker-type": {mockerlist[len(mockerlist)-1]}, "node-count": {strconv.Itoa(nodeCount)}})
	if err != nil {
		t.Fatalf("Could not start mocker: %s", err)
	}

	//wait until all nodes are started and connected
	time.Sleep(3 * time.Second)

	//check there are nodeCount number of nodes in the network
	nodes_info, err := client.GetNodes()
	if err != nil {
		t.Fatalf("Could not get nodes list: %s", err)
	}

	if len(nodes_info) != nodeCount {
		t.Fatalf("Expected %d number of nodes, got: %d", nodeCount, len(nodes_info))
	}

	//stop the mocker
	_, err = http.Post(s.URL+"/mocker/stop", "", nil)
	if err != nil {
		t.Fatalf("Could not stop mocker: %s", err)
	}

	//reset the network
	_, err = http.Post(s.URL+"/reset", "", nil)
	if err != nil {
		t.Fatalf("Could not reset network: %s", err)
	}

	//now the number of nodes in the network should be zero
	nodes_info, err = client.GetNodes()
	if err != nil {
		t.Fatalf("Could not get nodes list: %s", err)
	}

	if len(nodes_info) != 0 {
		t.Fatalf("Expected empty list of nodes, got: %d", len(nodes_info))
	}

	//stop the network to terminate
	err = client.StopNetwork()
	if err != nil {
		t.Fatalf("Could not stop test network: %s", err)
	}
}
