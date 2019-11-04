package rest

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/juju/errors"
	"github.com/shopspring/decimal"
)

type priceServer struct {
	Result struct {
		Price decimal.Decimal `json:"price"`
	} `json:"result"`
}

// GetPrice returns latest price on the given market.
func (c *RESTClient) GetPrice(
	exchangeSymbol string, pairSymbol string,
) (decimal.Decimal, error) {
	resp, err := http.Get(fmt.Sprintf("%s/markets/%s/%s/price", c.baseURLStr, exchangeSymbol, pairSymbol))
	if err != nil {
		return decimal.Decimal{}, errors.Trace(err)
	}

	defer resp.Body.Close()

	res := priceServer{}

	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&res); err != nil {
		return decimal.Decimal{}, errors.Trace(err)
	}

	return res.Result.Price, nil
}
