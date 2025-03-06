package websocket

import (
	"compress/flate"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsflate"
	"github.com/gospider007/gson"
	"github.com/gospider007/re"
)

type MessageType byte
type Option struct {
	EnableCompression bool
}

func SetClientHeadersWithOption(headers http.Header, option Option) {
	p := make([]byte, 16)
	io.ReadFull(rand.Reader, p)
	headers.Set("Upgrade", "websocket")
	headers.Set("Connection", "Upgrade")
	headers.Set("Sec-WebSocket-Key", base64.StdEncoding.EncodeToString(p))
	headers.Set("Sec-WebSocket-Version", "13")
	if option.EnableCompression {
		headers.Set("Sec-WebSocket-Extensions", "permessage-deflate; client_max_window_bits")
	}
}

type Conn struct {
	conn                net.Conn
	bit                 int
	isClient            bool
	maxLength           int
	helper              wsflate.Helper
	compressorContext   *writer
	decompressorContext *reader
}

const (
	ContinuationMessage MessageType = 0x0
	TextMessage         MessageType = 0x1
	BinaryMessage       MessageType = 0x2
	CloseMessage        MessageType = 0x8
	PingMessage         MessageType = 0x9
	PongMessage         MessageType = 0xa
)

func (obj *Conn) ReadMessage() (MessageType, []byte, error) {
	var lastFrame *ws.Frame
	for {
		frame, err := ws.ReadFrame(obj.conn)
		if err != nil {
			return 0, nil, err
		}
		if frame.Header.Masked {
			frame = ws.UnmaskFrame(frame)
		}
		if frame.Header.Fin && lastFrame == nil {
			if ok, err := wsflate.IsCompressed(frame.Header); ok && err == nil {
				if frame, err = obj.helper.DecompressFrame(frame); err != nil {
					return 0, nil, err
				}
			}
			return MessageType(frame.Header.OpCode), frame.Payload, nil
		}
		switch frame.Header.OpCode {
		case ws.OpContinuation:
			if lastFrame == nil {
				return 0, nil, errors.New("invalid message")
			}
			lastFrame.Header.Fin = frame.Header.Fin
			lastFrame.Payload = append(lastFrame.Payload, frame.Payload...)
		case ws.OpText, ws.OpBinary:
			if lastFrame != nil {
				return 0, nil, errors.New("invalid message")
			}
			lastFrame = &frame
		default:
			return 0, nil, errors.New("invalid message")
		}
		if lastFrame.Header.Fin {
			if ok, err := wsflate.IsCompressed(lastFrame.Header); ok && err == nil {
				if *lastFrame, err = obj.helper.DecompressFrame(*lastFrame); err != nil {
					return 0, nil, err
				}
			}
			return MessageType(lastFrame.Header.OpCode), lastFrame.Payload, nil
		}
	}
}

func (obj *Conn) writeMeta(messageType MessageType, fin bool, data []byte) (err error) {
	frame := ws.NewFrame(ws.OpCode(messageType), fin, data)
	if obj.isClient {
		frame = ws.MaskFrame(frame)
	}
	return ws.WriteFrame(obj.conn, frame)
}

func (obj *Conn) WriteMessage(messageType MessageType, value any) error {
	p, err := gson.Encode(value)
	if err != nil {
		return err
	}
	frame := ws.NewFrame(ws.OpCode(messageType), true, p)
	if obj.helper.Compressor != nil {
		if frame, err = obj.helper.CompressFrame(frame); err != nil {
			return err
		}
	}
	if obj.maxLength <= 0 || frame.Header.Length <= int64(obj.maxLength) {
		if obj.isClient {
			frame = ws.MaskFrame(frame)
		}
		return ws.WriteFrame(obj.conn, frame)
	} else {
		p = frame.Payload[obj.maxLength:]
		err = obj.writeMeta(messageType, false, frame.Payload[:obj.maxLength])
		if err != nil {
			return err
		}
	}
	for {
		if len(p) <= obj.maxLength {
			return obj.writeMeta(ContinuationMessage, true, p)
		} else {
			err = obj.writeMeta(ContinuationMessage, false, p[:obj.maxLength])
			if err != nil {
				return err
			}
			p = p[obj.maxLength:]
		}
	}
}
func (obj *Conn) Close() error {
	return obj.conn.Close()
}
func NewConn(conn net.Conn, isClient bool, Extension string) *Conn {
	con := Conn{conn: conn, isClient: isClient}
	con.helper.Decompressor = con.decompressor
	if strings.Contains(Extension, "permessage-deflate") {
		con.helper.Compressor = con.compressor
	}
	if isClient && strings.Contains(Extension, "client_no_context_takeover") {
		con.helper.Compressor = func(w io.Writer) wsflate.Compressor {
			f, _ := flate.NewWriter(w, flate.BestCompression)
			return f
		}
		con.helper.Decompressor = func(r io.Reader) wsflate.Decompressor {
			f := flate.NewReader(r)
			return f
		}
	}
	if !isClient && strings.Contains(Extension, "server_no_context_takeover") {
		con.helper.Compressor = func(w io.Writer) wsflate.Compressor {
			f, _ := flate.NewWriter(w, flate.BestCompression)
			return f
		}
		con.helper.Decompressor = func(r io.Reader) wsflate.Decompressor {
			f := flate.NewReader(r)
			return f
		}
	}
	if bitRs := re.Search(`client_max_window_bits=(\d+)`, Extension); bitRs != nil {
		con.bit, _ = strconv.Atoi(bitRs.Group(1))
	} else if bitRs := re.Search(`server_max_window_bits=(\d+)`, Extension); bitRs != nil {
		con.bit, _ = strconv.Atoi(bitRs.Group(1))
	}
	return &con
}

type reader struct {
	r    io.ReadCloser
	dict []byte
	l    int
}

func (obj *reader) updateDict(p []byte) {
	if len(p) == 0 {
		return
	}
	pL := len(p)
	if pL >= obj.l {
		obj.dict = p[pL-obj.l:]
		return
	}
	dictL := len(obj.dict)
	yL := dictL + pL
	if yL > obj.l {
		obj.dict = obj.dict[yL-obj.l:]
	}
	obj.dict = append(obj.dict, p...)
}
func (obj *reader) Read(p []byte) (n int, err error) {
	n, err = obj.r.Read(p)
	if err == nil && n > 0 {
		obj.updateDict(p[:n])
	}
	return n, err
}
func (obj *reader) Reset(r io.Reader) {
	obj.r.(flate.Resetter).Reset(r, obj.dict)
}

func (obj *Conn) newReader(r io.Reader) *reader {
	bit := 15
	if obj.bit > 0 {
		bit = obj.bit
	}
	return &reader{
		l:    1 << uint(bit),
		dict: []byte{},
		r:    flate.NewReader(r),
	}
}

type writer struct {
	w    *flate.Writer
	dict []byte
	l    int
}

func (obj *writer) updateDict(p []byte) {
	if len(p) == 0 {
		return
	}
	pL := len(p)
	if pL >= obj.l {
		obj.dict = p[pL-obj.l:]
		return
	}
	dictL := len(obj.dict)
	yL := dictL + pL
	if yL > obj.l {
		obj.dict = obj.dict[yL-obj.l:]
	}
	obj.dict = append(obj.dict, p...)
}
func (obj *writer) Write(p []byte) (n int, err error) {
	n, err = obj.w.Write(p)
	if err == nil && n > 0 {
		obj.updateDict(p[:n])
	}
	return n, err
}
func (obj *writer) Flush() error {
	return obj.w.Flush()
}
func (obj *writer) Reset(w io.Writer) {
	obj.w.Close()
	fw, _ := flate.NewWriterDict(w, flate.BestCompression, obj.dict)
	obj.w = fw
}

func (obj *Conn) newWriter(w io.Writer) *writer {
	bit := 15
	if obj.bit > 0 {
		bit = obj.bit
	}
	fw, _ := flate.NewWriterDict(w, flate.BestCompression, nil)
	return &writer{
		l:    1 << uint(bit),
		dict: []byte{},
		w:    fw,
	}
}

func (obj *Conn) decompressor(r io.Reader) wsflate.Decompressor {
	if obj.decompressorContext == nil {
		obj.decompressorContext = obj.newReader(r)
	} else {
		obj.decompressorContext.Reset(r)
	}
	return obj.decompressorContext
}
func (obj *Conn) compressor(w io.Writer) wsflate.Compressor {
	if obj.compressorContext == nil {
		obj.compressorContext = obj.newWriter(w)
	} else {
		obj.compressorContext.Reset(w)
	}
	return obj.compressorContext
}

func (obj *Conn) IsClient() bool {
	return obj.isClient
}
