package clickhouse

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMessageSearchDedup(t *testing.T) {
	for name, tc := range map[string]struct {
		in, want messageSearch
	}{
		"empty": {
			in:   messageSearch{},
			want: messageSearch{},
		},
		"no repeats are untouched": {
			in:   messageSearch{tokens: []string{"error", "timeout"}, substrings: []string{"fail"}},
			want: messageSearch{tokens: []string{"error", "timeout"}, substrings: []string{"fail"}},
		},
		"repeated words keep the first occurrence and the order": {
			in:   messageSearch{tokens: []string{"error", "timeout", "error", "db", "timeout"}},
			want: messageSearch{tokens: []string{"error", "timeout", "db"}},
		},
		"words and substrings are deduplicated separately": {
			in:   messageSearch{tokens: []string{"retry", "retry"}, substrings: []string{"fail", "fail", "eout"}},
			want: messageSearch{tokens: []string{"retry"}, substrings: []string{"fail", "eout"}},
		},
		"the same text as a word and as a substring stays in both": {
			in:   messageSearch{tokens: []string{"fail"}, substrings: []string{"fail"}},
			want: messageSearch{tokens: []string{"fail"}, substrings: []string{"fail"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := tc.in.dedup()
			assert.Equal(t, len(tc.want.tokens), len(got.tokens))
			assert.Equal(t, len(tc.want.substrings), len(got.substrings))
			for i := range tc.want.tokens {
				assert.Equal(t, tc.want.tokens[i], got.tokens[i])
			}
			for i := range tc.want.substrings {
				assert.Equal(t, tc.want.substrings[i], got.substrings[i])
			}
		})
	}
}

func TestMessageSearchDedupDoesNotModifyInput(t *testing.T) {
	in := messageSearch{tokens: []string{"a", "b", "a"}, substrings: []string{"x", "x"}}
	in.dedup()
	assert.Equal(t, []string{"a", "b", "a"}, in.tokens)
	assert.Equal(t, []string{"x", "x"}, in.substrings)
}

func TestMessageSearchEmpty(t *testing.T) {
	assert.True(t, messageSearch{}.empty())
	assert.False(t, messageSearch{tokens: []string{"a"}}.empty())
	assert.False(t, messageSearch{substrings: []string{"a"}}.empty())
}

func TestParseMessageSearch(t *testing.T) {
	for name, tc := range map[string]struct {
		in         string
		tokens     []string
		substrings []string
	}{
		"empty":                       {in: ""},
		"only spaces":                 {in: "   "},
		"one word":                    {in: "timeout", tokens: []string{"timeout"}},
		"words are lowercased":        {in: "Connection REFUSED", tokens: []string{"connection", "refused"}},
		"punctuation splits a word":   {in: "db-5432", tokens: []string{"db", "5432"}},
		"url and path":                {in: "GET /api/orders/42", tokens: []string{"get", "api", "orders", "42"}},
		"trailing star is substring":  {in: "fail*", substrings: []string{"fail"}},
		"leading star is substring":   {in: "*eout", substrings: []string{"eout"}},
		"both stars":                  {in: "*mid*", substrings: []string{"mid"}},
		"substring keeps its case":    {in: "Fail*", substrings: []string{"Fail"}},
		"a lone star is ignored":      {in: "* timeout", tokens: []string{"timeout"}},
		"words and substrings":        {in: "retry fail* db-1", tokens: []string{"retry", "db", "1"}, substrings: []string{"fail"}},
		"repeats are kept by parsing": {in: "a a", tokens: []string{"a", "a"}},
		"non ASCII letters stay":      {in: "Łódź", tokens: []string{"łódź"}},
	} {
		t.Run(name, func(t *testing.T) {
			got := parseMessageSearch(tc.in)
			assert.Equal(t, len(tc.tokens), len(got.tokens), "%v", got.tokens)
			for i := range tc.tokens {
				assert.Equal(t, tc.tokens[i], got.tokens[i])
			}
			assert.Equal(t, len(tc.substrings), len(got.substrings), "%v", got.substrings)
			for i := range tc.substrings {
				assert.Equal(t, tc.substrings[i], got.substrings[i])
			}
		})
	}
}

func TestMessageExpr(t *testing.T) {
	t.Run("empty search gives no condition", func(t *testing.T) {
		var args []any
		assert.Equal(t, "", messageExpr(messageSearch{}, "token", &args))
		assert.Empty(t, args)
	})
	t.Run("words become one hasAllTokens over the indexed expression", func(t *testing.T) {
		var args []any
		expr := messageExpr(messageSearch{tokens: []string{"error", "timeout"}}, "token", &args)
		assert.Equal(t, "hasAllTokens(lowerUTF8(Body), @token_tokens)", expr)
		assert.Equal(t, []string{"error", "timeout"}, namedArg(t, args, "token_tokens"))
	})
	t.Run("substrings are separate case-insensitive terms", func(t *testing.T) {
		var args []any
		expr := messageExpr(messageSearch{substrings: []string{"fail", "eout"}}, "token", &args)
		assert.Equal(t, "positionCaseInsensitiveUTF8(Body, @token_sub_0) > 0 AND positionCaseInsensitiveUTF8(Body, @token_sub_1) > 0", expr)
		assert.Equal(t, "fail", namedArg(t, args, "token_sub_0"))
		assert.Equal(t, "eout", namedArg(t, args, "token_sub_1"))
	})
	t.Run("words and substrings are joined with AND, under the given prefix", func(t *testing.T) {
		var args []any
		expr := messageExpr(messageSearch{tokens: []string{"retry"}, substrings: []string{"fail"}}, "not_token_2", &args)
		assert.Equal(t, "hasAllTokens(lowerUTF8(Body), @not_token_2_tokens) AND positionCaseInsensitiveUTF8(Body, @not_token_2_sub_0) > 0", expr)
		assert.Len(t, args, 2)
	})
}
