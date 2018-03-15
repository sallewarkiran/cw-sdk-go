package streamclient

import (
	"sync"

	"github.com/cryptowatch/proto/markets"
	"github.com/golang/protobuf/proto"
	"github.com/juju/errors"
)

type MarketParams struct {
	StreamParams
}

// MarketConn is a client stream connection which is able to decode market
// update messages, see AddMessageListener.
type MarketConn struct {
	*StreamConn
	msgListeners []OnMessageCallback

	mtx sync.Mutex
}

// NewMarketConn creates a new client stream connection tailored for market
// update messages.
//
// Note that clients should manually call Connect on a newly created
// connection; the rationale is that clients might register some state and/or
// message handlers before the connection, to avoid any possible races.
func NewMarketConn(params *MarketParams) (*MarketConn, error) {
	streamConn, err := NewStreamConn(&params.StreamParams)
	if err != nil {
		return nil, errors.Trace(err)
	}

	conn := &MarketConn{
		StreamConn: streamConn,
	}

	streamConn.onRead(func(sc *StreamConn, data []byte) {
		var msg ProtobufMarkets.MarketUpdateMessage
		if err := proto.Unmarshal(data, &msg); err != nil {
			// Close connection (and if reconnection was requested, then reconnect)
			conn.StreamConn.closeInternal(false)
		}

		for _, l := range conn.msgListeners {
			l(conn, &msg)
		}
	})

	return conn, nil
}

type OnMessageCallback func(conn *MarketConn, msg *ProtobufMarkets.MarketUpdateMessage)

// AddMessageListener registers a new listener for all received market update
// messages.
func (c *MarketConn) AddMessageListener(cb OnMessageCallback) {
	c.mtx.Lock()
	defer c.mtx.Unlock()

	c.msgListeners = append(c.msgListeners, cb)
}
