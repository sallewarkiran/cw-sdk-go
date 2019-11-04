package rest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/juju/errors"
	"github.com/shopspring/decimal"

	"code.cryptowat.ch/cw-sdk-go/common"
)

type ohlcServer struct {
	Result map[string][][]decimal.Decimal `json:"result"`
}

// GetOHLC returns a map from a period (represented as a number of seconds: 60,
// 180, etc) to a slice of OHLC candles for that period, in the time ascending
// order.
func (c *RESTClient) GetOHLC(
	exchangeSymbol, pairSymbol string,
) (map[common.Period][]common.Interval, error) {
	resp, err := http.Get(fmt.Sprintf(
		"%s/markets/%s/%s/ohlc",
		c.baseURLStr, exchangeSymbol, pairSymbol,
	))
	if err != nil {
		return nil, errors.Trace(err)
	}

	defer resp.Body.Close()

	srv := ohlcServer{}

	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&srv); err != nil {
		return nil, errors.Trace(err)
	}

	ret := make(map[common.Period][]common.Interval, len(srv.Result))
	for srvPeriod, srvCandles := range srv.Result {
		v64, err := strconv.ParseInt(srvPeriod, 10, 64)
		if err != nil {
			return nil, errors.Annotatef(err, "parsing period %q", srvPeriod)
		}

		period := common.Period(v64)

		candles := make([]common.Interval, 0, len(srvCandles))
		for _, srvCandle := range srvCandles {
			if len(srvCandle) < 7 {
				return nil, errors.Errorf("unexpected response from the server: wanted 7 elements, got %v", srvCandle)
			}

			ts := srvCandle[0].IntPart()

			candle := common.Interval{
				Period:    period,
				CloseTime: time.Unix(ts, 0).UTC(),
				OHLC: common.OHLC{
					Open:  srvCandle[1],
					High:  srvCandle[2],
					Low:   srvCandle[3],
					Close: srvCandle[4],
				},
				VolumeBase:  srvCandle[5],
				VolumeQuote: srvCandle[6],
			}

			candles = append(candles, candle)
		}

		ret[period] = candles
	}

	return ret, nil
}
