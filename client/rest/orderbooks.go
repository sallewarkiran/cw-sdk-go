package rest

import (
	"encoding/json"
	"fmt"

	"github.com/juju/errors"
	"github.com/shopspring/decimal"

	"code.cryptowat.ch/cw-sdk-go/common"
)

type orderbookServer struct {
	Asks   [][]decimal.Decimal `json:"asks"`
	Bids   [][]decimal.Decimal `json:"bids"`
	SeqNum common.SeqNum       `json:"seqNum"`
}

func (c *RESTClient) GetOrderBook(
	exchangeSymbol string, pairSymbol string,
) (common.OrderBookSnapshot, error) {
	var ob common.OrderBookSnapshot

	result, err := c.do(request{
		endpoint: fmt.Sprintf("markets/%s/%s/orderbook", exchangeSymbol, pairSymbol),
	})
	if err != nil {
		return common.OrderBookSnapshot{}, errors.Trace(err)
	}

	res := orderbookServer{}
	err = json.Unmarshal(result, &res)
	if err != nil {
		return common.OrderBookSnapshot{}, errors.Trace(err)
	}

	ob.SeqNum = res.SeqNum
	ob.Asks = make([]common.PublicOrder, 0, len(res.Asks))
	ob.Bids = make([]common.PublicOrder, 0, len(res.Bids))

	for i, v := range res.Asks {
		if len(v) != 2 {
			return common.OrderBookSnapshot{}, errors.Errorf("ask #%d: expected tuple of 2 elements, got %v", i, v)
		}

		ob.Asks = append(ob.Asks, common.PublicOrder{
			Price:  v[0],
			Amount: v[1],
		})
	}

	for i, v := range res.Bids {
		if len(v) != 2 {
			return common.OrderBookSnapshot{}, errors.Errorf("bid #%d: expected tuple of 2 elements, got %v", i, v)
		}

		ob.Bids = append(ob.Bids, common.PublicOrder{
			Price:  v[0],
			Amount: v[1],
		})
	}

	return ob, nil
}
