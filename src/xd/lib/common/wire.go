package common

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"xd/lib/log"
	"xd/lib/util"
)

// WireMessageType is type for wire message id
type WireMessageType byte

// Chock is message id for choke message
const Choke = WireMessageType(0)

// UnChoke is message id for unchoke message
const UnChoke = WireMessageType(1)

// Interested is messageid for interested message
const Interested = WireMessageType(2)

// NotInterested is messageid for not-interested message
const NotInterested = WireMessageType(3)

// Have is messageid for have message
const Have = WireMessageType(4)

// BitField is messageid for bitfield message
const BitField = WireMessageType(5)

// Request is messageid for piece request message
const Request = WireMessageType(6)

// Piece is messageid for response to Request message
const Piece = WireMessageType(7)

// Cancel is messageid for a Cancel message, used to cancel a pending request
const Cancel = WireMessageType(8)

// Extended is messageid for ExtendedOptions message
const Extended = WireMessageType(20)

// special for invalid
const Invalid = WireMessageType(255)

func (t WireMessageType) Byte() byte {
	return byte(t)
}

// String returns a string name of this wire message id
func (t WireMessageType) String() string {
	switch t {
	case Choke:
		return "Choke"
	case UnChoke:
		return "UnChoke"
	case Interested:
		return "Interested"
	case NotInterested:
		return "NotInterested"
	case Have:
		return "Have"
	case BitField:
		return "BitField"
	case Request:
		return "Request"
	case Piece:
		return "Piece"
	case Cancel:
		return "Cancel"
	case Extended:
		return "Extended"
	case Invalid:
		return "INVALID"
	default:
		return fmt.Sprintf("??? (%d)", uint8(t))
	}
}

// WireMessage is a serializable bittorrent wire message
type WireMessage []byte

func (msg *WireMessage) Reset() {
	*msg = nil
}

// KeepAlive makes a WireMessage of size 0
var KeepAlive = WireMessage([]byte{0, 0, 0, 0})

// NewWireMessage creates new wire message with id and body
func NewWireMessage(id WireMessageType, body []byte) (msg WireMessage) {
	if body == nil {
		body = []byte{}
	}
	l := len(body) + 1
	msg = make([]byte, 4+l)
	binary.BigEndian.PutUint32(msg[:], uint32(l))
	msg[4] = id.Byte()
	copy(msg[5:], body)
	return
}

const MaxWireMessageSize = 32 * 1024

// read wire messages from reader and call a function on each it gets
// reads until reader is done
func ReadWireMessages(r io.Reader, f func(WireMessage) error) (err error) {
	var msg [MaxWireMessageSize + 4]byte
	for err == nil {
		data := msg[:4]
		_, err = io.ReadFull(r, data)
		l := binary.BigEndian.Uint32(data)
		if l > 0 {
			if l < MaxWireMessageSize {
				log.Warnf("message too big, discarding %d bytes", l)
				_, err = io.CopyN(util.Discard, r, int64(l))
			} else {
				body := msg[4 : 4+l]
				log.Debugf("read message of size %d bytes", l)
				_, err = io.ReadFull(r, body)
				if err == nil {
					err = f(WireMessage(msg[:4+l]))
				}
			}
		}
	}
	return
}

// KeepAlive returns true if this message is a keepalive message
func (msg WireMessage) KeepAlive() bool {
	return msg.Len() == 0
}

// Len returns the length of the body of this message
func (msg WireMessage) Len() uint32 {
	return binary.BigEndian.Uint32(msg[:])
}

// Payload returns a byteslice for the body of this message
func (msg WireMessage) Payload() []byte {
	if msg.Len() > 0 {
		return msg[5:]
	} else {
		return nil
	}
}

// MessageID returns the id of this message
func (msg WireMessage) MessageID() WireMessageType {
	if len(msg) > 4 {
		return WireMessageType(msg[4])
	}
	return Invalid
}

var ErrToBig = errors.New("message too big")

// Send writes WireMessage via writer
func (msg WireMessage) Send(w io.Writer) (err error) {
	err = util.WriteFull(w, msg[:])
	return
}

// ToWireMessage serialize to BitTorrent wire message
func (p *PieceData) ToWireMessage() WireMessage {
	buff := make([]byte, 8+len(p.Data))
	binary.BigEndian.PutUint32(buff[:], p.Index)
	binary.BigEndian.PutUint32(buff[4:], p.Begin)
	copy(buff[8:], p.Data[:])
	return NewWireMessage(Piece, buff)
}

// ToWireMessage serialize to BitTorrent wire message
func (req *PieceRequest) ToWireMessage() WireMessage {
	body := make([]byte, 12)
	binary.BigEndian.PutUint32(body[:], req.Index)
	binary.BigEndian.PutUint32(body[4:], req.Begin)
	binary.BigEndian.PutUint32(body[8:], req.Length)
	return NewWireMessage(Request, body)
}

// GetPieceData gets this wire message as a PieceData if applicable
func (msg WireMessage) GetPieceData() (p PieceData) {

	if msg.MessageID() == Piece {
		data := msg.Payload()
		if len(data) > 8 {
			p.Index = binary.BigEndian.Uint32(data[:])
			p.Begin = binary.BigEndian.Uint32(data[4:])
			p.Data = data[8:]
		}
	}
	return
}

// GetPieceRequest gets piece request from wire message
func (msg WireMessage) GetPieceRequest() (req PieceRequest) {
	if msg.MessageID() == Request {
		data := msg.Payload()
		if len(data) == 12 {
			req.Index = binary.BigEndian.Uint32(data[:])
			req.Begin = binary.BigEndian.Uint32(data[4:])
			req.Length = binary.BigEndian.Uint32(data[8:])
		}
	}
	return
}

// GetHave gets the piece index of a have message
func (msg *WireMessage) GetHave() (h uint32) {
	if msg.MessageID() == Have {
		data := msg.Payload()
		if len(data) == 4 {
			h = binary.BigEndian.Uint32(data[:])
		}
	}
	return
}

// NewHave creates a new have message
func NewHave(idx uint32) WireMessage {
	body := make([]byte, 4)
	binary.BigEndian.PutUint32(body[:], idx)
	return NewWireMessage(Have, body)
}

// NewNotInterested creates a new NotInterested message
func NewNotInterested() WireMessage {
	return NewWireMessage(NotInterested, nil)
}

// NewInterested creates a new Interested message
func NewInterested() WireMessage {
	return NewWireMessage(Interested, nil)
}
