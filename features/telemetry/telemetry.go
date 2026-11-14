// Package telemetry exports OpenTelemetry traces and metrics derived from
// session events (docs/01 §5: export is off by default). Spans:
//
//	crewbase.turn            turn/start … turn/end
//	  crewbase.step          step/start … step/end (model, context size, usage)
//	    crewbase.tool <name> tool/call … tool/result
//
// Metrics: crewbase.llm.tokens, crewbase.turns, crewbase.turn.duration,
// crewbase.tool.calls. Message text, tool arguments and tool results are
// never exported unless the config sets includeContent: true.
//
// Data goes to an OTLP/HTTP endpoint (protobuf). Header values may be
// credential references ("env:OTEL_TOKEN", "keychain:crewbase/otel") or
// contain inline references ("Bearer ${keychain:crewbase/otel}"); they are
// resolved through the credentials service on every export.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/hedeqiang/crewbase/capabilities/credentials"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/kernel"
)

// ServiceKey is the key of the telemetry service (*Service).
const ServiceKey = "otel"

// Config of the plugin.
type Config struct {
	// Endpoint is the OTLP/HTTP base URL, e.g. http://127.0.0.1:4318;
	// /v1/traces and /v1/metrics are appended unless the URL already ends
	// with one of them.
	Endpoint string            `json:"endpoint"`
	Headers  map[string]string `json:"headers,omitempty"`
	// IncludeContent exports message text, tool arguments and results.
	IncludeContent bool   `json:"includeContent,omitempty"`
	ServiceName    string `json:"serviceName,omitempty"` // default crewbase
	// MetricIntervalSeconds is the metric export period (default 30).
	MetricIntervalSeconds int `json:"metricIntervalSeconds,omitempty"`
	// Gzip compresses export bodies.
	Gzip bool `json:"gzip,omitempty"`
}

// maxContent bounds exported content attributes.
const maxContent = 4096

var inlineRef = regexp.MustCompile(`\$\{((?:keychain|env|mem):[^}]+)\}`)

func isRef(v string) bool {
	for _, p := range []string{"keychain:", "env:", "mem:"} {
		if strings.HasPrefix(v, p) {
			return true
		}
	}
	return false
}

type resolver interface {
	Resolve(ctx context.Context, ref string) (string, error)
}

func resolveHeader(ctx context.Context, r resolver, v string) (string, error) {
	if !isRef(v) && !inlineRef.MatchString(v) {
		return v, nil
	}
	if r == nil {
		return "", errors.New("credentials service unavailable")
	}
	if isRef(v) {
		return r.Resolve(ctx, v)
	}
	var ferr error
	out := inlineRef.ReplaceAllStringFunc(v, func(m string) string {
		s, err := r.Resolve(ctx, inlineRef.FindStringSubmatch(m)[1])
		if err != nil && ferr == nil {
			ferr = err
		}
		return s
	})
	return out, ferr
}

// headerTransport adds configured headers, resolving credential references
// per request (rotation needs no restart). An unresolvable reference fails
// the export rather than sending it unauthenticated.
type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
	creds   func() resolver
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if len(t.headers) == 0 {
		return t.base.RoundTrip(req)
	}
	req = req.Clone(req.Context())
	r := t.creds()
	for k, v := range t.headers {
		val, err := resolveHeader(req.Context(), r, v)
		if err != nil {
			return nil, fmt.Errorf("otel: header %s: %w", k, err)
		}
		req.Header.Set(k, val)
	}
	return t.base.RoundTrip(req)
}

func endpoints(base string) (traces, metrics string) {
	b := strings.TrimRight(base, "/")
	switch {
	case strings.HasSuffix(b, "/v1/traces"):
		root := strings.TrimSuffix(b, "/v1/traces")
		return b, root + "/v1/metrics"
	case strings.HasSuffix(b, "/v1/metrics"):
		root := strings.TrimSuffix(b, "/v1/metrics")
		return root + "/v1/traces", b
	}
	return b + "/v1/traces", b + "/v1/metrics"
}

// Service is the `otel` service.
type Service struct {
	cfg    Config
	tp     *sdktrace.TracerProvider
	mp     *sdkmetric.MeterProvider
	tracer trace.Tracer

	tokens   metric.Int64Counter
	turns    metric.Int64Counter
	duration metric.Float64Histogram
	toolsN   metric.Int64Counter

	mu      sync.Mutex
	turnsOn map[string]*turnState // session/agent → open turn
	toolsOn map[string]trace.Span // call id → open tool span
	input   map[string]string     // session/agent → last user text (content mode)
}

type turnState struct {
	ctx     context.Context
	span    trace.Span
	start   time.Time
	step    trace.Span
	stepCtx context.Context
	model   llm.ModelRef
}

// Flush exports everything buffered so far.
func (s *Service) Flush(ctx context.Context) error {
	return errors.Join(s.tp.ForceFlush(ctx), s.mp.ForceFlush(ctx))
}

func (s *Service) shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.mu.Lock()
	for _, ts := range s.turnsOn {
		if ts.step != nil {
			ts.step.End()
		}
		ts.span.SetStatus(codes.Error, "daemon stopped")
		ts.span.End()
	}
	for _, sp := range s.toolsOn {
		sp.End()
	}
	s.turnsOn, s.toolsOn = map[string]*turnState{}, map[string]trace.Span{}
	s.mu.Unlock()
	_ = s.tp.Shutdown(ctx)
	_ = s.mp.Shutdown(ctx)
}

func clip(s string) string {
	if len(s) <= maxContent {
		return s
	}
	return s[:maxContent] + "…"
}

func modelAttrs(m llm.ModelRef) []attribute.KeyValue {
	return []attribute.KeyValue{attribute.String("gen_ai.system", m.Provider), attribute.String("gen_ai.request.model", m.Model)}
}

func (s *Service) onEvent(a session.Appended) {
	e := a.Event
	key := a.Session + "/" + e.Agent
	at := trace.WithTimestamp(e.Time)
	bg := context.Background()
	s.mu.Lock()
	defer s.mu.Unlock()
	switch e.Type {
	case session.TypeUserMessage:
		if s.cfg.IncludeContent {
			var m session.UserMessage
			if e.Decode(&m) == nil {
				s.input[a.Session+"/"+m.To] = m.Text
			}
		}
	case session.TypeTurnStart:
		var d agentloop.TurnStart
		_ = e.Decode(&d)
		if old := s.turnsOn[key]; old != nil {
			old.span.End(at)
		}
		attrs := append([]attribute.KeyValue{
			attribute.String("crewbase.agent", e.Agent), attribute.String("crewbase.session", a.Session),
			attribute.String("crewbase.input_id", d.Input),
		}, modelAttrs(d.Model)...)
		if s.cfg.IncludeContent {
			if t, ok := s.input[key]; ok {
				attrs = append(attrs, attribute.String("crewbase.input", clip(t)))
				delete(s.input, key)
			}
		}
		ctx, sp := s.tracer.Start(bg, "crewbase.turn", at, trace.WithAttributes(attrs...), trace.WithSpanKind(trace.SpanKindInternal))
		s.turnsOn[key] = &turnState{ctx: ctx, span: sp, start: e.Time, model: d.Model}
	case session.TypeStepStart:
		ts := s.turnsOn[key]
		if ts == nil {
			return
		}
		var d agentloop.StepStart
		_ = e.Decode(&d)
		if ts.step != nil {
			ts.step.End(at)
		}
		ts.model = d.Model
		attrs := append([]attribute.KeyValue{
			attribute.Int("crewbase.step", d.Step), attribute.Int("crewbase.context_tokens", d.ContextTokens),
			attribute.Int("crewbase.context_window", d.ContextWindow),
		}, modelAttrs(d.Model)...)
		ts.stepCtx, ts.step = s.tracer.Start(ts.ctx, "crewbase.step", at, trace.WithAttributes(attrs...), trace.WithSpanKind(trace.SpanKindClient))
	case session.TypeAssistantMessage:
		var d session.AssistantMessage
		if e.Decode(&d) != nil {
			return
		}
		mattrs := modelAttrs(d.Model)
		s.tokens.Add(bg, int64(d.Usage.InputTokens), metric.WithAttributes(append(mattrs, attribute.String("gen_ai.token.type", "input"))...))
		s.tokens.Add(bg, int64(d.Usage.OutputTokens), metric.WithAttributes(append(mattrs, attribute.String("gen_ai.token.type", "output"))...))
		if ts := s.turnsOn[key]; ts != nil && ts.step != nil {
			ts.step.SetAttributes(
				attribute.Int("gen_ai.usage.input_tokens", d.Usage.InputTokens),
				attribute.Int("gen_ai.usage.output_tokens", d.Usage.OutputTokens),
				attribute.Int("crewbase.usage.cache_read", d.Usage.CacheRead),
				attribute.String("gen_ai.response.finish_reason", d.Stop),
			)
			if s.cfg.IncludeContent {
				ts.step.SetAttributes(attribute.String("crewbase.output", clip(d.Message.Text())))
			}
		}
	case session.TypeToolCall:
		var d session.ToolCallData
		if e.Decode(&d) != nil {
			return
		}
		parent := bg
		if ts := s.turnsOn[key]; ts != nil {
			parent = ts.ctx
			if ts.step != nil {
				parent = ts.stepCtx
			}
		}
		attrs := []attribute.KeyValue{attribute.String("gen_ai.tool.name", d.Call.Name), attribute.String("gen_ai.tool.call.id", d.Call.ID)}
		if s.cfg.IncludeContent {
			attrs = append(attrs, attribute.String("crewbase.tool.args", clip(string(d.Call.Args))))
		}
		_, sp := s.tracer.Start(parent, "crewbase.tool "+d.Call.Name, at, trace.WithAttributes(attrs...))
		s.toolsOn[d.Call.ID] = sp
	case session.TypeToolResult:
		var d llm.ToolResult
		if e.Decode(&d) != nil {
			return
		}
		s.toolsN.Add(bg, 1, metric.WithAttributes(attribute.String("gen_ai.tool.name", d.Name), attribute.Bool("error", d.IsError)))
		sp, ok := s.toolsOn[d.CallID]
		if !ok {
			return
		}
		delete(s.toolsOn, d.CallID)
		sp.SetAttributes(attribute.Bool("crewbase.tool.error", d.IsError))
		if s.cfg.IncludeContent {
			sp.SetAttributes(attribute.String("crewbase.tool.result", clip(d.Content)))
		}
		if d.IsError {
			sp.SetStatus(codes.Error, "tool error")
		}
		sp.End(at)
	case session.TypeStepEnd:
		if ts := s.turnsOn[key]; ts != nil && ts.step != nil {
			ts.step.End(at)
			ts.step, ts.stepCtx = nil, nil
		}
	case session.TypeModelSwitched:
		var d session.ModelSwitchData
		_ = e.Decode(&d)
		if ts := s.turnsOn[key]; ts != nil {
			ts.span.AddEvent("model.switched", at, trace.WithAttributes(
				attribute.String("from", d.From.String()), attribute.String("to", d.To.String()), attribute.String("reason", d.Reason)))
		}
	case session.TypeCompaction:
		if ts := s.turnsOn[key]; ts != nil {
			ts.span.AddEvent("compaction", at)
		}
	case session.TypeApprovalAsked:
		if ts := s.turnsOn[key]; ts != nil {
			ts.span.AddEvent("approval.asked", at)
		}
	case session.TypeTurnEnd:
		ts := s.turnsOn[key]
		if ts == nil {
			return
		}
		delete(s.turnsOn, key)
		var d agentloop.TurnEnd
		_ = e.Decode(&d)
		if ts.step != nil {
			ts.step.End(at)
		}
		ts.span.SetAttributes(attribute.String("crewbase.turn.status", d.Status), attribute.Int("crewbase.turn.steps", d.Steps))
		if d.Status == "failed" {
			// Error messages can quote content; only the status is exported
			// unless content export is on.
			msg := "turn failed"
			if s.cfg.IncludeContent {
				msg = clip(d.Error)
			}
			ts.span.SetStatus(codes.Error, msg)
		}
		ts.span.End(at)
		attrs := metric.WithAttributes(attribute.String("crewbase.agent", e.Agent), attribute.String("crewbase.turn.status", d.Status))
		s.turns.Add(bg, 1, attrs)
		s.duration.Record(bg, e.Time.Sub(ts.start).Seconds(), attrs)
	}
}

// Plugin exports traces and metrics. It is disabled by default in bundles.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/otel",
	Optional:    []string{credentials.ServiceKey},
	Description: "OpenTelemetry export (OTLP/HTTP) of turn/step/tool spans and token metrics; content excluded by default.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		if cfg.Endpoint == "" {
			return errors.New("otel: config endpoint is required")
		}
		if !strings.HasPrefix(cfg.Endpoint, "http://") && !strings.HasPrefix(cfg.Endpoint, "https://") {
			return errors.New("otel: endpoint must be an http:// or https:// URL")
		}
		if cfg.ServiceName == "" {
			cfg.ServiceName = "crewbase"
		}
		if cfg.MetricIntervalSeconds <= 0 {
			cfg.MetricIntervalSeconds = 30
		}
		client := &http.Client{Timeout: 10 * time.Second, Transport: &headerTransport{
			base: http.DefaultTransport, headers: cfg.Headers,
			creds: func() resolver {
				if cr, ok := kernel.Lookup[*credentials.Service](c, credentials.ServiceKey); ok {
					return cr
				}
				return nil
			},
		}}
		tURL, mURL := endpoints(cfg.Endpoint)
		ctx := c.Context()
		topts := []otlptracehttp.Option{otlptracehttp.WithEndpointURL(tURL), otlptracehttp.WithHTTPClient(client)}
		mopts := []otlpmetrichttp.Option{otlpmetrichttp.WithEndpointURL(mURL), otlpmetrichttp.WithHTTPClient(client)}
		if cfg.Gzip {
			topts = append(topts, otlptracehttp.WithCompression(otlptracehttp.GzipCompression))
			mopts = append(mopts, otlpmetrichttp.WithCompression(otlpmetrichttp.GzipCompression))
		}
		texp, err := otlptracehttp.New(ctx, topts...)
		if err != nil {
			return err
		}
		mexp, err := otlpmetrichttp.New(ctx, mopts...)
		if err != nil {
			return err
		}
		res := resource.NewSchemaless(attribute.String("service.name", cfg.ServiceName))
		s := &Service{cfg: cfg,
			tp: sdktrace.NewTracerProvider(sdktrace.WithBatcher(texp), sdktrace.WithResource(res)),
			mp: sdkmetric.NewMeterProvider(sdkmetric.WithResource(res),
				sdkmetric.WithReader(sdkmetric.NewPeriodicReader(mexp, sdkmetric.WithInterval(time.Duration(cfg.MetricIntervalSeconds)*time.Second)))),
			turnsOn: map[string]*turnState{}, toolsOn: map[string]trace.Span{}, input: map[string]string{},
		}
		// The providers are private: the global otel providers are left
		// untouched so embedding programs keep their own.
		s.tracer = s.tp.Tracer("github.com/hedeqiang/crewbase")
		meter := s.mp.Meter("github.com/hedeqiang/crewbase")
		if s.tokens, err = meter.Int64Counter("crewbase.llm.tokens", metric.WithUnit("{token}"), metric.WithDescription("Model tokens by type")); err != nil {
			return err
		}
		if s.turns, err = meter.Int64Counter("crewbase.turns", metric.WithDescription("Finished turns by status")); err != nil {
			return err
		}
		if s.duration, err = meter.Float64Histogram("crewbase.turn.duration", metric.WithUnit("s"), metric.WithDescription("Turn duration")); err != nil {
			return err
		}
		if s.toolsN, err = meter.Int64Counter("crewbase.tool.calls", metric.WithDescription("Tool calls by tool and outcome")); err != nil {
			return err
		}
		c.Defer(s.shutdown)
		session.OnEvent.Listen(c, s.onEvent)
		return c.Provide(ServiceKey, s)
	},
})
