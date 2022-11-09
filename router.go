package noise

// table assoc Socket with peer.
type table map[ID]*peer

// Add new peer to table.
func (t table) Add(peer *peer) {
	t[peer.ID()] = peer
}

// Get peer from table.
func (t table) Get(id ID) *peer {
	// exist socket related peer in table?
	if peer, ok := t[id]; ok {
		return peer
	}

	return nil
}

// Remove peer from table.
func (t table) Remove(peer *peer) {
	delete(t, peer.ID())
}

// router keep a hash table to associate ID with peer.
// It implements a unstructured mesh topology.
type router struct {
	table table
}

func newRouter() *router {
	return &router{make(table)}
}

// Table return current routing table.
func (r *router) Table() table {
	return r.table
}

// Query return connection interface based on socket parameter.
func (r *router) Query(id ID) *peer {
	// exist socket related peer?
	return r.table.Get(id)
}

// Add create new Socket Peer association.
func (r *router) Add(peer *peer) {
	r.table.Add(peer)
}

// Len return the number of routed connections.
func (r *router) Len() uint8 {
	// 255 max peers len supported
	// uint8 is enough for routing peers len
	return uint8(len(r.table))
}

// Flush clean table and return total peers removed.
// This will be garbage collected eventually.
func (r *router) Flush() uint8 {
	size := r.Len()
	// nil its a valid type for mapping since its a reference type.
	// ref: https://github.com/go101/go101/wiki/About-the-terminology-%22reference-type%22-in-Go
	r.table = nil
	return size

}

// Remove removes a connection from router.
// It return recently removed peer.
func (r *router) Remove(peer *peer) {
	r.table.Remove(peer)
}
