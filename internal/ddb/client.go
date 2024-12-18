package ddb

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-dax-go-v2/dax"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/BohuaiSong2004/study-abroad-assistant/internal/platform"
)

// Cache modes.
const (
	CacheOff   = "off"   // every read goes to DynamoDB
	CacheLocal = "local" // in-process DAX-style item + query cache
	CacheDAX   = "dax"   // Amazon DAX cluster (production)
)

// Config selects the DynamoDB endpoint and caching strategy.
type Config struct {
	Endpoint      string // e.g. http://localhost:8000 for DynamoDB Local
	Region        string
	CacheMode     string
	DAXEndpoint   string // e.g. daxs://cluster.xxxx.dax-clusters.us-east-1.amazonaws.com
	ItemTTL       time.Duration
	QueryTTL      time.Duration
	NegativeTTL   time.Duration
	ItemCapacity  int
	QueryCapacity int
}

// ConfigFromEnv reads DYNAMODB_ENDPOINT, AWS_REGION, CACHE_MODE, DAX_ENDPOINT,
// CACHE_ITEM_TTL, CACHE_QUERY_TTL, CACHE_NEGATIVE_TTL, CACHE_ITEMS, CACHE_QUERIES.
func ConfigFromEnv() Config {
	return Config{
		Endpoint:      platform.Env("DYNAMODB_ENDPOINT", ""),
		Region:        platform.Env("AWS_REGION", "us-east-1"),
		CacheMode:     platform.Env("CACHE_MODE", CacheLocal),
		DAXEndpoint:   platform.Env("DAX_ENDPOINT", ""),
		ItemTTL:       platform.EnvDuration("CACHE_ITEM_TTL", 5*time.Minute),
		QueryTTL:      platform.EnvDuration("CACHE_QUERY_TTL", 10*time.Second),
		NegativeTTL:   platform.EnvDuration("CACHE_NEGATIVE_TTL", 2*time.Second),
		ItemCapacity:  platform.EnvInt("CACHE_ITEMS", 50_000),
		QueryCapacity: platform.EnvInt("CACHE_QUERIES", 5_000),
	}
}

// Clients bundles the read path (possibly cached) with the raw client.
type Clients struct {
	// API is used for normal reads and writes; it may be cached.
	API API
	// Raw always talks to DynamoDB directly: table management and reads that
	// must not be served from any cache.
	Raw *dynamodb.Client
	// Local is set when CacheMode is "local" (for stats and tests).
	Local *CachedAPI
}

// Connect builds the clients for cfg.
func Connect(ctx context.Context, cfg Config) (*Clients, error) {
	opts := []func(*config.LoadOptions) error{config.WithRegion(cfg.Region)}
	if cfg.Endpoint != "" {
		// DynamoDB Local accepts any credentials; avoid probing IMDS.
		opts = append(opts, config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("local", "local", "")))
	}
	awsCfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	raw := dynamodb.NewFromConfig(awsCfg, func(o *dynamodb.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
	})
	c := &Clients{API: raw, Raw: raw}
	switch cfg.CacheMode {
	case CacheOff, "":
	case CacheLocal:
		c.Local = NewCachedAPI(raw, CacheOptions{
			ItemTTL: cfg.ItemTTL, QueryTTL: cfg.QueryTTL, NegativeTTL: cfg.NegativeTTL,
			ItemCapacity: cfg.ItemCapacity, QueryCapacity: cfg.QueryCapacity,
		})
		c.API = c.Local
	case CacheDAX:
		if cfg.DAXEndpoint == "" {
			return nil, fmt.Errorf("CACHE_MODE=dax requires DAX_ENDPOINT")
		}
		d, err := dax.NewFromConfig(awsCfg, cfg.DAXEndpoint)
		if err != nil {
			return nil, fmt.Errorf("connect dax: %w", err)
		}
		c.API = d
	default:
		return nil, fmt.Errorf("unknown CACHE_MODE %q (want off, local or dax)", cfg.CacheMode)
	}
	return c, nil
}

// The DAX client must stay a drop-in replacement for the SDK client.
var _ API = (*dax.Dax)(nil)
