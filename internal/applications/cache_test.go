package applications

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	appsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/applications/v1"
	eventsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/events/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ddb"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/testutil"
)

func TestInvalidateOnApplicationEvents(t *testing.T) {
	c, prefix := testutil.Tables(t, ddb.CacheOff)
	table := prefix + ddb.TableApplications
	cache := ddb.NewCachedAPI(c.Raw, ddb.CacheOptions{})
	get := func() {
		if _, err := cache.GetItem(context.Background(), &dynamodb.GetItemInput{TableName: aws.String(table), Key: appKey("s1", "a1")}); err != nil {
			t.Fatal(err)
		}
	}
	event := func(typ string, m proto.Message) *eventsv1.Event {
		p, _ := anypb.New(m)
		return &eventsv1.Event{Type: typ, AggregateId: "a1", Payload: p}
	}
	inv := InvalidateOn(cache, table)
	get()
	inv(event(EventApplicationUpserted, &appsv1.ApplicationUpserted{Application: &appsv1.Application{Id: "a1", StudentId: "s1"}}))
	get()
	inv(event(EventApplicationDeleted, &appsv1.ApplicationDeleted{ApplicationId: "a1", StudentId: "s1"}))
	get()
	inv(event(EventApplicationUpserted, &appsv1.ApplicationUpserted{Application: &appsv1.Application{Id: "a1", StudentId: "other"}}))
	get()
	if m := cache.ItemStats().Misses; m != 3 {
		t.Fatalf("misses = %d, want 3 (initial + upsert + delete; another student's event must not invalidate)", m)
	}
}
