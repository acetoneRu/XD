package metainfo

import (
	"bytes"
	"crypto/sha1"
	"github.com/zeebo/bencode"
	"io"
	"os"
	"path/filepath"
	"xd/lib/common"
	"xd/lib/log"
)

type FilePath []string

// get filepath
func (f FilePath) FilePath() string {
	return filepath.Join(f...)
}

/** open file using base path */
func (f FilePath) Open(base string) (*os.File, error) {
	return os.OpenFile(filepath.Join(base, f.FilePath()), os.O_RDWR|os.O_CREATE, 0600)
}

type FileInfo struct {
	// length of file
	Length uint64 `bencode:"length"`
	// relative path of file
	Path FilePath `bencode:"path"`
	// md5sum
	Sum []byte `bencode:"md5sum,omitempty"`
}

// (v2)
type FileTreeEntry struct {
	Length     uint64 `bencode:"length"`
	MerkleRoot []byte `bencode:"pieces root,omitempty"`
}

// (v2)
type FileTree struct {
}

// info section of torrent file
type Info struct {
	// length of pices in bytes
	PieceLength uint32 `bencode:"piece length"`
	// piece data
	Pieces []byte `bencode:"pieces"`
	// name of root file
	Path string `bencode:"name"`
	// file metadata
	Files []FileInfo `bencode:"files,omitempty"`
	// private torrent
	Private *uint64 `bencode:"private,omitempty"`
	// length of file in signle file mode
	Length uint64 `bencode:"length,omitempty"`
	// md5sum
	Sum []byte `bencode:"md5sum,omitempty"`
	// metainfo version (v2)
	MetaVersion *uint64 `bencode:"meta version,omitempty"`
	// file tree (v2)
	FileTree *FileTree `bencode:"file tree,omitempty"`
}

// is this a version 2 compatable info
func (i Info) IsV2Compat() bool {
	return i.MetaVersion != nil && *i.MetaVersion == 2
}

// is this a version 1 compatable info
func (i Info) IsV1Compat() bool {
	return i.MetaVersion == nil || *i.MetaVersion == 1
}

// is this a version 1 only info
func (i Info) IsV1Only() bool {
	return i.MetaVersion == nil
}

// get fileinfos from this info section
func (i Info) GetFiles() (infos []FileInfo) {
	if i.Length > 0 {
		infos = append(infos, FileInfo{
			Length: i.Length,
			Path:   FilePath([]string{i.Path}),
			Sum:    i.Sum,
		})
	} else {
		infos = append(infos, i.Files...)
	}
	return
}

// check if a piece is valid against the pieces in this info section
func (i Info) CheckPiece(p *common.PieceData) bool {
	idx := p.Index * 20
	if i.NumPieces() > p.Index {
		log.Debugf("sum len=%d idx=%d ih=%d", len(p.Data), idx, len(i.Pieces))
		h := sha1.Sum(p.Data[:])
		expected := i.Pieces[idx : idx+20]
		return bytes.Equal(h[:], expected)
	}
	log.Error("piece index out of bounds")
	return false
}

func (i Info) NumPieces() uint32 {
	return uint32(len(i.Pieces) / 20)
}

// (v2)
type PieceLayers map[string][]byte

// a torrent file
type TorrentFile struct {
	Info         Info       `bencode:"info"`
	Announce     string     `bencode:"announce"`
	AnnounceList [][]string `bencode:"announce-list"`
	Created      int64      `bencode:"created"`
	Comment      []byte     `bencode:"comment"`
	CreatedBy    []byte     `bencode:"created by"`
	Encoding     []byte     `bencode:"encoding"`
	// (v2)
	PieceLayers PieceLayers `bencode:"piece layers,omitemtpy"`
}

func (tf *TorrentFile) LengthOfPiece(idx uint32) (l uint32) {
	i := tf.Info
	np := i.NumPieces()
	if np == idx+1 {
		sz := tf.TotalSize()
		l64 := uint64(i.PieceLength) - ((uint64(np) * uint64(i.PieceLength)) - sz)
		l = uint32(l64)
	} else {
		l = i.PieceLength
	}
	return
}

// get total size of files from torrent info section
func (tf *TorrentFile) TotalSize() uint64 {
	if tf.IsSingleFile() {
		return tf.Info.Length
	}
	total := uint64(0)
	for _, f := range tf.Info.Files {
		total += f.Length
	}
	return total
}

func (tf *TorrentFile) GetAllAnnounceURLS() (l []string) {
	if len(tf.Announce) > 0 {
		l = append(l, tf.Announce)
	}
	for _, al := range tf.AnnounceList {
		for _, a := range al {
			if len(a) > 0 {
				l = append(l, a)
			}
		}
	}
	return
}

func (tf *TorrentFile) TorrentName() string {
	return tf.Info.Path
}

// calculate infohash v1
func (tf *TorrentFile) InfohashV1() (ih common.InfohashV1) {
	s := sha1.New()
	enc := bencode.NewEncoder(s)
	enc.Encode(&tf.Info)
	d := s.Sum(nil)
	copy(ih[:], d[:])
	return
}

func (tf *TorrentFile) Infohash() common.Infohash {
	if tf.Info.IsV1Only() {
		return tf.InfohashV1()
	}
	return tf.InfohashV2()
}

func (tf *TorrentFile) InfohashV2() (ih common.InfohashV2) {
	return
}

// return true if this torrent is for a single file
func (tf *TorrentFile) IsSingleFile() bool {
	return tf.Info.Length > 0
}

// bencode this file via an io.Writer
func (tf *TorrentFile) BEncode(w io.Writer) (err error) {
	enc := bencode.NewEncoder(w)
	err = enc.Encode(tf)
	return
}

// load from an io.Reader
func (tf *TorrentFile) BDecode(r io.Reader) (err error) {
	dec := bencode.NewDecoder(r)
	err = dec.Decode(tf)
	return
}

// IsPrivate returns true if this torrent is a private torrent
func (tf *TorrentFile) IsPrivate() bool {
	return tf.Info.Private != nil && *tf.Info.Private > 0
}
