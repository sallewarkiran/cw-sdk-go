package rest

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/juju/errors"
)

type PairDescr struct {
	ID      int           `json:"id"`
	Symbol  string        `json:"symbol"`
	Route   string        `json:"route"`
	Base    PairSideDescr `json:"base"`
	Quote   PairSideDescr `json:"quote"`
	Markets []MarketDescr `json:"markets"`
}

type PairSideDescr struct {
	ID     int    `json:"id"`
	Symbol string `json:"symbol"`
	Name   string `json:"name"`
	Fiat   bool   `json:"fiat"`
	Route  string `json:"route"`
}

type pairsDescrServer struct {
	Result []PairDescr `json:"result"`
}

type pairDescrServer struct {
	Result PairDescr `json:"result"`
	Error  string    `json:"error"`
}

func (c *RESTClient) GetPairsIndex() ([]PairDescr, error) {
	resp, err := http.Get(fmt.Sprintf("%s/pairs", c.baseURLStr))
	if err != nil {
		return nil, errors.Trace(err)
	}

	defer resp.Body.Close()

	res := pairsDescrServer{}

	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&res); err != nil {
		return nil, errors.Trace(err)
	}

	return res.Result, nil
}

func (c *RESTClient) GetPairDescr(symbol string) (PairDescr, error) {
	resp, err := http.Get(fmt.Sprintf("%s/pairs/%s", c.baseURLStr, symbol))
	if err != nil {
		return PairDescr{}, errors.Trace(err)
	}

	defer resp.Body.Close()

	res := pairDescrServer{}

	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&res); err != nil {
		return PairDescr{}, errors.Trace(err)
	}

	return res.Result, nil
}
