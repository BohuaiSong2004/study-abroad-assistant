package events

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"google.golang.org/protobuf/proto"

	eventsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/events/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ddb"
)

// DeadLetter is an event parked after a permanent handler error.
type DeadLetter struct {
	Consumer string
	Event    *eventsv1.Event
	Error    string
	ParkedAt time.Time
}

// ErrNoDeadLetter is returned by Replay for an unknown sequence number.
var ErrNoDeadLetter = errors.New("no such dead letter")

// Name returns the consumer's name.
func (c *Consumer) Name() string { return c.name }

func (c *Consumer) dlqPK() types.AttributeValue { return ddb.S("DLQ#" + c.name) }

// DeadLetters lists this consumer's parked events, oldest first.
func (c *Consumer) DeadLetters(ctx context.Context, limit int32) ([]DeadLetter, error) {
	out, err := c.api.Query(ctx, &dynamodb.QueryInput{
		TableName: aws.String(c.table), KeyConditionExpression: aws.String("pk = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":pk": c.dlqPK()},
		ConsistentRead:            aws.Bool(true), Limit: aws.Int32(limit),
	})
	if err != nil {
		return nil, err
	}
	res := make([]DeadLetter, 0, len(out.Items))
	for _, it := range out.Items {
		var ev eventsv1.Event
		if err := proto.Unmarshal(ddb.GetB(it, "event"), &ev); err != nil {
			return nil, fmt.Errorf("decode dead letter: %w", err)
		}
		res = append(res, DeadLetter{Consumer: c.name, Event: &ev, Error: ddb.GetS(it, "error"), ParkedAt: time.UnixMilli(ddb.GetN(it, "at_ms"))})
	}
	return res, nil
}

// CountDeadLetters returns how many events are parked.
func (c *Consumer) CountDeadLetters(ctx context.Context) (int64, error) {
	in := &dynamodb.QueryInput{
		TableName: aws.String(c.table), KeyConditionExpression: aws.String("pk = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":pk": c.dlqPK()},
		ConsistentRead:            aws.Bool(true), Select: types.SelectCount,
	}
	var n int64
	for {
		out, err := c.api.Query(ctx, in)
		if err != nil {
			return 0, err
		}
		n += int64(out.Count)
		if out.LastEvaluatedKey == nil {
			return n, nil
		}
		in.ExclusiveStartKey = out.LastEvaluatedKey
	}
}

// Replay runs the handler once more for a parked event, typically after the
// cause was fixed (missing reference data, a deployed bug fix). On success
// the dead letter is removed; on failure it stays, with the new error.
// Handlers are idempotent, so replaying concurrently with the live stream is
// safe.
func (c *Consumer) Replay(ctx context.Context, seq int64) error {
	key := map[string]types.AttributeValue{ddb.PK: c.dlqPK(), ddb.SK: ddb.S(ddb.Pad(seq))}
	got, err := c.api.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(c.table), Key: key, ConsistentRead: aws.Bool(true)})
	if err != nil {
		return err
	}
	if got.Item == nil {
		return ErrNoDeadLetter
	}
	var ev eventsv1.Event
	if err := proto.Unmarshal(ddb.GetB(got.Item, "event"), &ev); err != nil {
		return Permanent(fmt.Errorf("decode dead letter: %w", err))
	}
	if herr := c.handler(ctx, &ev); herr != nil {
		_, err := c.api.UpdateItem(ctx, &dynamodb.UpdateItemInput{
			TableName: aws.String(c.table), Key: key,
			UpdateExpression:          aws.String("SET #e = :e, replayed_ms = :now"),
			ExpressionAttributeNames:  map[string]string{"#e": "error"},
			ExpressionAttributeValues: map[string]types.AttributeValue{":e": ddb.S(herr.Error()), ":now": ddb.N(time.Now().UnixMilli())},
		})
		return errors.Join(herr, err)
	}
	c.log.Info("dead letter replayed", "seq", seq, "type", ev.Type)
	_, err = c.api.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: aws.String(c.table), Key: key})
	return err
}
