package rest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/juju/errors"
	"github.com/shopspring/decimal"

	"code.cryptowat.ch/cw-sdk-go/common"
)

type tradesServer struct {
	Result [][]decimal.Decimal `json:"result"`
}

// GetTrades returns latest trades on the given market, sorted in ascending
// order.
func (c *RESTClient) GetTrades(
	exchangeSymbol string, pairSymbol string,
) ([]common.PublicTrade, error) {
	resp, err := http.Get(fmt.Sprintf("%s/markets/%s/%s/trades", c.baseURLStr, exchangeSymbol, pairSymbol))
	if err != nil {
		return nil, errors.Trace(err)
	}

	defer resp.Body.Close()

	res := tradesServer{}

	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&res); err != nil {
		return nil, errors.Trace(err)
	}

	trades := make([]common.PublicTrade, 0, len(res.Result))

	for _, ts := range res.Result {
		if len(ts) != 4 {
			return nil, errors.Errorf("invalid trade format: %+v", ts)
		}

		trades = append(trades, common.PublicTrade{
			Timestamp: time.Unix(ts[1].IntPart(), 0),
			Price:     ts[2],
			Amount:    ts[3],
		})
	}

	return trades, nil
}
