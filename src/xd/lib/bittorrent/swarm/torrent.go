package swarm

import (
	"bytes"
	"errors"
	"net"
	"sync"
	"time"
	"xd/lib/bittorrent"
	"xd/lib/bittorrent/extensions"
	"xd/lib/common"
	"xd/lib/dht"
	"xd/lib/log"
	"xd/lib/metainfo"
	"xd/lib/network"
	"xd/lib/storage"
	"xd/lib/tracker"
)

// single torrent tracked in a swarm
type Torrent struct {
	Completed      func()
	Started        func()
	Stopped        func()
	RemoveSelf     func()
	netacces       sync.Mutex
	suspended      bool
	netContext     network.Network
	Trackers       map[string]tracker.Announcer
	announcers     map[string]*torrentAnnounce
	announceMtx    sync.Mutex
	announceTicker *time.Ticker
	id             common.PeerID
	st             storage.Torrent
	obconns        map[string]*PeerConn
	ibconns        map[string]*PeerConn
	connMtx        sync.Mutex
	pt             *pieceTracker
	defaultOpts    *extensions.Message
	closing        bool
	started        bool
	MaxRequests    int
	pexState       *PEXSwarmState
	xdht           *dht.XDHT
}

func (t *Torrent) ObtainedNetwork(n network.Network) {
	t.netContext = n
	if t.suspended {
		t.suspended = false
		t.netacces.Unlock()
	}
	log.Debug("torrent obtained network")
}

func (t *Torrent) WaitForNetwork() {
	for t.netContext == nil {
		log.Debug("torrent waiting for network")
		time.Sleep(time.Second)
	}
}

// get our current network context
func (t *Torrent) Network() (n network.Network) {
	for t.suspended {
		time.Sleep(time.Millisecond)
	}
	t.netacces.Lock()
	n = t.netContext
	t.netacces.Unlock()
	return
}

// called when we lost network access abruptly
func (t *Torrent) LostNetwork() {
	if t.suspended {
		return
	}
	t.netacces.Lock()
	t.suspended = true
	t.netContext = nil
}

// implements io.Closer
func (t *Torrent) Close() error {
	if t.closing {
		return nil
	}
	t.closing = true
	t.started = false
	t.VisitPeers(func(c *PeerConn) {
		c.Close()
	})
	for t.NumPeers() > 0 {
		time.Sleep(time.Millisecond)
	}
	return t.st.Flush()
}

func (t *Torrent) shouldAnnounce(name string) bool {
	return time.Now().After(t.nextAnnounceFor(name))
}

func (t *Torrent) SetPieceWindow(n int) {
	t.MaxRequests = n
	t.VisitPeers(func(c *PeerConn) {
		c.MaxParalellRequests = n
	})
	t.pt.maxPending = n
}

func (t *Torrent) nextAnnounceFor(name string) (tm time.Time) {
	t.announceMtx.Lock()
	a, ok := t.announcers[name]
	if ok {
		tm = a.next
	} else {
		tm = time.Now().Add(time.Minute)
		t.announcers[name] = &torrentAnnounce{
			next:     tm,
			t:        t,
			announce: t.Trackers[name],
		}
	}
	t.announceMtx.Unlock()
	return tm
}

func newTorrent(st storage.Torrent) *Torrent {
	t := &Torrent{
		Trackers:    make(map[string]tracker.Announcer),
		announcers:  make(map[string]*torrentAnnounce),
		st:          st,
		ibconns:     make(map[string]*PeerConn),
		obconns:     make(map[string]*PeerConn),
		defaultOpts: extensions.New(),
		MaxRequests: DefaultMaxParallelRequests,
		pexState:    NewPEXSwarmState(),
	}
	t.pt = createPieceTracker(st, t.getRarestPiece)
	t.pt.have = t.broadcastHave
	return t
}

func (t *Torrent) getRarestPiece(remote *bittorrent.Bitfield, exclude []uint32) (idx uint32, has bool) {
	var swarm []*bittorrent.Bitfield
	t.VisitPeers(func(c *PeerConn) {
		if c.bf != nil {
			swarm = append(swarm, c.bf)
		}
	})
	m := make(map[uint32]bool)
	for idx := range exclude {
		m[exclude[idx]] = true
	}
	bt := t.st.Bitfield()
	idx, has = remote.FindRarest(swarm, func(idx uint32) bool {
		return bt.Has(idx) || m[idx]
	})
	return
}

// NumPeers counts how many peers we have on this torrent
func (t *Torrent) NumPeers() (count uint) {
	t.VisitPeers(func(_ *PeerConn) {
		count++
	})
	return
}

// call a visitor on each open peer connection
func (t *Torrent) VisitPeers(v func(*PeerConn)) {
	var conns []*PeerConn
	t.connMtx.Lock()
	for _, conn := range t.obconns {
		if conn != nil {
			conns = append(conns, conn)
		}
	}
	for _, conn := range t.ibconns {
		if conn != nil {
			conns = append(conns, conn)
		}
	}
	t.connMtx.Unlock()
	for _, conn := range conns {
		v(conn)
	}
}

func (t *Torrent) GetStatus() TorrentStatus {
	name := t.Name()
	var peers []*PeerConnStats
	t.VisitPeers(func(c *PeerConn) {
		peers = append(peers, c.Stats())
	})
	state := Downloading
	if t.Done() {
		state = Seeding
	}
	bf := t.Bitfield()
	var files []TorrentFileInfo
	nfo := t.st.MetaInfo().Info
	var idx uint64
	f := nfo.GetFiles()
	if len(f) == 1 {
		b := bittorrent.Bitfield{
			Data:   bf.Data,
			Length: bf.Length,
		}
		files = append(files, TorrentFileInfo{
			FileInfo: f[0],
			Progress: b.Progress(),
		})
	} else {
		for _, file := range f {
			l := file.Length / uint64(nfo.PieceLength)
			// XXX: this below here is wrong because how the bits are packed in the bitfield
			l /= 8
			plen := l
			var data []byte
			if l == 0 {
				data = []byte{bf.Data[idx]}
				plen = 1
			} else if idx+l < uint64(len(bf.Data)) {
				data = bf.Data[idx : idx+l]
			} else {
				data = bf.Data[idx:]
			}
			b := bittorrent.Bitfield{
				Data:   data,
				Length: uint32(plen),
			}
			files = append(files, TorrentFileInfo{
				FileInfo: file,
				Progress: b.Progress(),
			})
			idx += l
		}
	}
	b := bittorrent.Bitfield{
		Data:   bf.Data,
		Length: bf.Length,
	}
	return TorrentStatus{
		Peers:    peers,
		Name:     name,
		State:    state,
		Infohash: t.MetaInfo().Infohash().Hex(),
		Progress: b.Progress(),
		Files:    files,
	}
}

func (t *Torrent) Bitfield() *bittorrent.Bitfield {
	return t.st.Bitfield()
}

// start annoucing on all trackers
func (t *Torrent) StartAnnouncing() {
	t.WaitForNetwork()
	ev := tracker.Started
	if t.Done() {
		ev = tracker.Completed
	}
	for name := range t.Trackers {
		t.nextAnnounceFor(name)
		go t.announce(name, ev)
	}
	if t.announceTicker == nil {
		t.announceTicker = time.NewTicker(time.Second)
	}
	go t.pollAnnounce()
}

// stop annoucing on all trackers
func (t *Torrent) StopAnnouncing() {
	if t.announceTicker != nil {
		t.announceTicker.Stop()
		t.announceTicker = nil
	}
	for name := range t.Trackers {
		t.announce(name, tracker.Stopped)
		log.Debugf("%s stopped", name)
	}
	log.Debugf("%s stopped annoucing", t.Name())
}

// poll announce ticker channel and issue announces
func (t *Torrent) pollAnnounce() {
	for t.announceTicker != nil {
		_, ok := <-t.announceTicker.C
		if !ok {
			// done
			return
		}
		ev := tracker.Nop
		if t.Done() {
			ev = tracker.Completed
		}
		for name := range t.Trackers {
			if t.shouldAnnounce(name) {
				t.announce(name, ev)
			}
		}
	}
}

func (t *Torrent) announce(name string, ev tracker.Event) {
	t.announceMtx.Lock()
	a := t.announcers[name]
	t.announceMtx.Unlock()
	if a != nil {
		err := a.tryAnnounce(ev)
		if err == nil {
			a.fails = 0
		} else {
			log.Warnf("announce to %s failed: %s", name, err)
			a.fails++
		}
	}
}

// add peers to torrent
func (t *Torrent) addPeers(peers []common.Peer) {
	for _, p := range peers {
		a, e := p.Resolve(t.Network())
		if e == nil {
			if a.String() == t.Network().Addr().String() {
				// don't connect to self or a duplicate
				continue
			}
			if t.HasOBConn(a) {
				continue
			}
			// no error resolving
			go t.PersistPeer(a, p.ID)
		} else {
			log.Warnf("failed to resolve peer %s", e.Error())
		}
	}
}

// persit a connection to a peer
func (t *Torrent) PersistPeer(a net.Addr, id common.PeerID) {

	triesLeft := 10
	for !t.closing {
		if t.HasIBConn(a) {
			return
		}
		if !t.HasOBConn(a) {
			err := t.DialPeer(a, id)
			if err == nil {
				return
			} else {
				triesLeft--
			}
			if triesLeft <= 0 {
				return
			}
		} else {
			time.Sleep(time.Second)
		}
	}
}

func (t *Torrent) HasIBConn(a net.Addr) (has bool) {
	t.connMtx.Lock()
	_, has = t.ibconns[a.String()]
	t.connMtx.Unlock()
	return
}

func (t *Torrent) HasOBConn(a net.Addr) (has bool) {
	t.connMtx.Lock()
	_, has = t.obconns[a.String()]
	t.connMtx.Unlock()
	return
}

func (t *Torrent) addOBPeer(c *PeerConn) {
	addr := c.c.RemoteAddr()
	t.connMtx.Lock()
	t.obconns[addr.String()] = c
	t.connMtx.Unlock()
	t.pexState.onNewPeer(addr)
}

func (t *Torrent) removeOBConn(c *PeerConn) {
	addr := c.c.RemoteAddr()
	t.connMtx.Lock()
	delete(t.obconns, addr.String())
	t.connMtx.Unlock()
	t.pexState.onPeerDisconnected(addr)
}

func (t *Torrent) addIBPeer(c *PeerConn) {
	addr := c.c.RemoteAddr()
	t.connMtx.Lock()
	t.ibconns[addr.String()] = c
	t.connMtx.Unlock()
	c.inbound = true
	t.pexState.onNewPeer(addr)
}

func (t *Torrent) removeIBConn(c *PeerConn) {
	addr := c.c.RemoteAddr()
	t.connMtx.Lock()
	delete(t.ibconns, addr.String())
	t.connMtx.Unlock()
	t.pexState.onPeerDisconnected(addr)
}

// connect to a new peer for this swarm, blocks
func (t *Torrent) DialPeer(a net.Addr, id common.PeerID) error {
	if t.HasOBConn(a) {
		return nil
	}
	c, err := t.Network().Dial(a.Network(), a.String())
	if err == nil {
		// connected
		ih := t.st.Infohash()
		// build handshake
		h := new(bittorrent.Handshake)
		// enable bittorrent extensions
		h.Reserved.Set(bittorrent.Extension)
		copy(h.Infohash.Bytes(), ih.ToV1().Bytes())
		copy(h.PeerID[:], t.id[:])
		// send handshake
		err = h.Send(c)
		if err == nil {
			// get response to handshake
			err = h.Recv(c)
			if err == nil {
				if bytes.Equal(ih.ToV1().Bytes(), h.Infohash.ToV1().Bytes()) {
					// infohashes match
					var opts *extensions.Message
					if h.Reserved.Has(bittorrent.Extension) {
						opts = t.defaultOpts.Copy()
					}
					pc := makePeerConn(c, t, h.PeerID, opts)
					t.addOBPeer(pc)
					pc.start()
					pc.Send(t.Bitfield().ToWireMessage())
					return nil
				} else {
					log.Warn("Infohash missmatch")
				}
			}
		}
		log.Debugf("didn't complete handshake with peer: %s", err)
		// bad thing happened
		c.Close()
	}
	log.Debugf("didn't connect to %s: %s", a, err)
	return err
}

func (t *Torrent) broadcastHave(idx uint32) {
	msg := common.NewHave(idx)
	log.Debugf("%s got piece %d", t.Name(), idx)
	conns := make(map[string]*PeerConn)
	t.VisitPeers(func(c *PeerConn) {
		conns[c.c.RemoteAddr().String()] = c
	})
	for _, conn := range conns {
		conn.Send(msg)
	}
}

// get metainfo for this torrent
func (t *Torrent) MetaInfo() *metainfo.TorrentFile {
	return t.st.MetaInfo()
}

func (t *Torrent) Name() string {
	return t.MetaInfo().TorrentName()
}

// callback called when we get a new inbound peer
func (t *Torrent) onNewPeer(c *PeerConn) {
	a := c.c.RemoteAddr()
	if t.HasIBConn(a) {
		log.Debugf("duplicate peer from %s", a)
		c.Close()
		return
	}
	log.Debugf("New peer (%s) for %s", c.id.String(), t.st.Infohash().Hex())
	t.addIBPeer(c)
	c.start()
	c.Send(t.Bitfield().ToWireMessage())
}

func (t *Torrent) run() {
	if !t.MetaInfo().IsPrivate() {
		go t.pexBroadcastLoop()
	}
	if t.Started != nil {
		go t.Started()
	}
	t.started = true
	for !t.Done() {
		time.Sleep(time.Second * 5)
	}
	if t.Completed != nil {
		go t.Completed()
	}
}

func (t *Torrent) pexBroadcastLoop() {
	for !t.closing {
		connected, disconnected := t.pexState.PopDestHashLists()
		t.VisitPeers(func(p *PeerConn) {
			if p.SupportsPEX() {
				p.sendPEX(connected, disconnected)
			}
		})
		time.Sleep(time.Second * 90)
	}
}

func (t *Torrent) handlePieceRequest(c *PeerConn, r *common.PieceRequest) {

	if r != nil && r.Length > 0 {
		log.Debugf("%s asked for piece %d %d-%d", c.id.String(), r.Index, r.Begin, r.Begin+r.Length)
		// TODO: cache common pieces (?)
		t.st.VisitPiece(r, func(p *common.PieceData) error {
			// have the piece, send it
			c.Send(p.ToWireMessage())
			log.Debugf("%s queued piece %d %d-%d", c.id.String(), r.Index, r.Begin, r.Begin+r.Length)
			return nil
		})
		//if err != nil {
		//	ev.c.Close()
		//}
	} else {
		log.Infof("%s asked for a zero length piece", c.id.String())
		// TODO: should we close here?
		c.Close()
	}

}

func (t *Torrent) Done() bool {
	bf := t.Bitfield()
	if bf == nil {
		return false
	}
	return bf.Completed()
}

var ErrAlreadyStopped = errors.New("torrent already stopped")
var ErrAlreadyStarted = errors.New("torrent already started")

func (t *Torrent) Stop() error {
	if t.closing {
		return ErrAlreadyStopped
	}
	err := t.Close()
	t.StopAnnouncing()
	return err
}

func (t *Torrent) Delete() error {
	t.Close()
	t.StopAnnouncing()
	err := t.st.Delete()
	if err == nil {
		t.RemoveSelf()
	}
	return err
}

func (t *Torrent) Remove() error {
	err := t.Stop()
	if err != nil {
		return err
	}
	t.RemoveSelf()
	return nil
}

func (t *Torrent) Start() error {
	if t.started {
		return ErrAlreadyStarted
	}
	t.closing = false
	go t.run()
	return nil
}
