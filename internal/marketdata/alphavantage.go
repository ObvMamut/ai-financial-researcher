package marketdata

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

type alphaVantageProvider struct {
	client  *http.Client
	apiKey  string
	limiter *Limiter
}

func NewAlphaVantageProvider(apiKey string) Provider {
	return &alphaVantageProvider{
		client:  &http.Client{Timeout: 10 * time.Second},
		apiKey:  apiKey,
		limiter: NewLimiter(25, 5), // 25 per day, 5 per minute
	}
}

func (p *alphaVantageProvider) Name() string   { return "AlphaVantage" }
func (p *alphaVantageProvider) Source() string { return "https://www.alphavantage.co" }
func (p *alphaVantageProvider) Domains() []string {
	return []string{"technicals", "news", "sentiment"}
}
func (p *alphaVantageProvider) Available() bool { return p.apiKey != "" }

func (p *alphaVantageProvider) Fetch(ctx context.Context, domain string, ticker string) (TickerData, error) {
	if !p.Available() {
		return TickerData{}, ErrUnavailable
	}

	switch domain {
	case "technicals":
		return p.fetchGlobalQuote(ctx, ticker)
	case "news", "sentiment":
		return p.fetchNewsSentiment(ctx, ticker)
	default:
		return TickerData{}, ErrUnavailable
	}
}

func (p *alphaVantageProvider) fetchGlobalQuote(ctx context.Context, ticker string) (TickerData, error) {
	if !p.limiter.Allow() {
		return TickerData{}, fmt.Errorf("%w: AlphaVantage rate limit reached", ErrUnavailable)
	}

	v := url.Values{}
	v.Set("function", "GLOBAL_QUOTE")
	v.Set("symbol", ticker)
	v.Set("apikey", p.apiKey)

	u := "https://www.alphavantage.co/query?" + v.Encode()
	resp, err := p.client.Get(u)
	if err != nil {
		return TickerData{}, err
	}
	defer resp.Body.Close()

	var data struct {
		Quote map[string]string `json:"Global Quote"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return TickerData{}, err
	}

	td := TickerData{Ticker: ticker}
	if price, ok := data.Quote["05. price"]; ok {
		td.Facts = append(td.Facts, Fact{
			Label:  "Last Price",
			Value:  price,
			AsOf:   time.Now(),
			Source: "AlphaVantage",
		})
	}
	if change, ok := data.Quote["10. change percent"]; ok {
		td.Facts = append(td.Facts, Fact{
			Label:  "Change %",
			Value:  change,
			AsOf:   time.Now(),
			Source: "AlphaVantage",
		})
	}

	return td, nil
}

func (p *alphaVantageProvider) fetchNewsSentiment(ctx context.Context, ticker string) (TickerData, error) {
	if !p.limiter.Allow() {
		return TickerData{}, fmt.Errorf("%w: AlphaVantage rate limit reached", ErrUnavailable)
	}

	v := url.Values{}
	v.Set("function", "NEWS_SENTIMENT")
	v.Set("tickers", ticker)
	v.Set("limit", "5")
	v.Set("apikey", p.apiKey)

	u := "https://www.alphavantage.co/query?" + v.Encode()
	resp, err := p.client.Get(u)
	if err != nil {
		return TickerData{}, err
	}
	defer resp.Body.Close()

	var data struct {
		Feed []struct {
			Title               string  `json:"title"`
			OverallSentimentScore float64 `json:"overall_sentiment_score"`
			OverallSentimentLabel string  `json:"overall_sentiment_label"`
		} `json:"feed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return TickerData{}, err
	}

	td := TickerData{Ticker: ticker}
	if len(data.Feed) > 0 {
		avgScore := 0.0
		for _, item := range data.Feed {
			avgScore += item.OverallSentimentScore
		}
		avgScore /= float64(len(data.Feed))

		td.Facts = append(td.Facts, Fact{
			Label:  "News Sentiment Score",
			Value:  fmt.Sprintf("%.2f", avgScore),
			AsOf:   time.Now(),
			Source: "AlphaVantage",
		})
	}

	return td, nil
}

func (p *alphaVantageProvider) MacroFetch(ctx context.Context) ([]Fact, error) {
	return nil, ErrUnavailable
}
