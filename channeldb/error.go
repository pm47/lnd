package channeldb

import "fmt"

var (
	// ErrNoChanDBExists is returned when a channel bucket hasn't been
	// created.
	ErrNoChanDBExists = fmt.Errorf("channel db has not yet been created")

	// ErrLinkNodesNotFound is returned when node info bucket hasn't been
	// created.
	ErrLinkNodesNotFound = fmt.Errorf("no link nodes exist")

	// ErrNoActiveChannels  is returned when there is no active (open)
	// channels within the database.
	ErrNoActiveChannels = fmt.Errorf("no active channels exist")

	// ErrNoPastDeltas is returned when the channel delta bucket hasn't been
	// created.
	ErrNoPastDeltas = fmt.Errorf("channel has no recorded deltas")

	// ErrInvoiceNotFound is returned when a targeted invoice can't be
	// found.
	ErrInvoiceNotFound = fmt.Errorf("unable to locate invoice")

	// ErrNoInvoicesCreated is returned when we don't have invoices in
	// our database to return.
	ErrNoInvoicesCreated = fmt.Errorf("there are no existing invoices")

	// ErrDuplicateInvoice is returned when an invoice with the target
	// payment hash already exists.
	ErrDuplicateInvoice = fmt.Errorf("invoice with payment hash already exists")

	// ErrNoPaymentsCreated is returned when bucket of payments hasn't been
	// created.
	ErrNoPaymentsCreated = fmt.Errorf("there are no existing payments")

	// ErrNodeNotFound is returned when node bucket exists, but node with
	// specific identity can't be found.
	ErrNodeNotFound = fmt.Errorf("link node with target identity not found")

	// ErrMetaNotFound is returned when meta bucket hasn't been
	// created.
	ErrMetaNotFound = fmt.Errorf("unable to locate meta information")

	// ErrGraphNotFound is returned when at least one of the components of
	// graph doesn't exist.
	ErrGraphNotFound = fmt.Errorf("graph bucket not initialized")

	// ErrGraphNeverPruned is returned when graph was never pruned.
	ErrGraphNeverPruned = fmt.Errorf("graph never pruned")

	// ErrSourceNodeNotSet is returned if the the source node of the graph
	// hasn't been added The source node is the center node within a
	// star-graph.
	ErrSourceNodeNotSet = fmt.Errorf("source node does not exist")

	// ErrGraphNodesNotFound is returned in case none of the nodes has
	// been added in graph node bucket.
	ErrGraphNodesNotFound = fmt.Errorf("no graph nodes exist")

	// ErrGraphNoEdgesFound is returned in case of none of the channel/edges
	// has been added in graph edge bucket.
	ErrGraphNoEdgesFound = fmt.Errorf("no graph edges exist")

	// ErrGraphNodeNotFound is returned when we're unable to find the target
	// node.
	ErrGraphNodeNotFound = fmt.Errorf("unable to find node")

	// ErrEdgeNotFound is returned when an edge for the target chanID
	// can't be found.
	ErrEdgeNotFound = fmt.Errorf("edge for chanID not found")

	// ErrNodeAliasNotFound is returned when alias for node can't be found.
	ErrNodeAliasNotFound = fmt.Errorf("alias for node not found")
)
