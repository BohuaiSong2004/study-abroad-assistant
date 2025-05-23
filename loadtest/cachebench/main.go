// Command cachebench measures DynamoDB read latency with and without the
// DAX-style read-through cache (internal/ddb.CachedAPI), against whatever
// DYNAMODB_ENDPOINT points at (DynamoDB Local in the published numbers).
//
//	go run ./loadtest/cachebench -items 2000 -reads 20000 -concurrency 8
//
// Each run creates a throw-away table, writes realistic program items, and
// replays the same random key sequence through both clients.
package main

import (
	"context"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"google.golang.org/protobuf/proto"

	catalogv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/catalog/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ddb"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ids"
)

func main() {
	endpoint := flag.String("endpoint", envOr("DYNAMODB_ENDPOINT", "http://localhost:8000"), "DynamoDB endpoint")
	items := flag.Int("items", 2000, "items in the table")
	reads := flag.Int("reads", 20000, "reads per mode")
	conc := flag.Int("concurrency", 8, "concurrent readers")
	hot := flag.Float64("zipf", 1.1, "Zipf skew of key popularity (s > 1; higher = hotter keys)")
	flag.Parse()
	ctx := context.Background()

	c, err := ddb.Connect(ctx, ddb.Config{Endpoint: *endpoint, Region: "us-east-1", CacheMode: ddb.CacheOff})
	check(err)
	prefix := "bench" + ids.New("")[18:] + "-"
	check(ddb.EnsureTables(ctx, c.Raw, prefix, ddb.TableCatalog))
	defer ddb.DeleteTables(context.Background(), c.Raw, prefix, ddb.TableCatalog)
	table := prefix + ddb.TableCatalog

	fmt.Printf("writing %d items...\n", *items)
	for i := 0; i < *items; i++ {
		p := &catalogv1.Program{
			Id: fmt.Sprintf("p%05d", i), University: fmt.Sprintf("University %d", i), Name: "MS in Computer Science",
			Degree: catalogv1.Degree_DEGREE_MASTER, Field: "Computer Science", Country: "Canada", City: "Toronto",
			Timezone: "America/Toronto", Intake: "Fall 2027", Version: 1,
			Deadlines:    []*catalogv1.Deadline{{Round: catalogv1.RoundKind_ROUND_KIND_ROUND_1, Date: "2027-01-15"}, {Round: catalogv1.RoundKind_ROUND_KIND_ROUND_2, Date: "2027-03-01"}},
			Requirements: &catalogv1.Requirements{MinToefl: 100, RecommendationsRequired: 2, Documents: []catalogv1.DocumentType{1, 2, 3, 6}},
		}
		raw, _ := proto.Marshal(p)
		item := ddb.Key("PROGRAM#"+p.Id, "META")
		item["program"] = ddb.B(raw)
		_, err := c.Raw.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(table), Item: item})
		check(err)
	}

	// Same key sequence for both modes: popularity follows a Zipf
	// distribution, as catalog and timeline reads do (a few hot programs).
	r := rand.New(rand.NewPCG(42, 42)) //nolint:gosec // deterministic benchmark key sequence
	z := rand.NewZipf(r, *hot, 1, uint64(*items-1))
	keys := make([]string, *reads)
	for i := range keys {
		keys[i] = fmt.Sprintf("p%05d", z.Uint64())
	}

	cached := ddb.NewCachedAPI(c.Raw, ddb.CacheOptions{ItemTTL: 5 * time.Minute})
	fmt.Printf("\n%d reads, %d concurrent readers, %d items, zipf s=%.2f, endpoint %s\n\n", *reads, *conc, *items, *hot, *endpoint)
	fmt.Printf("%-28s %9s %9s %9s %9s %10s\n", "mode", "p50 ms", "p95 ms", "p99 ms", "mean ms", "reads/s")
	off := run(ctx, c.Raw, table, keys, *conc)
	report("DynamoDB (CACHE_MODE=off)", off)
	on := run(ctx, cached, table, keys, *conc)
	report("read-through cache (local)", on)
	st := cached.ItemStats()
	fmt.Printf("\ncache hit ratio: %.1f%% (%d hits, %d misses)\n", 100*float64(st.Hits)/float64(st.Hits+st.Misses), st.Hits, st.Misses)
	fmt.Printf("p50 latency change: %.1f%%   mean latency change: %.1f%%\n",
		100*(on.p50-off.p50)/off.p50, 100*(on.mean-off.mean)/off.mean)
}

type result struct {
	p50, p95, p99, mean, rps float64
}

func run(ctx context.Context, api ddb.API, table string, keys []string, conc int) result {
	lat := make([]float64, len(keys))
	var wg sync.WaitGroup
	next := make(chan int, len(keys))
	for i := range keys {
		next <- i
	}
	close(next)
	start := time.Now()
	for w := 0; w < conc; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				t := time.Now()
				out, err := api.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(table), Key: ddb.Key("PROGRAM#"+keys[i], "META")})
				check(err)
				var p catalogv1.Program
				check(proto.Unmarshal(ddb.GetB(out.Item, "program"), &p))
				lat[i] = float64(time.Since(t).Microseconds()) / 1000
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	sorted := append([]float64(nil), lat...)
	sort.Float64s(sorted)
	var sum float64
	for _, v := range sorted {
		sum += v
	}
	q := func(p float64) float64 { return sorted[int(p*float64(len(sorted)-1))] }
	return result{q(.50), q(.95), q(.99), sum / float64(len(sorted)), float64(len(keys)) / elapsed.Seconds()}
}

func report(name string, r result) {
	fmt.Printf("%-28s %9.3f %9.3f %9.3f %9.3f %10.0f\n", name, r.p50, r.p95, r.p99, r.mean, r.rps)
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
