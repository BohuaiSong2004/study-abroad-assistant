package applications

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
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ddb"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/events"
)

// Event types on the applications stream.
const (
	EventApplicationUpserted = "applications.application_upserted"
	EventApplicationDeleted  = "applications.application_deleted"
)

var (
	errNotFound     = errors.New("not found")
	errConflict     = errors.New("version conflict")
	errDuplicate    = errors.New("duplicate")
	errPrecondition = errors.New("precondition failed")
)

// Store persists applications and recommendations. Everything a student
// owns lives under their partition, so ownership checks are structural: a
// student can only ever address keys under STUDENT#<their id>.
//
//	pk=STUDENT#<sid> sk=APP#<aid>          application (protobuf), version
//	pk=STUDENT#<sid> sk=PROGRAM#<pid>      one application per program guard
//	pk=STUDENT#<sid> sk=REC#<aid>#<rid>    recommendation (protobuf), status
//	pk=RECTOKEN#<sha256> sk=TOKEN          invitation token -> (sid, aid, rid)
type Store struct {
	api    ddb.API
	table  string
	outbox *events.Outbox
}

// NewStore creates a store.
func NewStore(api ddb.API, table string, ob *events.Outbox) *Store {
	return &Store{api: api, table: table, outbox: ob}
}

func studentPK(sid string) string { return "STUDENT#" + sid }
func appKey(sid, aid string) map[string]types.AttributeValue {
	return ddb.Key(studentPK(sid), "APP#"+aid)
}
func guardKey(sid, pid string) map[string]types.AttributeValue {
	return ddb.Key(studentPK(sid), "PROGRAM#"+pid)
}
func recKey(sid, aid, rid string) map[string]types.AttributeValue {
	return ddb.Key(studentPK(sid), "REC#"+aid+"#"+rid)
}
func tokenKey(hash string) map[string]types.AttributeValue { return ddb.Key("RECTOKEN#"+hash, "TOKEN") }

func (s *Store) appPut(a *appsv1.Application, expected int64) (types.TransactWriteItem, error) {
	raw, err := proto.Marshal(a)
	if err != nil {
		return types.TransactWriteItem{}, err
	}
	item := appKey(a.StudentId, a.Id)
	item["app"] = ddb.B(raw)
	item["version"] = ddb.N(a.Version)
	item["program_id"] = ddb.S(a.ProgramId)
	put := &types.Put{TableName: aws.String(s.table), Item: item}
	if expected == 0 {
		put.ConditionExpression = aws.String("attribute_not_exists(pk)")
	} else {
		put.ConditionExpression = aws.String("version = :v")
		put.ExpressionAttributeValues = map[string]types.AttributeValue{":v": ddb.N(expected)}
	}
	return types.TransactWriteItem{Put: put}, nil
}

func upserted(a *appsv1.Application) events.Spec {
	return events.Spec{Type: EventApplicationUpserted, AggregateID: a.Id, Payload: &appsv1.ApplicationUpserted{Application: a}}
}

func decodeApp(item map[string]types.AttributeValue) (*appsv1.Application, error) {
	var a appsv1.Application
	if err := proto.Unmarshal(ddb.GetB(item, "app"), &a); err != nil {
		return nil, fmt.Errorf("decode application: %w", err)
	}
	return &a, nil
}

// GetApp loads an application.
func (s *Store) GetApp(ctx context.Context, sid, aid string, consistent bool) (*appsv1.Application, error) {
	out, err := s.api.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(s.table), Key: appKey(sid, aid), ConsistentRead: aws.Bool(consistent)})
	if err != nil {
		return nil, err
	}
	if out.Item == nil {
		return nil, errNotFound
	}
	return decodeApp(out.Item)
}

// CreateApp inserts an application and its per-program guard.
func (s *Store) CreateApp(ctx context.Context, a *appsv1.Application) error {
	put, err := s.appPut(a, 0)
	if err != nil {
		return err
	}
	guard := guardKey(a.StudentId, a.ProgramId)
	guard["app_id"] = ddb.S(a.Id)
	err = s.outbox.Commit(ctx, []types.TransactWriteItem{
		put,
		{Put: &types.Put{TableName: aws.String(s.table), Item: guard, ConditionExpression: aws.String("attribute_not_exists(pk)")}},
	}, upserted(a))
	if ddb.IsConditionFailed(err) {
		return errDuplicate
	}
	return err
}

// ListApps returns a page of a student's applications (strongly consistent:
// a student must see the application they just created).
func (s *Store) ListApps(ctx context.Context, sid string, limit int32, start map[string]types.AttributeValue) ([]*appsv1.Application, map[string]types.AttributeValue, error) {
	out, err := s.api.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(s.table),
		KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :p)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": ddb.S(studentPK(sid)), ":p": ddb.S("APP#"),
		},
		ConsistentRead:    aws.Bool(true),
		Limit:             aws.Int32(limit),
		ExclusiveStartKey: start,
		ScanIndexForward:  aws.Bool(false), // ids are time-ordered: newest first
	})
	if err != nil {
		return nil, nil, err
	}
	apps := make([]*appsv1.Application, 0, len(out.Items))
	for _, it := range out.Items {
		a, err := decodeApp(it)
		if err != nil {
			return nil, nil, err
		}
		apps = append(apps, a)
	}
	return apps, out.LastEvaluatedKey, nil
}

// CountApps counts a student's applications.
func (s *Store) CountApps(ctx context.Context, sid string) (int, error) {
	out, err := s.api.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(s.table),
		KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :p)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": ddb.S(studentPK(sid)), ":p": ddb.S("APP#"),
		},
		Select: types.SelectCount, ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return 0, err
	}
	return int(out.Count), nil
}

// Mutate performs an optimistic read-modify-write of an application. fn
// mutates the application (whose version is then bumped) and may return
// extra transaction items; the whole thing commits atomically with an
// ApplicationUpserted event. If a concurrent writer bumped the version first,
// Mutate re-reads and re-applies fn (up to 5 times). If expected > 0 the
// caller's version must match the stored one (client-side optimistic lock).
func (s *Store) Mutate(ctx context.Context, sid, aid string, expected int64, fn func(a *appsv1.Application) ([]types.TransactWriteItem, error)) (*appsv1.Application, error) {
	for attempt := 0; attempt < 5; attempt++ {
		a, err := s.GetApp(ctx, sid, aid, true)
		if err != nil {
			return nil, err
		}
		if expected > 0 && a.Version != expected {
			return nil, errConflict
		}
		cur := a.Version
		extra, err := fn(a)
		if err != nil {
			return nil, err
		}
		a.Version = cur + 1
		put, err := s.appPut(a, cur)
		if err != nil {
			return nil, err
		}
		err = s.outbox.Commit(ctx, append([]types.TransactWriteItem{put}, extra...), upserted(a))
		if err == nil {
			return a, nil
		}
		switch idx := ddb.CancellationIndex(err); {
		case idx == 0 && expected == 0:
			continue // lost a race with another writer: retry on fresh state
		case idx == 0:
			return nil, errConflict
		case idx > 0:
			return nil, errPrecondition
		}
		if ddb.IsRetryableConflict(err) {
			continue
		}
		return nil, err
	}
	return nil, errConflict
}

// DeleteApp removes an application, its guard, recommendations and tokens.
func (s *Store) DeleteApp(ctx context.Context, a *appsv1.Application, recs []*storedRec) error {
	items := []types.TransactWriteItem{
		{Delete: &types.Delete{TableName: aws.String(s.table), Key: appKey(a.StudentId, a.Id),
			ConditionExpression: aws.String("version = :v"), ExpressionAttributeValues: map[string]types.AttributeValue{":v": ddb.N(a.Version)}}},
		{Delete: &types.Delete{TableName: aws.String(s.table), Key: guardKey(a.StudentId, a.ProgramId)}},
	}
	for _, r := range recs {
		items = append(items, types.TransactWriteItem{Delete: &types.Delete{TableName: aws.String(s.table), Key: recKey(a.StudentId, a.Id, r.rec.Id)}})
		if r.tokenHash != "" {
			items = append(items, types.TransactWriteItem{Delete: &types.Delete{TableName: aws.String(s.table), Key: tokenKey(r.tokenHash)}})
		}
	}
	err := s.outbox.Commit(ctx, items, events.Spec{
		Type: EventApplicationDeleted, AggregateID: a.Id,
		Payload: &appsv1.ApplicationDeleted{ApplicationId: a.Id, StudentId: a.StudentId, Version: a.Version + 1},
	})
	if ddb.IsConditionFailed(err) {
		return errConflict
	}
	return err
}

// storedRec is a recommendation with its storage-only fields.
type storedRec struct {
	rec       *appsv1.Recommendation
	sid       string
	tokenHash string
}

func (s *Store) recPut(sid string, r *appsv1.Recommendation, tokenHash string, cond *types.Put) (types.TransactWriteItem, error) {
	raw, err := proto.Marshal(r)
	if err != nil {
		return types.TransactWriteItem{}, err
	}
	item := recKey(sid, r.ApplicationId, r.Id)
	item["rec"] = ddb.B(raw)
	item["status"] = ddb.N(int64(r.Status))
	if tokenHash != "" {
		item["token_hash"] = ddb.S(tokenHash)
	}
	put := &types.Put{TableName: aws.String(s.table), Item: item}
	if cond != nil {
		put.ConditionExpression = cond.ConditionExpression
		put.ExpressionAttributeValues = cond.ExpressionAttributeValues
	}
	return types.TransactWriteItem{Put: put}, nil
}

func decodeRec(sid string, it map[string]types.AttributeValue) (*storedRec, error) {
	var r appsv1.Recommendation
	if err := proto.Unmarshal(ddb.GetB(it, "rec"), &r); err != nil {
		return nil, fmt.Errorf("decode recommendation: %w", err)
	}
	return &storedRec{rec: &r, sid: sid, tokenHash: ddb.GetS(it, "token_hash")}, nil
}

// ListRecs returns an application's recommendations, oldest first.
func (s *Store) ListRecs(ctx context.Context, sid, aid string) ([]*storedRec, error) {
	out, err := s.api.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(s.table),
		KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :p)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": ddb.S(studentPK(sid)), ":p": ddb.S("REC#" + aid + "#"),
		},
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return nil, err
	}
	res := make([]*storedRec, 0, len(out.Items))
	for _, it := range out.Items {
		r, err := decodeRec(sid, it)
		if err != nil {
			return nil, err
		}
		res = append(res, r)
	}
	return res, nil
}

// GetRec loads one recommendation.
func (s *Store) GetRec(ctx context.Context, sid, aid, rid string) (*storedRec, error) {
	out, err := s.api.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(s.table), Key: recKey(sid, aid, rid), ConsistentRead: aws.Bool(true)})
	if err != nil {
		return nil, err
	}
	if out.Item == nil {
		return nil, errNotFound
	}
	return decodeRec(sid, out.Item)
}

// tokenRef is what an invitation token points to.
type tokenRef struct {
	sid, aid, rid string
	expiresMs     int64
}

// GetToken resolves an invitation token hash (strongly consistent, so a
// cancelled or regenerated link stops working immediately).
func (s *Store) GetToken(ctx context.Context, hash string) (*tokenRef, error) {
	out, err := s.api.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(s.table), Key: tokenKey(hash), ConsistentRead: aws.Bool(true)})
	if err != nil {
		return nil, err
	}
	if out.Item == nil {
		return nil, errNotFound
	}
	ref := strings.Split(ddb.GetS(out.Item, "ref"), "/")
	if len(ref) != 3 {
		return nil, errNotFound
	}
	return &tokenRef{sid: ref[0], aid: ref[1], rid: ref[2], expiresMs: ddb.GetN(out.Item, "expires_ms")}, nil
}

func (s *Store) tokenPut(hash string, sid, aid, rid string, expiresMs int64) types.TransactWriteItem {
	item := tokenKey(hash)
	item["ref"] = ddb.S(sid + "/" + aid + "/" + rid)
	item["expires_ms"] = ddb.N(expiresMs)
	// Keep the item a while after expiry so the recommender sees "expired"
	// rather than "not found".
	item[ddb.TTL] = ddb.N(expiresMs/1000 + 30*24*3600)
	return types.TransactWriteItem{Put: &types.Put{TableName: aws.String(s.table), Item: item, ConditionExpression: aws.String("attribute_not_exists(pk)")}}
}

func (s *Store) tokenDelete(hash string) types.TransactWriteItem {
	return types.TransactWriteItem{Delete: &types.Delete{TableName: aws.String(s.table), Key: tokenKey(hash)}}
}
