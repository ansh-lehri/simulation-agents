package core

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// candlesEndpointPath is the literal Core path for the paged candle
// history read.
const candlesEndpointPath = "/v1/candles"

// Candle timeframe values accepted by Core.
const (
	TimeframeOneMinute    = "1m"
	TimeframeFiveMinute   = "5m"
	TimeframeThirtyMinute = "30m"
	TimeframeSixtyMinute  = "60m"
)

// candlesLimitMaximum caps the per-page candle count on the wire,
// matching Core's server-side cap. Pre-flight rejects a higher Limit
// locally so a mis-sized request fails with a clear error here rather
// than opaquely at Core.
const candlesLimitMaximum = 500

// CandlesRequest carries every parameter for GET /v1/candles. The
// four required scalars (Market, Instrument, Timeframe, FromNaiveLocal,
// ToNaiveLocal) are validated pre-flight; Cursor and Limit are
// optional, with the empty / zero defaults omitted from the wire so
// Core applies its own defaults.
//
// FromNaiveLocal and ToNaiveLocal are naive-local timestamp strings in
// the "YYYY-MM-DDThh:mm:ss" format — Core does not accept a timezone
// suffix on candle timestamps; the market's timezone comes back on
// the response envelope. Callers hold their own time.Time -> string
// discipline; this client transports the strings verbatim.
type CandlesRequest struct {
	Market          string
	Instrument      string
	Timeframe       string
	FromNaiveLocal  string
	ToNaiveLocal    string
	Cursor          string
	Limit           int
}

// Candle is one row from the paged response. Open/High/Low/Close/Volume
// are integer ticks/counts — the Core contract never emits float money,
// and this client preserves int64 exactly through the decode. Start
// and End are naive-local strings, kept as strings end-to-end because
// the market timezone needed to resolve them lives on the envelope.
type Candle struct {
	StartNaiveLocal string `json:"start"`
	EndNaiveLocal   string `json:"end"`
	Open            int64  `json:"open"`
	High            int64  `json:"high"`
	Low             int64  `json:"low"`
	Close           int64  `json:"close"`
	Volume          int64  `json:"volume"`
}

// CandlesPage is one page of a paged candle read. Candles is never
// null on the wire — Core emits an empty array when a window carries
// no bars — and the decoded slice carries that discipline. NextCursor
// is empty on the final page and set to a naive-local timestamp
// cursor on every earlier page.
type CandlesPage struct {
	Market         string   `json:"market"`
	Instrument     string   `json:"instrument"`
	Timeframe      string   `json:"timeframe"`
	Timezone       string   `json:"timezone"`
	FromNaiveLocal string   `json:"from"`
	ToNaiveLocal   string   `json:"to"`
	Candles        []Candle `json:"candles"`
	NextCursor     string   `json:"next_cursor"`
}

// GetCandles fetches one page of candle history for an instrument. The
// caller drives pagination by threading NextCursor from one call into
// the next request's Cursor; on the final page NextCursor is empty.
//
// Classed as a pre-open call: candle pulls happen during the bring-up
// spine before trading begins. Idempotent (pure read); runs through
// the retry wrapper so a transient 5xx or network blip does not force
// the caller to restart its pagination from scratch.
func (client *Client) GetCandles(ctx context.Context, request CandlesRequest) (CandlesPage, error) {
	if err := validateCandlesRequest(request); err != nil {
		return CandlesPage{}, err
	}
	queryParameters := url.Values{}
	queryParameters.Set("market", request.Market)
	queryParameters.Set("instrument", request.Instrument)
	queryParameters.Set("timeframe", request.Timeframe)
	queryParameters.Set("from", request.FromNaiveLocal)
	queryParameters.Set("to", request.ToNaiveLocal)
	if request.Cursor != "" {
		queryParameters.Set("cursor", request.Cursor)
	}
	if request.Limit > 0 {
		queryParameters.Set("limit", strconv.Itoa(request.Limit))
	}
	requestPath := candlesEndpointPath + "?" + queryParameters.Encode()

	var responseBody CandlesPage
	if err := client.executeIdempotentJSONCall(
		ctx,
		preOpenCall,
		http.MethodGet,
		requestPath,
		nil,
		&responseBody,
	); err != nil {
		return CandlesPage{}, err
	}
	// Defensive: if an upstream variant ever emits `candles: null`,
	// normalize to an empty slice so callers can range over it safely.
	if responseBody.Candles == nil {
		responseBody.Candles = []Candle{}
	}
	return responseBody, nil
}

func validateCandlesRequest(request CandlesRequest) error {
	if request.Market == "" {
		return fmt.Errorf("core: GetCandles requires a non-empty market")
	}
	if request.Instrument == "" {
		return fmt.Errorf("core: GetCandles requires a non-empty instrument")
	}
	if !isAcceptedCandleTimeframe(request.Timeframe) {
		return fmt.Errorf(
			"core: GetCandles timeframe must be one of %q/%q/%q/%q, got %q",
			TimeframeOneMinute, TimeframeFiveMinute, TimeframeThirtyMinute, TimeframeSixtyMinute,
			request.Timeframe,
		)
	}
	if request.FromNaiveLocal == "" {
		return fmt.Errorf("core: GetCandles requires a non-empty from timestamp")
	}
	if request.ToNaiveLocal == "" {
		return fmt.Errorf("core: GetCandles requires a non-empty to timestamp")
	}
	if request.Limit < 0 {
		return fmt.Errorf("core: GetCandles limit must be >= 0, got %d", request.Limit)
	}
	if request.Limit > candlesLimitMaximum {
		return fmt.Errorf(
			"core: GetCandles limit must be <= %d, got %d",
			candlesLimitMaximum, request.Limit,
		)
	}
	return nil
}

func isAcceptedCandleTimeframe(timeframe string) bool {
	switch timeframe {
	case TimeframeOneMinute, TimeframeFiveMinute, TimeframeThirtyMinute, TimeframeSixtyMinute:
		return true
	default:
		return false
	}
}
