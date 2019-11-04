package rest

import (
	"encoding/json"

	"github.com/juju/errors"
)

type ExchangeDescr struct {
	ID     int                 `json:"id"`
	Symbol string              `json:"symbol"`
	Name   string              `json:"name"`
	Active bool                `json:"active"`
	Routes ExchangeDescrRoutes `json:"routes"`
}

type ExchangeDescrRoutes struct {
	Markets string `json:"markets"`
}

type ExchangeDescrBrief struct {
	ID     int    `json:"id"`
	Symbol string `json:"symbol"`
	Name   string `json:"name"`
	Route  string `json:"route"`
	Active bool   `json:"active"`
}

type exchangesDescrServer struct {
	Result []ExchangeDescrBrief `json:"result"`
}

type exchangeDescrServer struct {
	Result ExchangeDescr `json:"result"`
}

func (c *RESTClient) GetExchangesIndex() ([]ExchangeDescrBrief, error) {
	result, err := c.do(request{
		endpoint: "exchanges",
	})
	if err != nil {
		return nil, errors.Trace(err)
	}

	ret := []ExchangeDescrBrief{}
	err = json.Unmarshal(result, &ret)

	return ret, errors.Trace(err)
}

func (c *RESTClient) GetExchangeDescr(exchangeSymbol string) (*ExchangeDescr, error) {
	result, err := c.do(request{
		endpoint: "exchanges/" + exchangeSymbol,
	})
	if err != nil {
		return nil, errors.Trace(err)
	}

	ret := &ExchangeDescr{}
	err = json.Unmarshal(result, ret)

	return ret, errors.Trace(err)
}
