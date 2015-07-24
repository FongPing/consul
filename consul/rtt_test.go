package consul

import (
	"math"
	"net/rpc"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/consul/consul/structs"
	"github.com/hashicorp/consul/testutil"
	"github.com/hashicorp/serf/coordinate"
)

// generateCoordinate creates a new coordinate with the given distance from the
// origin.
func generateCoordinate(rtt time.Duration) *coordinate.Coordinate {
	coord := coordinate.NewCoordinate(coordinate.DefaultConfig())
	coord.Vec[0] = rtt.Seconds()
	return coord
}

// verifyNodeSort makes sure the order of the nodes in the slice is the same as
// the expected order, expressed as a comma-separated string.
func verifyNodeSort(t *testing.T, nodes structs.Nodes, expected string) {
	vec := make([]string, len(nodes))
	for i, node := range nodes {
		vec[i] = node.Node
	}
	actual := strings.Join(vec, ",")
	if actual != expected {
		t.Fatalf("bad sort: %s != %s", actual, expected)
	}
}

// verifyServiceNodeSort makes sure the order of the nodes in the slice is the
// same as the expected order, expressed as a comma-separated string.
func verifyServiceNodeSort(t *testing.T, nodes structs.ServiceNodes, expected string) {
	vec := make([]string, len(nodes))
	for i, node := range nodes {
		vec[i] = node.Node
	}
	actual := strings.Join(vec, ",")
	if actual != expected {
		t.Fatalf("bad sort: %s != %s", actual, expected)
	}
}

// seedCoordinates uses the client to set up a set of nodes with a specific
// set of distances from the origin. We also include the server so that we
// can wait for the coordinates to get committed to the Raft log.
//
// Here's the layout of the nodes:
//
//       node3 node2 node5                         node4       node1
//   |     |     |     |     |     |     |     |     |     |     |
//   0     1     2     3     4     5     6     7     8     9     10  (ms)
//
func seedCoordinates(t *testing.T, client *rpc.Client, server *Server) {
	updates := []structs.CoordinateUpdateRequest{
		structs.CoordinateUpdateRequest{
			Datacenter: "dc1",
			Node:       "node1",
			Coord:      generateCoordinate(10 * time.Millisecond),
		},
		structs.CoordinateUpdateRequest{
			Datacenter: "dc1",
			Node:       "node2",
			Coord:      generateCoordinate(2 * time.Millisecond),
		},
		structs.CoordinateUpdateRequest{
			Datacenter: "dc1",
			Node:       "node3",
			Coord:      generateCoordinate(1 * time.Millisecond),
		},
		structs.CoordinateUpdateRequest{
			Datacenter: "dc1",
			Node:       "node4",
			Coord:      generateCoordinate(8 * time.Millisecond),
		},
		structs.CoordinateUpdateRequest{
			Datacenter: "dc1",
			Node:       "node5",
			Coord:      generateCoordinate(3 * time.Millisecond),
		},
	}

	// Apply the updates and wait a while for the batch to get committed to
	// the Raft log.
	for _, update := range updates {
		var out struct{}
		if err := client.Call("Coordinate.Update", &update, &out); err != nil {
			t.Fatalf("err: %v", err)
		}
	}
	time.Sleep(2 * server.config.CoordinateUpdatePeriod)
}

func TestRtt_sortNodesByDistanceFrom_Nodes(t *testing.T) {
	dir, server := testServer(t)
	defer os.RemoveAll(dir)
	defer server.Shutdown()

	client := rpcClient(t, server)
	defer client.Close()
	testutil.WaitForLeader(t, client.Call, "dc1")
	seedCoordinates(t, client, server)

	nodes := structs.Nodes{
		structs.Node{Node: "apple"},
		structs.Node{Node: "node1"},
		structs.Node{Node: "node2"},
		structs.Node{Node: "node3"},
		structs.Node{Node: "node4"},
		structs.Node{Node: "node5"},
	}

	// The zero value for the source should not trigger any sorting.
	var source structs.QuerySource
	if err := server.sortNodesByDistanceFrom(source, nodes); err != nil {
		t.Fatalf("err: %v", err)
	}
	verifyNodeSort(t, nodes, "apple,node1,node2,node3,node4,node5")

	// Same for a source in some other DC.
	source.Node = "node1"
	source.Datacenter = "dc2"
	if err := server.sortNodesByDistanceFrom(source, nodes); err != nil {
		t.Fatalf("err: %v", err)
	}
	verifyNodeSort(t, nodes, "apple,node1,node2,node3,node4,node5")

	// Same for a source node in our DC that we have no coordinate for.
	source.Node = "apple"
	source.Datacenter = "dc1"
	if err := server.sortNodesByDistanceFrom(source, nodes); err != nil {
		t.Fatalf("err: %v", err)
	}
	verifyNodeSort(t, nodes, "apple,node1,node2,node3,node4,node5")

	// Now sort relative to node1, note that apple doesn't have any
	// seeded coordinate info so it should end up at the end, despite
	// its lexical hegemony.
	source.Node = "node1"
	source.Datacenter = "dc1"
	if err := server.sortNodesByDistanceFrom(source, nodes); err != nil {
		t.Fatalf("err: %v", err)
	}
	verifyNodeSort(t, nodes, "node1,node4,node5,node2,node3,apple")

	// Try another sort from node2. Note that node5 and node3 are the
	// same distance away so the stable sort should preserve the order
	// they were in from the previous sort.
	source.Node = "node2"
	source.Datacenter = "dc1"
	if err := server.sortNodesByDistanceFrom(source, nodes); err != nil {
		t.Fatalf("err: %v", err)
	}
	verifyNodeSort(t, nodes, "node2,node5,node3,node4,node1,apple")

	// Let's exercise the stable sort explicitly to make sure we didn't
	// just get lucky.
	nodes[1], nodes[2] = nodes[2], nodes[1]
	if err := server.sortNodesByDistanceFrom(source, nodes); err != nil {
		t.Fatalf("err: %v", err)
	}
	verifyNodeSort(t, nodes, "node2,node3,node5,node4,node1,apple")
}

func TestRtt_sortNodesByDistanceFrom_ServiceNodes(t *testing.T) {
	dir, server := testServer(t)
	defer os.RemoveAll(dir)
	defer server.Shutdown()

	client := rpcClient(t, server)
	defer client.Close()
	testutil.WaitForLeader(t, client.Call, "dc1")
	seedCoordinates(t, client, server)

	nodes := structs.ServiceNodes{
		structs.ServiceNode{Node: "apple"},
		structs.ServiceNode{Node: "node1"},
		structs.ServiceNode{Node: "node2"},
		structs.ServiceNode{Node: "node3"},
		structs.ServiceNode{Node: "node4"},
		structs.ServiceNode{Node: "node5"},
	}

	// The zero value for the source should not trigger any sorting.
	var source structs.QuerySource
	if err := server.sortNodesByDistanceFrom(source, nodes); err != nil {
		t.Fatalf("err: %v", err)
	}
	verifyServiceNodeSort(t, nodes, "apple,node1,node2,node3,node4,node5")

	// Same for a source in some other DC.
	source.Node = "node1"
	source.Datacenter = "dc2"
	if err := server.sortNodesByDistanceFrom(source, nodes); err != nil {
		t.Fatalf("err: %v", err)
	}
	verifyServiceNodeSort(t, nodes, "apple,node1,node2,node3,node4,node5")

	// Same for a source node in our DC that we have no coordinate for.
	source.Node = "apple"
	source.Datacenter = "dc1"
	if err := server.sortNodesByDistanceFrom(source, nodes); err != nil {
		t.Fatalf("err: %v", err)
	}
	verifyServiceNodeSort(t, nodes, "apple,node1,node2,node3,node4,node5")

	// Now sort relative to node1, note that apple doesn't have any
	// seeded coordinate info so it should end up at the end, despite
	// its lexical hegemony.
	source.Node = "node1"
	source.Datacenter = "dc1"
	if err := server.sortNodesByDistanceFrom(source, nodes); err != nil {
		t.Fatalf("err: %v", err)
	}
	verifyServiceNodeSort(t, nodes, "node1,node4,node5,node2,node3,apple")

	// Try another sort from node2. Note that node5 and node3 are the
	// same distance away so the stable sort should preserve the order
	// they were in from the previous sort.
	source.Node = "node2"
	source.Datacenter = "dc1"
	if err := server.sortNodesByDistanceFrom(source, nodes); err != nil {
		t.Fatalf("err: %v", err)
	}
	verifyServiceNodeSort(t, nodes, "node2,node5,node3,node4,node1,apple")

	// Let's exercise the stable sort explicitly to make sure we didn't
	// just get lucky.
	nodes[1], nodes[2] = nodes[2], nodes[1]
	if err := server.sortNodesByDistanceFrom(source, nodes); err != nil {
		t.Fatalf("err: %v", err)
	}
	verifyServiceNodeSort(t, nodes, "node2,node3,node5,node4,node1,apple")
}

// mockNodeMap is keyed by node name and the values are the coordinates of the
// node.
type mockNodeMap map[string]*coordinate.Coordinate

// mockServer is used to provide a serfer interface for unit tests. The key is
// DC, which selects a map from node name to coordinate for that node.
type mockServer map[string]mockNodeMap

// newMockServer is used to generate a serfer interface that presents a known DC
// topology for unit tests. The server is in dc0.
//
// Here's the layout of the nodes:
//
//            /----   dc1         ----\         /-  dc2  -\ /-  dc0  -\
//             node2 node1       node3             node1       node1
//   |     |     |     |     |     |     |     |     |     |     |
//   0     1     2     3     4     5     6     7     8     9     10  (ms)
//
// We also include a node4 in dc1 with no known coordinate, as well as a
// mysterious dcX with no nodes with known coordinates.
//
func newMockServer() *mockServer {
	s := make(mockServer)
	s["dc0"] = mockNodeMap{
		"dc0.node1": generateCoordinate(10 * time.Millisecond),
	}
	s["dc1"] = mockNodeMap{
		"dc1.node1": generateCoordinate(3 * time.Millisecond),
		"dc1.node2": generateCoordinate(2 * time.Millisecond),
		"dc1.node3": generateCoordinate(5 * time.Millisecond),
		"dc1.node4": nil, // no known coordinate
	}
	s["dc2"] = mockNodeMap{
		"dc2.node1": generateCoordinate(8 * time.Millisecond),
	}
	s["dcX"] = mockNodeMap{
		"dcX.node1": nil, // no known coordinate
	}
	return &s
}

// See serfer.
func (s *mockServer) GetDatacenter() string {
	return "dc0"
}

// See serfer.
func (s *mockServer) GetCoordinate() (*coordinate.Coordinate, error) {
	return (*s)["dc0"]["dc0.node1"], nil
}

// See serfer.
func (s *mockServer) GetCachedCoordinate(node string) (*coordinate.Coordinate, bool) {
	for _, nodes := range *s {
		for n, coord := range nodes {
			if n == node && coord != nil {
				return coord, true
			}
		}
	}
	return nil, false
}

// See serfer.
func (s *mockServer) GetNodesForDatacenter(dc string) []string {
	nodes := make([]string, 0)
	if n, ok := (*s)[dc]; ok {
		for name := range n {
			nodes = append(nodes, name)
		}
	}
	return nodes
}

func TestRtt_getDatacenterDistance(t *testing.T) {
	s := newMockServer()

	// The serfer's own DC is always 0 ms away.
	if dist, err := getDatacenterDistance(s, "dc0"); err != nil || dist != 0.0 {
		t.Fatalf("bad: %v err: %v", dist, err)
	}

	// Check a DC with no coordinates, which should give positive infinity.
	if dist, err := getDatacenterDistance(s, "dcX"); err != nil || dist != math.Inf(1.0) {
		t.Fatalf("bad: %v err: %v", dist, err)
	}

	// Similar for a totally unknown DC.
	if dist, err := getDatacenterDistance(s, "acdc"); err != nil || dist != math.Inf(1.0) {
		t.Fatalf("bad: %v err: %v", dist, err)
	}

	// Check the trivial median case (just one node).
	if dist, err := getDatacenterDistance(s, "dc2"); err != nil || dist != 0.002 {
		t.Fatalf("bad: %v err: %v", dist, err)
	}

	// Check the more interesting median case, note that there's a mystery
	// node4 in there that should be excluded to make the distances sort
	// like this:
	//
	// [0] node3 (0.005), [1] node1 (0.007), [2] node2 (0.008)
	//
	// So the median should be at index 3 / 2 = 1 -> 0.007.
	if dist, err := getDatacenterDistance(s, "dc1"); err != nil || dist != 0.007 {
		t.Fatalf("bad: %v err: %v", dist, err)
	}
}

func TestRtt_sortDatacentersByDistance(t *testing.T) {
	s := newMockServer()

	dcs := []string{"acdc", "dc0", "dc1", "dc2", "dcX"}
	if err := sortDatacentersByDistance(s, dcs); err != nil {
		t.Fatalf("err: %v", err)
	}

	expected := "dc0,dc2,dc1,acdc,dcX"
	if actual := strings.Join(dcs, ","); actual != expected {
		t.Fatalf("bad sort: %s != %s", actual, expected)
	}

	// Make sure the sort is stable and we didn't just get lucky.
	dcs = []string{"dcX", "dc0", "dc1", "dc2", "acdc"}
	if err := sortDatacentersByDistance(s, dcs); err != nil {
		t.Fatalf("err: %v", err)
	}

	expected = "dc0,dc2,dc1,dcX,acdc"
	if actual := strings.Join(dcs, ","); actual != expected {
		t.Fatalf("bad sort: %s != %s", actual, expected)
	}
}
