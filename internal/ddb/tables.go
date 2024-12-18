package ddb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// Table names (without prefix). Each service owns exactly one table and no
// service reads another service's table; data crosses service boundaries only
// through gRPC calls and event streams.
const (
	TableCatalog      = "catalog"
	TableApplications = "applications"
	TableTimeline     = "timeline"
)

// AllTables lists every table the system uses.
var AllTables = []string{TableCatalog, TableApplications, TableTimeline}

// TableSpec returns the CreateTable input for a table. All tables share the
// same shape (pk/sk + one overloaded GSI), which keeps local bootstrap and the
// Terraform definition in deploy/terraform identical.
func TableSpec(fullName string) *dynamodb.CreateTableInput {
	return &dynamodb.CreateTableInput{
		TableName:   aws.String(fullName),
		BillingMode: types.BillingModePayPerRequest,
		AttributeDefinitions: []types.AttributeDefinition{
			{AttributeName: aws.String(PK), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String(SK), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String(GSI1PK), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String(GSI1SK), AttributeType: types.ScalarAttributeTypeS},
		},
		KeySchema: []types.KeySchemaElement{
			{AttributeName: aws.String(PK), KeyType: types.KeyTypeHash},
			{AttributeName: aws.String(SK), KeyType: types.KeyTypeRange},
		},
		GlobalSecondaryIndexes: []types.GlobalSecondaryIndex{{
			IndexName: aws.String(GSI1),
			KeySchema: []types.KeySchemaElement{
				{AttributeName: aws.String(GSI1PK), KeyType: types.KeyTypeHash},
				{AttributeName: aws.String(GSI1SK), KeyType: types.KeyTypeRange},
			},
			Projection: &types.Projection{ProjectionType: types.ProjectionTypeAll},
		}},
	}
}

// EnsureTables creates any missing tables and enables TTL. It is idempotent
// and safe to run from several processes at once.
func EnsureTables(ctx context.Context, raw *dynamodb.Client, prefix string, names ...string) error {
	if len(names) == 0 {
		names = AllTables
	}
	for _, n := range names {
		full := prefix + n
		_, err := raw.CreateTable(ctx, TableSpec(full))
		var inUse *types.ResourceInUseException
		if err != nil && !errors.As(err, &inUse) {
			return fmt.Errorf("create table %s: %w", full, err)
		}
		w := dynamodb.NewTableExistsWaiter(raw)
		if err := w.Wait(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(full)}, 2*time.Minute); err != nil {
			return fmt.Errorf("wait for table %s: %w", full, err)
		}
		_, err = raw.UpdateTimeToLive(ctx, &dynamodb.UpdateTimeToLiveInput{
			TableName:               aws.String(full),
			TimeToLiveSpecification: &types.TimeToLiveSpecification{AttributeName: aws.String(TTL), Enabled: aws.Bool(true)},
		})
		// Enabling TTL twice is a ValidationException; that is fine.
		var ve interface{ ErrorCode() string }
		if err != nil && !(errors.As(err, &ve) && ve.ErrorCode() == "ValidationException") {
			return fmt.Errorf("enable ttl on %s: %w", full, err)
		}
	}
	return nil
}

// DeleteTables drops tables (tests only).
func DeleteTables(ctx context.Context, raw *dynamodb.Client, prefix string, names ...string) {
	if len(names) == 0 {
		names = AllTables
	}
	for _, n := range names {
		_, _ = raw.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(prefix + n)})
	}
}

// Ping checks connectivity (used by readiness probes).
func Ping(ctx context.Context, raw *dynamodb.Client, table string) error {
	_, err := raw.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(table)})
	return err
}
