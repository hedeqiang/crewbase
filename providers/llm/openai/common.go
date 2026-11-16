// Shared provider helpers live in providers/llm/internal/common; these
// aliases keep this package's call sites short.
package openai

import "github.com/hedeqiang/crewbase/providers/llm/internal/common"

const CredentialsKey = common.CredentialsKey

type (
	Resolver     = common.Resolver
	ModelSpec    = common.ModelSpec
	keySource    = common.KeySource
	apiErrorBody = common.APIErrorBody
)

var (
	parseErrorBody   = common.ParseErrorBody
	nilToEmpty       = common.NilToEmpty
	httpError        = common.HTTPError
	containsAny      = common.ContainsAny
	quotaWords       = common.QuotaWords
	rateWords        = common.RateWords
	overflowWords    = common.OverflowWords
	authWords        = common.AuthWords
	unavailableWords = common.UnavailableWords
	classify         = common.Classify
	retryAfter       = common.RetryAfter
	sanitize         = common.Sanitize
	transportError   = common.TransportError
	streamReadError  = common.StreamReadError
	normalizeArgs    = common.NormalizeArgs
	argsObject       = common.ArgsObject
	toolNames        = common.ToolNames
	schemaOrEmpty    = common.SchemaOrEmpty
	trimSlash        = common.TrimSlash
	estimateUsage    = common.EstimateUsage
	findSpec         = common.FindSpec
	hasCap           = common.HasCap
	mediaInfo        = common.MediaInfo
)
