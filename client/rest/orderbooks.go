package rest

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/juju/errors"
	"github.com/shopspring/decimal"

	"code.cryptowat.ch/cw-sdk-go/common"
)

type orderbookServer struct {
	Result struct {
		Asks   [][]decimal.Decimal `json:"asks"`
		Bids   [][]decimal.Decimal `json:"bids"`
		SeqNum common.SeqNum       `json:"seqNum"`
	} `json:"result"`
}

func (c *RESTClient) GetOrderBook(
	exchangeSymbol string, pairSymbol string,
) (common.OrderBookSnapshot, error) {
	var ob common.OrderBookSnapshot

	resp, err := http.Get(fmt.Sprintf("%s/markets/%s/%s/orderbook", c.baseURLStr, exchangeSymbol, pairSymbol))
	if err != nil {
		return common.OrderBookSnapshot{}, errors.Trace(err)
	}

	defer resp.Body.Close()

	res := orderbookServer{}

	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&res); err != nil {
		return common.OrderBookSnapshot{}, errors.Trace(err)
	}

	ob.SeqNum = res.Result.SeqNum
	ob.Asks = make([]common.PublicOrder, 0, len(res.Result.Asks))
	ob.Bids = make([]common.PublicOrder, 0, len(res.Result.Bids))

	for i, v := range res.Result.Asks {
		if len(v) != 2 {
			return common.OrderBookSnapshot{}, errors.Errorf("ask #%d: expected tuple of 2 elements, got %v", i, v)
		}

		ob.Asks = append(ob.Asks, common.PublicOrder{
			Price:  v[0],
			Amount: v[1],
		})
	}

	for i, v := range res.Result.Bids {
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
