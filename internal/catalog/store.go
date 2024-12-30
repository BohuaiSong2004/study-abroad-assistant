// Package catalog implements CatalogService: programs, their rounds
// (deadlines) and admission requirements.
package catalog

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"google.golang.org/protobuf/proto"

	catalogv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/catalog/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ddb"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/events"
)

// Event types on the catalog stream.
const EventProgramUpserted = "catalog.program_upserted"

// Errors returned by the store.
var (
	ErrNotFound        = errors.New("program not found")
	ErrExists          = errors.New("program already exists")
	ErrVersionConflict = errors.New("program was modified concurrently")
	ErrBadPageToken    = errors.New("invalid page token")
)

// Store persists programs in the catalog table.
//
// Item layout (single table):
//
//	pk=PROGRAM#<id> sk=META   program (protobuf bytes), version, filter attrs
//	gsi1pk=PROGRAMS gsi1sk=<university>#<name>#<id> (lower-cased) for listing
//	pk=EVT#catalog sk=<seq>   outbox
type Store struct {
	api    ddb.API
	table  string
	outbox *events.Outbox
}

// NewStore creates a store.
func NewStore(api ddb.API, table string, ob *events.Outbox) *Store {
	return &Store{api: api, table: table, outbox: ob}
}

func programKey(id string) map[string]types.AttributeValue { return ddb.Key("PROGRAM#"+id, "META") }

func sortKey(p *catalogv1.Program) string {
	return strings.ToLower(p.University) + "#" + strings.ToLower(p.Name) + "#" + p.Id
}

func searchText(p *catalogv1.Program) string {
	return strings.ToLower(strings.Join([]string{p.University, p.Name, p.Field, p.City, p.Country, strings.Join(p.Tags, " ")}, " "))
}

func toItem(p *catalogv1.Program) (map[string]types.AttributeValue, error) {
	raw, err := proto.Marshal(p)
	if err != nil {
		return nil, err
	}
	item := programKey(p.Id)
	item[ddb.GSI1PK] = ddb.S("PROGRAMS")
	item[ddb.GSI1SK] = ddb.S(sortKey(p))
	item["program"] = ddb.B(raw)
	item["version"] = ddb.N(p.Version)
	item["search"] = ddb.S(searchText(p))
	item["country"] = ddb.S(strings.ToLower(p.Country))
	item["field"] = ddb.S(strings.ToLower(p.Field))
	item["degree"] = ddb.N(int64(p.Degree))
	return item, nil
}

func fromItem(item map[string]types.AttributeValue) (*catalogv1.Program, error) {
	var p catalogv1.Program
	if err := proto.Unmarshal(ddb.GetB(item, "program"), &p); err != nil {
		return nil, fmt.Errorf("decode program: %w", err)
	}
	return &p, nil
}

// Get returns a program. consistent=false may be served by the cache.
func (s *Store) Get(ctx context.Context, id string, consistent bool) (*catalogv1.Program, error) {
	out, err := s.api.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.table), Key: programKey(id), ConsistentRead: aws.Bool(consistent),
	})
	if err != nil {
		return nil, err
	}
	if out.Item == nil {
		return nil, ErrNotFound
	}
	return fromItem(out.Item)
}

// BatchGet returns the programs that exist among ids, in no particular order.
func (s *Store) BatchGet(ctx context.Context, idList []string) ([]*catalogv1.Program, error) {
	seen := map[string]bool{}
	var keys []map[string]types.AttributeValue
	for _, id := range idList {
		if !seen[id] {
			seen[id] = true
			keys = append(keys, programKey(id))
		}
	}
	var res []*catalogv1.Program
	for len(keys) > 0 {
		n := min(len(keys), 100)
		req := map[string]types.KeysAndAttributes{s.table: {Keys: keys[:n]}}
		keys = keys[n:]
		for attempt := 0; len(req) > 0; attempt++ {
			if attempt > 5 {
				return nil, errors.New("batch get: unprocessed keys after retries")
			}
			out, err := s.api.BatchGetItem(ctx, &dynamodb.BatchGetItemInput{RequestItems: req})
			if err != nil {
				return nil, err
			}
			for _, it := range out.Responses[s.table] {
				p, err := fromItem(it)
				if err != nil {
					return nil, err
				}
				res = append(res, p)
			}
			req = out.UnprocessedKeys
		}
	}
	return res, nil
}

// Create inserts a new program (version 1) and publishes ProgramUpserted.
func (s *Store) Create(ctx context.Context, p *catalogv1.Program) error {
	item, err := toItem(p)
	if err != nil {
		return err
	}
	err = s.outbox.Commit(ctx, []types.TransactWriteItem{{Put: &types.Put{
		TableName: aws.String(s.table), Item: item,
		ConditionExpression: aws.String("attribute_not_exists(pk)"),
	}}}, events.Spec{Type: EventProgramUpserted, AggregateID: p.Id, Payload: &catalogv1.ProgramUpserted{Program: p}})
	if ddb.IsConditionFailed(err) {
		return ErrExists
	}
	return err
}

// Update replaces a program if its stored version equals expected.
func (s *Store) Update(ctx context.Context, p *catalogv1.Program, expected int64) error {
	item, err := toItem(p)
	if err != nil {
		return err
	}
	err = s.outbox.Commit(ctx, []types.TransactWriteItem{{Put: &types.Put{
		TableName: aws.String(s.table), Item: item,
		ConditionExpression:       aws.String("attribute_exists(pk) AND version = :v"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":v": ddb.N(expected)},
	}}}, events.Spec{Type: EventProgramUpserted, AggregateID: p.Id, Payload: &catalogv1.ProgramUpserted{Program: p}})
	if ddb.IsConditionFailed(err) {
		return ErrVersionConflict
	}
	return err
}

// Filter narrows List.
type Filter struct {
	Query, Country, Field string
	Degree                catalogv1.Degree
}

type pageToken struct {
	SK string `json:"s"`
	ID string `json:"i"`
}

func encodeToken(p *catalogv1.Program) string {
	b, _ := json.Marshal(pageToken{SK: sortKey(p), ID: p.Id})
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeToken(s string) (map[string]types.AttributeValue, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, ErrBadPageToken
	}
	var t pageToken
	if err := json.Unmarshal(b, &t); err != nil || t.ID == "" || !strings.HasSuffix(t.SK, "#"+t.ID) {
		return nil, ErrBadPageToken
	}
	k := programKey(t.ID)
	k[ddb.GSI1PK] = ddb.S("PROGRAMS")
	k[ddb.GSI1SK] = ddb.S(t.SK)
	return k, nil
}

// List returns programs ordered by university then name.
//
// Filters are DynamoDB FilterExpressions over the listing index, so one
// DynamoDB page can return fewer matches than requested; List keeps reading
// until the page is full. The continuation token is the key of the last
// program returned (not DynamoDB's LastEvaluatedKey, which may point past
// matches we did not return). This is a full index walk per search, fine for
// a catalog of a few thousand programs and served from the query cache when
// repeated; a much larger catalog would move search to OpenSearch.
func (s *Store) List(ctx context.Context, f Filter, pageSize int, token string) ([]*catalogv1.Program, string, error) {
	var start map[string]types.AttributeValue
	if token != "" {
		var err error
		if start, err = decodeToken(token); err != nil {
			return nil, "", err
		}
	}
	names := map[string]string{}
	values := map[string]types.AttributeValue{":p": ddb.S("PROGRAMS")}
	var conds []string
	if q := strings.ToLower(strings.TrimSpace(f.Query)); q != "" {
		for i, term := range strings.Fields(q) {
			if i == 5 {
				break
			}
			k := fmt.Sprintf(":q%d", i)
			conds = append(conds, "contains(#search, "+k+")")
			values[k] = ddb.S(term)
		}
		names["#search"] = "search"
	}
	if f.Country != "" {
		conds = append(conds, "#country = :country")
		names["#country"] = "country"
		values[":country"] = ddb.S(strings.ToLower(f.Country))
	}
	if f.Field != "" {
		conds = append(conds, "#field = :field")
		names["#field"] = "field"
		values[":field"] = ddb.S(strings.ToLower(f.Field))
	}
	if f.Degree != catalogv1.Degree_DEGREE_UNSPECIFIED {
		conds = append(conds, "#degree = :degree")
		names["#degree"] = "degree"
		values[":degree"] = ddb.N(int64(f.Degree))
	}
	in := &dynamodb.QueryInput{
		TableName:                 aws.String(s.table),
		IndexName:                 aws.String(ddb.GSI1),
		KeyConditionExpression:    aws.String("gsi1pk = :p"),
		ExpressionAttributeValues: values,
		Limit:                     aws.Int32(100),
	}
	if len(conds) > 0 {
		in.FilterExpression = aws.String(strings.Join(conds, " AND "))
		in.ExpressionAttributeNames = names
	}
	var res []*catalogv1.Program
	for {
		in.ExclusiveStartKey = start
		out, err := s.api.Query(ctx, in)
		if err != nil {
			return nil, "", err
		}
		for i, it := range out.Items {
			p, err := fromItem(it)
			if err != nil {
				return nil, "", err
			}
			res = append(res, p)
			if len(res) == pageSize {
				if i == len(out.Items)-1 && out.LastEvaluatedKey == nil {
					return res, "", nil // nothing after this item
				}
				return res, encodeToken(p), nil
			}
		}
		if out.LastEvaluatedKey == nil {
			return res, "", nil
		}
		start = out.LastEvaluatedKey
	}
}

// Facets returns the distinct countries and fields (display casing).
func (s *Store) Facets(ctx context.Context) (countries, fields []string, err error) {
	in := &dynamodb.QueryInput{
		TableName:                 aws.String(s.table),
		IndexName:                 aws.String(ddb.GSI1),
		KeyConditionExpression:    aws.String("gsi1pk = :p"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":p": ddb.S("PROGRAMS")},
	}
	cs, fs := map[string]string{}, map[string]string{}
	for {
		out, err := s.api.Query(ctx, in)
		if err != nil {
			return nil, nil, err
		}
		for _, it := range out.Items {
			p, err := fromItem(it)
			if err != nil {
				return nil, nil, err
			}
			cs[strings.ToLower(p.Country)] = p.Country
			fs[strings.ToLower(p.Field)] = p.Field
		}
		if out.LastEvaluatedKey == nil {
			break
		}
		in.ExclusiveStartKey = out.LastEvaluatedKey
	}
	for _, v := range cs {
		countries = append(countries, v)
	}
	for _, v := range fs {
		fields = append(fields, v)
	}
	return countries, fields, nil
}
