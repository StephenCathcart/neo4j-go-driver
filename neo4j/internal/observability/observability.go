/*
 * Copyright (c) "Neo4j"
 * Neo4j Sweden AB [https://neo4j.com]
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     https://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package observability

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const (
	tracerName = "neo4j-go-driver"

	// Span names
	spanNameExecuteRead         = "neo4j.ExecuteRead"
	spanNameExecuteWrite        = "neo4j.ExecuteWrite"
	spanNameRun                 = "neo4j.Run"
	spanNameBeginTransaction    = "neo4j.BeginTransaction"
	spanNameTransactionRun      = "neo4j.Transaction.Run"
	spanNameTransactionCommit   = "neo4j.Transaction.Commit"
	spanNameTransactionRollback = "neo4j.Transaction.Rollback"

	// Span attributes
	attrDatabase     = "neo4j.database"
	attrAccessMode   = "neo4j.access_mode"
	attrCypher       = "neo4j.cypher"
	attrServer       = "neo4j.server"
	attrRetries      = "neo4j.retries"
	attrError        = "neo4j.error"
	attrErrorMessage = "neo4j.error_message"
)

// getTracer returns a tracer instance. It returns a no-op tracer if OpenTelemetry
// is not configured, making observability optional and non-intrusive.
func getTracer() trace.Tracer {
	tracerProvider := otel.GetTracerProvider()
	if tracerProvider == nil {
		return trace.NewNoopTracerProvider().Tracer(tracerName)
	}
	return tracerProvider.Tracer(tracerName)
}

// StartSpan creates a new span with the given name and attributes.
// It uses the context's tracer if available, otherwise uses a no-op tracer.
func StartSpan(ctx context.Context, spanName string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	tracer := getTracer()
	return tracer.Start(ctx, spanName, opts...)
}

// StartExecuteReadSpan starts a span for an ExecuteRead operation.
func StartExecuteReadSpan(ctx context.Context, database string) (context.Context, trace.Span) {
	opts := []trace.SpanStartOption{
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String(attrDatabase, database),
			attribute.String(attrAccessMode, "read"),
		),
	}
	return StartSpan(ctx, spanNameExecuteRead, opts...)
}

// StartExecuteWriteSpan starts a span for an ExecuteWrite operation.
func StartExecuteWriteSpan(ctx context.Context, database string) (context.Context, trace.Span) {
	opts := []trace.SpanStartOption{
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String(attrDatabase, database),
			attribute.String(attrAccessMode, "write"),
		),
	}
	return StartSpan(ctx, spanNameExecuteWrite, opts...)
}

// StartRunSpan starts a span for a Run (auto-commit) operation.
func StartRunSpan(ctx context.Context, database string, cypher string, accessMode string) (context.Context, trace.Span) {
	opts := []trace.SpanStartOption{
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String(attrDatabase, database),
			attribute.String(attrAccessMode, accessMode),
			attribute.String(attrCypher, cypher),
		),
	}
	return StartSpan(ctx, spanNameRun, opts...)
}

// StartBeginTransactionSpan starts a span for a BeginTransaction operation.
func StartBeginTransactionSpan(ctx context.Context, database string, accessMode string) (context.Context, trace.Span) {
	opts := []trace.SpanStartOption{
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String(attrDatabase, database),
			attribute.String(attrAccessMode, accessMode),
		),
	}
	return StartSpan(ctx, spanNameBeginTransaction, opts...)
}

// StartTransactionRunSpan starts a span for a Transaction.Run operation.
func StartTransactionRunSpan(ctx context.Context, cypher string, server string) (context.Context, trace.Span) {
	opts := []trace.SpanStartOption{
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String(attrCypher, cypher),
		),
	}
	if server != "" {
		opts = append(opts, trace.WithAttributes(attribute.String(attrServer, server)))
	}
	return StartSpan(ctx, spanNameTransactionRun, opts...)
}

// StartTransactionCommitSpan starts a span for a Transaction.Commit operation.
func StartTransactionCommitSpan(ctx context.Context, server string) (context.Context, trace.Span) {
	opts := []trace.SpanStartOption{
		trace.WithSpanKind(trace.SpanKindClient),
	}
	if server != "" {
		opts = append(opts, trace.WithAttributes(attribute.String(attrServer, server)))
	}
	return StartSpan(ctx, spanNameTransactionCommit, opts...)
}

// StartTransactionRollbackSpan starts a span for a Transaction.Rollback operation.
func StartTransactionRollbackSpan(ctx context.Context, server string) (context.Context, trace.Span) {
	opts := []trace.SpanStartOption{
		trace.WithSpanKind(trace.SpanKindClient),
	}
	if server != "" {
		opts = append(opts, trace.WithAttributes(attribute.String(attrServer, server)))
	}
	return StartSpan(ctx, spanNameTransactionRollback, opts...)
}

// RecordError records an error on the span and sets the span status to Error.
func RecordError(span trace.Span, err error) {
	if err == nil || span == nil {
		return
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
	span.SetAttributes(
		attribute.Bool(attrError, true),
		attribute.String(attrErrorMessage, err.Error()),
	)
}

// SetRetries sets the number of retries as a span attribute.
func SetRetries(span trace.Span, retries int) {
	if span == nil {
		return
	}
	span.SetAttributes(attribute.Int(attrRetries, retries))
}

// SetServer sets the server name as a span attribute.
func SetServer(span trace.Span, server string) {
	if span == nil || server == "" {
		return
	}
	span.SetAttributes(attribute.String(attrServer, server))
}

// UpdateRetryCountFromContext extracts the span from context and updates the retry count.
func UpdateRetryCountFromContext(ctx context.Context, retries int) {
	span := trace.SpanFromContext(ctx)
	SetRetries(span, retries)
}
