package catalog

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	eventsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/events/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ddb"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/testutil"
)

func TestInvalidateOnProgramEvent(t *testing.T) {
	c, prefix := testutil.Tables(t, ddb.CacheOff)
	table := prefix + ddb.TableCatalog
	cache := ddb.NewCachedAPI(c.Raw, ddb.CacheOptions{})
	get := func() {
		if _, err := cache.GetItem(context.Background(), &dynamodb.GetItemInput{TableName: aws.String(table), Key: programKey("p1")}); err != nil {
			t.Fatal(err)
		}
	}
	get()
	get()
	if cache.ItemStats().Misses != 1 {
		t.Fatalf("setup: %+v", cache.ItemStats())
	}
	InvalidateOn(cache, table)(&eventsv1.Event{Type: "unrelated", AggregateId: "p1"})
	get()
	if cache.ItemStats().Misses != 1 {
		t.Fatal("unrelated event invalidated the program")
	}
	InvalidateOn(cache, table)(&eventsv1.Event{Type: EventProgramUpserted, AggregateId: "p1"})
	get()
	if cache.ItemStats().Misses != 2 {
		t.Fatal("ProgramUpserted did not invalidate the cached program")
	}
}
