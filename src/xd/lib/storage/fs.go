package storage

import (
	"errors"
	"io"
	"sync"
	"xd/lib/bittorrent"
	"xd/lib/common"
	"xd/lib/fs"
	"xd/lib/log"
	"xd/lib/metainfo"
)

// filesystem based storrent storage session
type fsTorrent struct {
	// parent storage
	st *FsStorage
	// infohash
	ih common.Infohash
	// metainfo
	meta *metainfo.TorrentFile
	// cached bitfield
	bf *bittorrent.Bitfield
	// mutex for bitfield access
	bfmtx sync.RWMutex
}

func (t *fsTorrent) Delete() (err error) {
	err = t.st.FS.RemoveAll(t.st.metainfoFilename(t.ih))
	if err == nil {
		err = t.st.FS.RemoveAll(t.st.bitfieldFilename(t.ih))
		if err == nil {
			err = t.st.FS.RemoveAll(t.FilePath())
		}
	}
	return
}

func (t *fsTorrent) AllocateFile(f metainfo.FileInfo) (err error) {
	fname := t.st.FS.Join(t.FilePath(), f.Path.FilePath())
	err = t.st.FS.EnsureFile(fname, f.Length)
	return
}

func (t *fsTorrent) Allocate() (err error) {
	if t.meta.IsSingleFile() {
		log.Debugf("file is %d bytes", t.meta.Info.Length)
		err = t.st.FS.EnsureFile(t.FilePath(), t.meta.Info.Length)
	} else {
		for _, f := range t.meta.Info.Files {
			err = t.AllocateFile(f)
			if err != nil {
				break
			}
		}
	}
	return
}

func (t *fsTorrent) openfileRead(i metainfo.FileInfo) (f fs.ReadFile, err error) {
	var fname string
	if t.meta.IsSingleFile() {
		fname = t.st.FS.Join(t.st.DataDir, i.Path.FilePath())
	} else {
		fname = t.st.FS.Join(t.FilePath(), i.Path.FilePath())
	}
	f, err = t.st.FS.OpenFileReadOnly(fname)
	return
}

func (t *fsTorrent) openfileWrite(i metainfo.FileInfo) (f fs.WriteFile, err error) {
	var fname string
	if t.meta.IsSingleFile() {
		fname = t.st.FS.Join(t.st.DataDir, i.Path.FilePath())
	} else {
		fname = t.st.FS.Join(t.FilePath(), i.Path.FilePath())
	}
	f, err = t.st.FS.OpenFileWriteOnly(fname)
	return
}

func (t *fsTorrent) readFileAt(fi metainfo.FileInfo, b []byte, off int64) (n int, err error) {

	// from github.com/anacrolix/torrent
	var f fs.ReadFile
	f, err = t.openfileRead(fi)
	fil := int64(fi.Length)
	// Limit the read to within the expected bounds of this file.
	if int64(len(b)) > fil-off {
		b = b[:fil-off]
	}
	for off < fil && len(b) != 0 {
		n1, err1 := f.ReadAt(b, off)
		b = b[n1:]
		n += n1
		off += int64(n1)
		if n1 == 0 {
			err = err1
			break
		}
	}
	return
}

func (t *fsTorrent) ReadAt(b []byte, off int64) (n int, err error) {

	// from github.com/anacrolix/torrent
	for _, fi := range t.meta.Info.GetFiles() {
		fil := int64(fi.Length)
		for off < fil {
			n1, err1 := t.readFileAt(fi, b, off)
			n += n1
			off += int64(n1)
			b = b[n1:]
			if len(b) == 0 {
				// Got what we need.
				return
			}
			if n1 != 0 {
				// Made progress.
				continue
			}
			err = err1
			if err == io.EOF {
				// Lies.
				err = io.ErrUnexpectedEOF
			}
			return
		}
		off -= fil
	}
	err = io.EOF
	return
}

func (t *fsTorrent) WriteAt(p []byte, off int64) (n int, err error) {

	// from github.com/anacrolix/torrent
	for _, fi := range t.meta.Info.GetFiles() {
		fil := int64(fi.Length)
		if off >= fil {
			off -= fil
			continue
		}
		n1 := len(p)
		if int64(n1) > fil-off {
			n1 = int(fil - off)
		}
		var f fs.WriteFile
		f, err = t.openfileWrite(fi)
		if err != nil {
			return
		}
		n1, err = f.WriteAt(p[:n1], off)
		f.Close()
		if err != nil {
			return
		}
		n += n1
		off = 0
		p = p[n1:]
		if len(p) == 0 {
			break
		}
	}
	return
}

func (t *fsTorrent) Bitfield() *bittorrent.Bitfield {
	t.bfmtx.Lock()
	if t.bf == nil {
		if !t.st.HasBitfield(t.ih) {
			// we have no pieces
			t.st.CreateNewBitfield(t.ih, t.meta.Info.NumPieces())
		}
		t.bf = t.st.FindBitfield(t.ih)
	}
	t.bfmtx.Unlock()
	return t.bf
}

func (t *fsTorrent) DownloadRemaining() (r uint64) {
	bf := t.Bitfield()
	have := uint64(bf.CountSet()) * uint64(t.meta.Info.PieceLength)
	r = t.meta.TotalSize() - have
	return
}

func (t *fsTorrent) MetaInfo() *metainfo.TorrentFile {
	return t.meta
}

func (t *fsTorrent) Name() string {
	return t.meta.TorrentName()
}

func (t *fsTorrent) Infohash() (ih common.Infohash) {
	var ihv1 common.InfohashV1
	copy(ihv1[:], t.ih.ToV1().Bytes())
	ih = ihv1
	return
}

func (t *fsTorrent) FilePath() string {
	return t.st.FS.Join(t.st.DataDir, t.meta.Info.Path)
}

func (t *fsTorrent) VisitPiece(r *common.PieceRequest, v func(*common.PieceData) error) (err error) {
	sz := t.meta.Info.PieceLength
	p := &common.PieceData{
		Index: r.Index,
		Begin: r.Begin,
		Data:  make([]byte, r.Length, r.Length),
	}
	_, err = t.ReadAt(p.Data, int64(r.Begin)+(int64(sz)*int64(r.Index)))
	if err == nil {
		err = v(p)
	}
	return
}

func (t *fsTorrent) checkPiece(pc *common.PieceData) (err error) {
	if !t.meta.Info.CheckPiece(pc) {
		err = common.ErrInvalidPiece
	}
	return
}

func (t *fsTorrent) VerifyPiece(idx uint32) (err error) {
	l := t.meta.LengthOfPiece(idx)
	err = t.VisitPiece(&common.PieceRequest{
		Index:  idx,
		Length: l,
	}, t.checkPiece)
	return
}

func (t *fsTorrent) PutPiece(pc *common.PieceData) (err error) {

	err = t.checkPiece(pc)
	if err == nil {
		sz := int64(t.meta.Info.PieceLength)
		_, err = t.WriteAt(pc.Data, sz*int64(pc.Index))
		if err == nil {
			t.bf.Set(pc.Index)
		}
	}
	return
}

func (t *fsTorrent) VerifyAll(fresh bool) (err error) {
	t.bfmtx.Lock()
	check := t.st.FindBitfield(t.ih)
	if check == nil {
		// no stored bitfield
		log.Infof("no bitfield for %s", t.Name())
		check = bittorrent.NewBitfield(t.meta.Info.NumPieces(), nil).Inverted()
		if fresh {
			var has *bittorrent.Bitfield
			has, err = t.verifyBitfield(check, false)
			t.st.flushBitfield(t.ih, has)
			t.bfmtx.Unlock()
			return
		}
	}
	// verify
	log.Infof("verify local data for %s", t.Name())
	t.bf, err = t.verifyBitfield(check, true)
	if err == nil {
		if t.bf.Equals(check) {
			log.Infof("%s check okay", t.Name())
		} else {
			log.Warnf("%s has miss matched data", t.Name())
		}
	} else {
		t.bfmtx.Unlock()
		return
	}
	t.bfmtx.Unlock()
	err = t.Flush()
	return
}

// verifyBitfield verifies a all pieces given by a bitfield
func (t *fsTorrent) verifyBitfield(bf *bittorrent.Bitfield, warn bool) (has *bittorrent.Bitfield, err error) {
	np := t.meta.Info.NumPieces()
	has = bittorrent.NewBitfield(np, nil)
	idx := uint32(0)
	for idx < np {
		l := t.meta.LengthOfPiece(idx)
		if bf.Has(idx) {
			err = t.VisitPiece(&common.PieceRequest{
				Index:  idx,
				Length: l,
			}, func(pc *common.PieceData) (e error) {
				e = t.checkPiece(pc)
				if e == nil {
					has.Set(idx)
				} else if warn {
					log.Warnf("piece %d failed check for %s: %s", idx, t.Name(), e)
				}
				return
			})
		}
		idx++
		log.Debugf("piece %d of %d", idx, np)
	}
	return
}

func (t *fsTorrent) Flush() error {
	log.Debugf("flush bitfield for %s", t.ih.Hex())
	bf := t.Bitfield()
	return t.st.flushBitfield(t.ih, bf)
}

func (t *fsTorrent) Close() error {
	return t.Flush()
}

// filesystem based torrent storage
type FsStorage struct {
	// directory for downloaded data
	DataDir string
	// directory for torrent seed data
	MetaDir string
	// filesystem driver
	FS fs.Driver
}

func (st *FsStorage) Close() (err error) {
	err = st.FS.Close()
	return
}

func (st *FsStorage) flushBitfield(ih common.Infohash, bf *bittorrent.Bitfield) (err error) {
	fname := st.bitfieldFilename(ih)
	var f fs.WriteFile
	f, err = st.FS.OpenFileWriteOnly(fname)
	if err == nil {
		err = bf.BEncode(f)
		f.Close()
	}
	return
}

func (st *FsStorage) Init() (err error) {
	log.Info("Ensure filesystem storage")
	err = st.FS.Open()
	if err != nil {
		return
	}
	if st.DataDir == "" || st.MetaDir == "" {
		err = errors.New("bad FsStorage parameters")
		return
	}
	err = st.FS.EnsureDir(st.DataDir)
	if err == nil {
		err = st.FS.EnsureDir(st.MetaDir)
	}
	return
}

func (st *FsStorage) FindBitfield(ih common.Infohash) (bf *bittorrent.Bitfield) {
	fpath := st.bitfieldFilename(ih)
	f, err := st.FS.OpenFileReadOnly(fpath)
	if err == nil {
		bf = new(bittorrent.Bitfield)
		err = bf.BDecode(f)
		if err != nil {
			bf = nil
		}
		f.Close()
	}
	return
}

func (st *FsStorage) bitfieldFilename(ih common.Infohash) string {
	return st.FS.Join(st.MetaDir, ih.Hex()+".bitfield")
}

func (st *FsStorage) HasBitfield(ih common.Infohash) bool {
	return st.FS.FileExists(st.bitfieldFilename(ih))
}

func (st *FsStorage) CreateNewBitfield(ih common.Infohash, bits uint32) {
	fname := st.bitfieldFilename(ih)
	bf := bittorrent.NewBitfield(bits, nil)
	f, err := st.FS.OpenFileWriteOnly(fname)
	if err == nil {
		bf.BEncode(f)
		f.Close()
	}
}

func (st *FsStorage) metainfoFilename(ih common.Infohash) string {
	return st.FS.Join(st.MetaDir, ih.Hex()+".torrent")
}

func (st *FsStorage) OpenTorrent(info *metainfo.TorrentFile) (t Torrent, err error) {
	basepath := st.FS.Join(st.DataDir, info.TorrentName())
	if !info.IsSingleFile() {
		// create directory
		st.FS.EnsureDir(basepath)
	}

	ih := info.Infohash()
	metapath := st.metainfoFilename(ih)
	if !st.FS.FileExists(metapath) {
		// put meta info down onto filesystem
		var f fs.WriteFile
		f, err = st.FS.OpenFileWriteOnly(metapath)
		if err == nil {
			info.BEncode(f)
			f.Close()
		}
	}

	if err == nil {
		ft := &fsTorrent{
			st:   st,
			meta: info,
			ih:   ih,
		}
		log.Debugf("allocate space for %s", ft.Name())
		err = ft.Allocate()
		if err != nil {
			t = nil
			return
		}
		t = ft
	}

	return
}

func (st *FsStorage) OpenAllTorrents() (torrents []Torrent, err error) {
	var matches []string
	matches, err = st.FS.Glob(st.FS.Join(st.MetaDir, "*.torrent"))
	for _, m := range matches {
		var t Torrent
		var f fs.ReadFile
		tf := new(metainfo.TorrentFile)
		f, err = st.FS.OpenFileReadOnly(m)
		if err == nil {
			err = tf.BDecode(f)
			f.Close()
		}
		if err == nil {
			t, err = st.OpenTorrent(tf)
		}
		if t != nil {
			torrents = append(torrents, t)
		}
	}
	return
}

func (st *FsStorage) PollNewTorrents() (torrents []Torrent) {
	matches, _ := st.FS.Glob(st.FS.Join(st.DataDir, "*.torrent"))
	for _, m := range matches {
		var t Torrent
		tf := new(metainfo.TorrentFile)
		f, err := st.FS.OpenFileReadOnly(m)
		if err == nil {
			err = tf.BDecode(f)
			f.Close()
		}
		if err != nil {
			log.Warnf("error checking torrent file %s: %s", m, err)
		}
		if st.HasBitfield(tf.Infohash()) {
			// we already have this torrent
			continue
		}
		if err == nil {
			t, err = st.OpenTorrent(tf)
		}
		if t != nil {
			torrents = append(torrents, t)
		}
	}
	return
}
