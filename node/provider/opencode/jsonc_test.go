package opencode

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestParseJSONCConfig(t *testing.T) {
	for _, test := range []struct{ name, input, want string }{
		{"strict JSON", `{"array":[1,true,false,null,{}],"text":"https://example.test"}`, `{"array":[1,true,false,null,{}],"text":"https://example.test"}`},
		{"comments and nested commas", "/* header */ {\r\n\"array\": [1, true, false, null, {}, [], /* tail */], // line\r\n\"object\": {\"key\": \"value\",},}\n// EOF", `{"array":[1,true,false,null,{},[]],"object":{"key":"value"}}`},
		{"comment punctuation in strings", `{"url":"https://example.test/a//b?q=/*a*/", "escaped":"a\"//b\\", "punctuation":"[,]}/*//",}`, `{"url":"https://example.test/a//b?q=/*a*/", "escaped":"a\"//b\\", "punctuation":"[,]}/*//"}`},
		{"comment separates tokens", `{"array": [1/* gap */,2,],"empty":{/* comment */},}`, `{"array":[1,2],"empty":{}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseJSONCConfig([]byte(test.input))
			if err != nil {
				t.Fatal(err)
			}
			var want map[string]any
			if err := json.Unmarshal([]byte(test.want), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatal("JSONC standardization altered configuration values")
			}
		})
	}
}

func TestParseJSONCRejectsMalformedInput(t *testing.T) {
	for _, input := range []string{
		``, `// comment only`, `null`, `[]`, `"private-value"`,
		`{"array":[,]}`, `{"array":[1,,]}`, `{"array":[1, , /* comment */ ]}`,
		`{"object":{,}}`, `{"missing":,}`, `{"missing":}`, `{"key",}`,
		`{"number":1/* comment */2}`, `{"value":true false}`, `{"value":undefined}`,
		`{"value":"unterminated}`, `{"value":"invalid\escape"}`,
		`{"value":1} /* private-value`, `{"value":1} /* closed */ trailing`,
		`{"value":1 // hides closing brace}`, "{\"value\":\"raw\nnewline\"}",
	} {
		if _, err := parseJSONCConfig([]byte(input)); err == nil {
			t.Errorf("accepted malformed JSONC: %q", input)
		} else if strings.Contains(err.Error(), "private-value") {
			t.Fatal("decoder error disclosed configuration contents")
		}
	}
}
