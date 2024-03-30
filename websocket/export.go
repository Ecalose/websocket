package websocket

import (
	"io"
	"net"
	"net/http"
)

type Option struct {
	Subprotocol            string
	EnableCompression      bool
	ReadBufferSize         int
	WriteBufferSize        int
	NewCompressionWriter   func(io.WriteCloser, int) io.WriteCloser
	NewDecompressionReader func(io.Reader) io.ReadCloser
}

func SetClientHeadersOption(headers http.Header, option Option) {
	challengeKey, _ := generateChallengeKey()
	headers.Set("Upgrade", "websocket")
	headers.Set("Connection", "Upgrade")
	headers.Set("Sec-WebSocket-Key", challengeKey)
	headers.Set("Sec-WebSocket-Version", "13")
	if option.Subprotocol != "" {
		headers.Set("Sec-WebSocket-Protocol", option.Subprotocol)
	}
	if option.EnableCompression {
		headers.Set("Sec-WebSocket-Extensions", "permessage-deflate; server_no_context_takeover; client_no_context_takeover")
	}
}
func GetResponseHeaderOption(header http.Header) Option {
	var option Option
	option.Subprotocol = header.Get("Sec-Websocket-Protocol")
	for _, ext := range parseExtensions(header) {
		if ext[""] != "permessage-deflate" {
			continue
		}
		_, snct := ext["server_no_context_takeover"]
		_, cnct := ext["client_no_context_takeover"]
		if !snct || !cnct {
			return option
		}
		option.NewCompressionWriter = compressNoContextTakeover
		option.NewDecompressionReader = decompressNoContextTakeover
		option.EnableCompression = true
		break
	}
	return option
}

func GetRequestHeaderOption(header http.Header) Option {
	var option Option
	option.Subprotocol = header.Get("Sec-Websocket-Protocol")
	var compress bool
	for _, ext := range parseExtensions(header) {
		if ext[""] != "permessage-deflate" {
			continue
		}
		compress = true
		option.NewCompressionWriter = compressNoContextTakeover
		option.NewDecompressionReader = decompressNoContextTakeover
		break
	}
	option.EnableCompression = compress
	return option
}
func NewClientConn(conn net.Conn, option Option) *Conn {
	con := newConn(conn, false, option.ReadBufferSize, option.WriteBufferSize, nil, nil, nil)
	con.newCompressionWriter = option.NewCompressionWriter
	con.newDecompressionReader = option.NewDecompressionReader
	con.subprotocol = option.Subprotocol
	return con
}
func NewServerConn(conn net.Conn, option Option) *Conn {
	con := newConn(conn, true, option.ReadBufferSize, option.WriteBufferSize, nil, nil, nil)
	con.subprotocol = option.Subprotocol
	if option.EnableCompression {
		con.newCompressionWriter = compressNoContextTakeover
		con.newDecompressionReader = decompressNoContextTakeover
	} else {
		con.newCompressionWriter = option.NewCompressionWriter
		con.newDecompressionReader = option.NewDecompressionReader
	}
	return con
}
