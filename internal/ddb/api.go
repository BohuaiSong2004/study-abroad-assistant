// Package ddb wraps DynamoDB access: a narrow client interface satisfied by
// both the AWS SDK client and the DAX client, table definitions, and a
// DAX-style read-through cache for local and test environments.
package ddb

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// API is the subset of DynamoDB operations the services use. Both
// *dynamodb.Client and *dax.Dax (github.com/aws/aws-dax-go-v2) implement it,
// so switching a service to DAX is a configuration change.
type API interface {
	GetItem(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	BatchGetItem(ctx context.Context, in *dynamodb.BatchGetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.BatchGetItemOutput, error)
	Query(ctx context.Context, in *dynamodb.QueryInput, opts ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
	// Scan is for operator tasks only (Resync); request paths never scan.
	Scan(ctx context.Context, in *dynamodb.ScanInput, opts ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error)
	PutItem(ctx context.Context, in *dynamodb.PutItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	UpdateItem(ctx context.Context, in *dynamodb.UpdateItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
	DeleteItem(ctx context.Context, in *dynamodb.DeleteItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error)
	TransactWriteItems(ctx context.Context, in *dynamodb.TransactWriteItemsInput, opts ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error)
	BatchWriteItem(ctx context.Context, in *dynamodb.BatchWriteItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.BatchWriteItemOutput, error)
}

var (
	_ API = (*dynamodb.Client)(nil)
	_ API = (*CachedAPI)(nil)
)

// Key attribute names shared by every table (single-table design).
const (
	PK     = "pk"
	SK     = "sk"
	GSI1PK = "gsi1pk"
	GSI1SK = "gsi1sk"
	TTL    = "ttl"
	GSI1   = "gsi1"
)

// S builds a string attribute.
func S(s string) types.AttributeValue { return &types.AttributeValueMemberS{Value: s} }

// N builds a number attribute.
func N(n int64) types.AttributeValue {
	return &types.AttributeValueMemberN{Value: strconv.FormatInt(n, 10)}
}

// B builds a binary attribute.
func B(b []byte) types.AttributeValue { return &types.AttributeValueMemberB{Value: b} }

// BOOL builds a boolean attribute.
func BOOL(v bool) types.AttributeValue { return &types.AttributeValueMemberBOOL{Value: v} }

// Key builds a primary key.
func Key(pk, sk string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{PK: S(pk), SK: S(sk)}
}

// GetS reads a string attribute ("" if absent or another type).
func GetS(item map[string]types.AttributeValue, name string) string {
	if v, ok := item[name].(*types.AttributeValueMemberS); ok {
		return v.Value
	}
	return ""
}

// GetN reads a number attribute (0 if absent).
func GetN(item map[string]types.AttributeValue, name string) int64 {
	if v, ok := item[name].(*types.AttributeValueMemberN); ok {
		n, _ := strconv.ParseInt(v.Value, 10, 64)
		return n
	}
	return 0
}

// GetB reads a binary attribute.
func GetB(item map[string]types.AttributeValue, name string) []byte {
	if v, ok := item[name].(*types.AttributeValueMemberB); ok {
		return v.Value
	}
	return nil
}

// IsConditionFailed reports whether err is a failed condition expression,
// including a transaction cancelled because of one.
func IsConditionFailed(err error) bool {
	var ccf *types.ConditionalCheckFailedException
	if errors.As(err, &ccf) {
		return true
	}
	var tc *types.TransactionCanceledException
	if errors.As(err, &tc) {
		for _, r := range tc.CancellationReasons {
			if r.Code != nil && *r.Code == "ConditionalCheckFailed" {
				return true
			}
		}
	}
	return false
}

// CancellationIndex returns the index of the first transaction item that
// failed its condition, or -1.
func CancellationIndex(err error) int {
	var tc *types.TransactionCanceledException
	if errors.As(err, &tc) {
		for i, r := range tc.CancellationReasons {
			if r.Code != nil && *r.Code == "ConditionalCheckFailed" {
				return i
			}
		}
	}
	return -1
}

// IsRetryableConflict reports transaction conflicts with concurrent
// transactions on the same items; callers retry these.
func IsRetryableConflict(err error) bool {
	var tc *types.TransactionCanceledException
	if errors.As(err, &tc) {
		for _, r := range tc.CancellationReasons {
			if r.Code != nil && (*r.Code == "TransactionConflict") {
				return true
			}
		}
	}
	var tcx *types.TransactionConflictException
	return errors.As(err, &tcx)
}

// Pad formats a sequence number so lexical order equals numeric order.
func Pad(n int64) string { return fmt.Sprintf("%019d", n) }
