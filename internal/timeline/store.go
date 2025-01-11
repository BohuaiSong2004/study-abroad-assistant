package timeline

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"google.golang.org/protobuf/proto"

	appsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/applications/v1"
	catalogv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/catalog/v1"
	timelinev1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/timeline/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ddb"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/events"
)

// EventTimelineChanged is published on the timeline stream.
const EventTimelineChanged = "timeline.changed"

var (
	errNotFound = errors.New("timeline not found")
	errConflict = errors.New("timeline version conflict")
)

// Store persists timelines and the program projection.
//
//	pk=STUDENT#<sid> sk=TL#<aid>   timeline + application snapshot (protobuf), versions
//	                gsi1pk=PROGRAM#<pid> gsi1sk=TL#<aid>  (re-plan on program change)
//	pk=PROG#<pid>    sk=META       latest program snapshot seen on the catalog stream
type Store struct {
	api    ddb.API // cached read path
	raw    ddb.API // uncached: GSI scans that must see recent writes
	table  string
	outbox *events.Outbox
}

// NewStore creates a store.
func NewStore(api, raw ddb.API, table string, ob *events.Outbox) *Store {
	return &Store{api: api, raw: raw, table: table, outbox: ob}
}

func tlKey(sid, aid string) map[string]types.AttributeValue {
	return ddb.Key("STUDENT#"+sid, "TL#"+aid)
}
func progKey(pid string) map[string]types.AttributeValue { return ddb.Key("PROG#"+pid, "META") }

// record is a stored timeline plus the application snapshot it was planned
// from (needed to re-plan when only the program changes).
type record struct {
	tl  *timelinev1.Timeline
	app *appsv1.Application
}

func decodeTL(it map[string]types.AttributeValue) (*timelinev1.Timeline, error) {
	var t timelinev1.Timeline
	if err := proto.Unmarshal(ddb.GetB(it, "tl"), &t); err != nil {
		return nil, fmt.Errorf("decode timeline: %w", err)
	}
	return &t, nil
}

// Get loads a timeline. consistent=false may be served from the cache.
func (s *Store) Get(ctx context.Context, sid, aid string, consistent bool) (*record, error) {
	out, err := s.api.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(s.table), Key: tlKey(sid, aid), ConsistentRead: aws.Bool(consistent)})
	if err != nil {
		return nil, err
	}
	if out.Item == nil {
		return nil, errNotFound
	}
	t, err := decodeTL(out.Item)
	if err != nil {
		return nil, err
	}
	var a appsv1.Application
	if err := proto.Unmarshal(ddb.GetB(out.Item, "app"), &a); err != nil {
		return nil, fmt.Errorf("decode application snapshot: %w", err)
	}
	return &record{tl: t, app: &a}, nil
}

// List returns all of a student's timelines (eventually consistent; may be
// served from the query cache).
func (s *Store) List(ctx context.Context, sid string) ([]*timelinev1.Timeline, error) {
	in := &dynamodb.QueryInput{
		TableName:              aws.String(s.table),
		KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :p)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": ddb.S("STUDENT#" + sid), ":p": ddb.S("TL#"),
		},
	}
	var res []*timelinev1.Timeline
	for {
		out, err := s.api.Query(ctx, in)
		if err != nil {
			return nil, err
		}
		for _, it := range out.Items {
			t, err := decodeTL(it)
			if err != nil {
				return nil, err
			}
			res = append(res, t)
		}
		if out.LastEvaluatedKey == nil {
			return res, nil
		}
		in.ExclusiveStartKey = out.LastEvaluatedKey
	}
}

// Put writes r (whose timeline Version must already be bumped) if the stored
// version equals prev (0 = must not exist), with a TimelineChanged event.
func (s *Store) Put(ctx context.Context, r *record, prev int64) error {
	t := r.tl
	raw, err := proto.Marshal(t)
	if err != nil {
		return err
	}
	rawApp, err := proto.Marshal(r.app)
	if err != nil {
		return err
	}
	item := tlKey(t.StudentId, t.ApplicationId)
	item[ddb.GSI1PK] = ddb.S("PROGRAM#" + t.ProgramId)
	item[ddb.GSI1SK] = ddb.S("TL#" + t.ApplicationId)
	item["tl"] = ddb.B(raw)
	item["app"] = ddb.B(rawApp)
	item["version"] = ddb.N(t.Version)
	item["app_version"] = ddb.N(t.ApplicationVersion)
	item["program_version"] = ddb.N(t.ProgramVersion)
	put := &types.Put{TableName: aws.String(s.table), Item: item}
	if prev == 0 {
		put.ConditionExpression = aws.String("attribute_not_exists(pk)")
	} else {
		put.ConditionExpression = aws.String("version = :v")
		put.ExpressionAttributeValues = map[string]types.AttributeValue{":v": ddb.N(prev)}
	}
	err = s.outbox.Commit(ctx, []types.TransactWriteItem{{Put: put}}, events.Spec{
		Type: EventTimelineChanged, AggregateID: t.ApplicationId,
		Payload: &timelinev1.TimelineChanged{ApplicationId: t.ApplicationId, StudentId: t.StudentId, Version: t.Version},
	})
	if ddb.IsConditionFailed(err) {
		return errConflict
	}
	return err
}

// Delete removes a timeline if it reflects an application version older
// than appVersion.
func (s *Store) Delete(ctx context.Context, sid, aid string, appVersion int64) (bool, error) {
	err := s.outbox.Commit(ctx, []types.TransactWriteItem{{Delete: &types.Delete{
		TableName: aws.String(s.table), Key: tlKey(sid, aid),
		ConditionExpression:       aws.String("attribute_exists(pk) AND app_version < :v"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":v": ddb.N(appVersion)},
	}}}, events.Spec{
		Type: EventTimelineChanged, AggregateID: aid,
		Payload: &timelinev1.TimelineChanged{ApplicationId: aid, StudentId: sid, Deleted: true},
	})
	if ddb.IsConditionFailed(err) {
		return false, nil
	}
	return err == nil, err
}

// Program returns the projected program, or nil.
func (s *Store) Program(ctx context.Context, pid string) (*catalogv1.Program, error) {
	out, err := s.api.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(s.table), Key: progKey(pid)})
	if err != nil || out.Item == nil {
		return nil, err
	}
	var p catalogv1.Program
	if err := proto.Unmarshal(ddb.GetB(out.Item, "program"), &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// PutProgram stores p if it is newer than the projected version. It reports
// whether the projection changed.
func (s *Store) PutProgram(ctx context.Context, p *catalogv1.Program) (bool, error) {
	raw, err := proto.Marshal(p)
	if err != nil {
		return false, err
	}
	item := progKey(p.Id)
	item["program"] = ddb.B(raw)
	item["version"] = ddb.N(p.Version)
	_, err = s.api.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(s.table), Item: item,
		ConditionExpression:       aws.String("attribute_not_exists(pk) OR version < :v"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":v": ddb.N(p.Version)},
	})
	if ddb.IsConditionFailed(err) {
		return false, nil
	}
	return err == nil, err
}

// StaleForProgram lists (student, application) pairs whose timeline was
// planned with a program version older than version. It reads the GSI with
// the raw client: a cached result could hide a timeline created moments ago.
func (s *Store) StaleForProgram(ctx context.Context, pid string, version int64) ([][2]string, error) {
	in := &dynamodb.QueryInput{
		TableName:              aws.String(s.table),
		IndexName:              aws.String(ddb.GSI1),
		KeyConditionExpression: aws.String("gsi1pk = :p"),
		FilterExpression:       aws.String("program_version < :v"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":p": ddb.S("PROGRAM#" + pid), ":v": ddb.N(version),
		},
		ProjectionExpression: aws.String("pk, sk"),
	}
	var res [][2]string
	for {
		out, err := s.raw.Query(ctx, in)
		if err != nil {
			return nil, err
		}
		for _, it := range out.Items {
			res = append(res, [2]string{
				strings.TrimPrefix(ddb.GetS(it, ddb.PK), "STUDENT#"),
				strings.TrimPrefix(ddb.GetS(it, ddb.SK), "TL#"),
			})
		}
		if out.LastEvaluatedKey == nil {
			return res, nil
		}
		in.ExclusiveStartKey = out.LastEvaluatedKey
	}
}
