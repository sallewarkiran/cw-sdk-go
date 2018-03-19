package streamclient

import (
	"sync"

	pbm "github.com/cryptowatch/proto/markets"
	"github.com/golang/protobuf/proto"
	"github.com/gorilla/websocket"
	"github.com/juju/errors"
)

type MarketParams struct {
	StreamParams
}

// MarketConn is a client stream connection which is able to decode market
// update messages, see AddMessageListener.
type MarketConn struct {
	*StreamConn
	msgListeners []OnMarketMsgCallback

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

	c := &MarketConn{
		StreamConn: streamConn,
	}

	streamConn.onRead(func(sc *StreamConn, data []byte) {
		var msg pbm.MarketUpdateMessage
		if err := proto.Unmarshal(data, &msg); err != nil {
			// Close connection (and if reconnection was requested, then reconnect)
			c.StreamConn.closeInternal(
				websocket.FormatCloseMessage(websocket.CloseUnsupportedData, ""),
				false,
			)
		}

		c.mtx.Lock()
		listeners := c.msgListeners
		c.mtx.Unlock()

		for _, l := range listeners {
			l(c, &msg)
		}
	})

	return c, nil
}

type OnMarketMsgCallback func(conn *MarketConn, msg *pbm.MarketUpdateMessage)

// AddMessageListener registers a new listener for all received market update
// messages.
func (c *MarketConn) AddMessageListener(cb OnMarketMsgCallback) {
	c.mtx.Lock()
	defer c.mtx.Unlock()

	c.msgListeners = append(c.msgListeners, cb)
}
