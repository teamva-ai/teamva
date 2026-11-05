package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
)

type responseMeta struct {
	RequestID       string `json:"request_id"`
	ResourceVersion int64  `json:"resource_version,omitempty"`
	Replayed        bool   `json:"replayed,omitempty"`
}

type successResponse struct {
	Data any          `json:"data"`
	Meta responseMeta `json:"meta"`
}

var errJSONResponseBudget = errors.New("JSON response exceeds byte budget")

const maxGitJSONResponseBytes = 20 << 20

const maxAttachmentJSONRequestBytes = 2 << 20

func respond(c *gin.Context, status int, data any, version int64) {
	c.Header("X-Content-Type-Options", "nosniff")
	c.PureJSON(status, gin.H{
		"data": browserResponseData(data),
		"meta": responseMeta{
			RequestID:       requestID(c),
			ResourceVersion: version,
		},
	})
}

func respondReplayed(c *gin.Context, status int, data any, version int64) {
	c.Header("X-Content-Type-Options", "nosniff")
	c.PureJSON(status, gin.H{
		"data": browserResponseData(data),
		"meta": responseMeta{
			RequestID:       requestID(c),
			ResourceVersion: version,
			Replayed:        true,
		},
	})
}

func respondError(c *gin.Context, err error) {
	var domainError *domain.Error
	if !errors.As(err, &domainError) {
		slog.Error("request failed", "request_id", requestID(c), "error", err)
		domainError = &domain.Error{
			Code:       "internal",
			Message:    "暂时无法完成这个操作，请重试；如果仍然失败，请到“活动记录”查看详情。",
			Retryable:  true,
			HTTPStatus: http.StatusInternalServerError,
		}
	}
	status := domainError.HTTPStatus
	if status == 0 {
		status = http.StatusInternalServerError
	}
	errorBody := gin.H{
		"code":      domainError.Code,
		"message":   publicErrorMessage(domainError.Code, domainError.Message, status, c.GetHeader("Accept-Language")),
		"retryable": domainError.Retryable,
	}
	if domainError.Details != nil {
		errorBody["details"] = domainError.Details
	}
	c.Header("X-Content-Type-Options", "nosniff")
	c.PureJSON(status, gin.H{
		"error": errorBody,
		"meta":  responseMeta{RequestID: requestID(c)},
	})
}

func responseTooLargeError() error {
	return &domain.Error{
		Code: "response_too_large", Message: "响应超过安全传输上限，请缩小分页范围",
		HTTPStatus: http.StatusRequestEntityTooLarge,
		Details:    map[string]any{"max_bytes": maxGitJSONResponseBytes},
	}
}

func encodeBoundedResponse(
	c *gin.Context, data any, version int64, maxBytes int,
) ([]byte, error) {
	return encodeBoundedJSON(successResponse{
		Data: data,
		Meta: responseMeta{RequestID: requestID(c), ResourceVersion: version},
	}, maxBytes)
}

func writeEncodedJSON(c *gin.Context, status int, payload []byte) {
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(status, "application/json; charset=utf-8", payload)
}

// encodeBoundedJSON performs a no-allocation size walk before invoking
// encoding/json. The walk deliberately overestimates numbers and fixed-size
// values, but accounts for string escaping with EscapeHTML(false). Therefore a
// rejected response never first materializes an unbounded encoded copy.
func encodeBoundedJSON(value any, maxBytes int) ([]byte, error) {
	estimated, ok := estimateJSONValue(reflect.ValueOf(value), maxBytes)
	if !ok || estimated+1 > maxBytes { // Encoder appends one newline.
		return nil, errJSONResponseBudget
	}
	var buffer bytes.Buffer
	buffer.Grow(estimated + 1)
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	if buffer.Len() > maxBytes {
		return nil, errJSONResponseBudget
	}
	return buffer.Bytes(), nil
}

var (
	timeType       = reflect.TypeOf(time.Time{})
	rawMessageType = reflect.TypeOf(json.RawMessage{})
)

func estimateJSONValue(value reflect.Value, limit int) (int, bool) {
	if !value.IsValid() {
		return boundedJSONSize(4, limit)
	}
	if value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return boundedJSONSize(4, limit)
		}
		return estimateJSONValue(value.Elem(), limit)
	}
	if value.Type() == timeType {
		return boundedJSONSize(40, limit)
	}
	if value.Type() == rawMessageType {
		return boundedJSONSize(value.Len(), limit)
	}
	switch value.Kind() {
	case reflect.Bool:
		return boundedJSONSize(5, limit)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Uintptr, reflect.Float32, reflect.Float64:
		return boundedJSONSize(64, limit)
	case reflect.String:
		return boundedJSONSize(escapedJSONStringSize(value.String()), limit)
	case reflect.Slice:
		if value.IsNil() {
			return boundedJSONSize(4, limit)
		}
		if value.Type().Elem().Kind() == reflect.Uint8 {
			return boundedJSONSize(2+4*((value.Len()+2)/3), limit)
		}
		fallthrough
	case reflect.Array:
		total := 2
		for index := 0; index < value.Len(); index++ {
			itemSize, ok := estimateJSONValue(value.Index(index), limit-total)
			if !ok {
				return 0, false
			}
			total += itemSize
			if index > 0 {
				total++
			}
			if total > limit {
				return 0, false
			}
		}
		return total, true
	case reflect.Map:
		if value.IsNil() {
			return boundedJSONSize(4, limit)
		}
		total := 2
		iterator := value.MapRange()
		index := 0
		for iterator.Next() {
			keySize := 128
			if iterator.Key().Kind() == reflect.String {
				keySize = escapedJSONStringSize(iterator.Key().String())
			}
			itemSize, ok := estimateJSONValue(iterator.Value(), limit-total-keySize-1)
			if !ok {
				return 0, false
			}
			total += keySize + 1 + itemSize
			if index > 0 {
				total++
			}
			index++
			if total > limit {
				return 0, false
			}
		}
		return total, true
	case reflect.Struct:
		total := 2
		written := 0
		typeInfo := value.Type()
		for index := 0; index < value.NumField(); index++ {
			fieldInfo := typeInfo.Field(index)
			if fieldInfo.PkgPath != "" || fieldInfo.Anonymous {
				continue
			}
			tag := fieldInfo.Tag.Get("json")
			name, options, _ := strings.Cut(tag, ",")
			if name == "-" {
				continue
			}
			if name == "" {
				name = fieldInfo.Name
			}
			fieldValue := value.Field(index)
			if strings.Contains(","+options+",", ",omitempty,") && jsonEmptyValue(fieldValue) {
				continue
			}
			keySize := escapedJSONStringSize(name) + 1
			itemSize, ok := estimateJSONValue(fieldValue, limit-total-keySize)
			if !ok {
				return 0, false
			}
			total += keySize + itemSize
			if written > 0 {
				total++
			}
			written++
			if total > limit {
				return 0, false
			}
		}
		return total, true
	default:
		return boundedJSONSize(128, limit)
	}
}

func boundedJSONSize(size, limit int) (int, bool) {
	return size, size <= limit
}

func jsonEmptyValue(value reflect.Value) bool {
	switch value.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return value.Len() == 0
	case reflect.Bool:
		return !value.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return value.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return value.Float() == 0
	case reflect.Interface, reflect.Pointer:
		return value.IsNil()
	}
	return false
}

func escapedJSONStringSize(value string) int {
	size := 2
	for len(value) > 0 {
		r, width := utf8.DecodeRuneInString(value)
		if r == utf8.RuneError && width == 1 {
			size += 6
			value = value[1:]
			continue
		}
		value = value[width:]
		switch r {
		case '\\', '"', '\n', '\r', '\t', '\b', '\f':
			size += 2
		case '\u2028', '\u2029':
			size += 6
		default:
			if r < 0x20 {
				size += 6
			} else {
				size += width
			}
		}
	}
	return size
}

func bindJSON(c *gin.Context, target any) bool {
	return bindJSONBytes(c, target, 1<<20)
}

func bindAttachmentJSON(c *gin.Context, target any) bool {
	return bindJSONBytes(c, target, maxAttachmentJSONRequestBytes)
}

func bindJSONBytes(c *gin.Context, target any, maxBytes int64) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
	decoder := json.NewDecoder(c.Request.Body)
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return rejectJSONRequest(c, err, maxBytes)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			return rejectJSONRequest(c, err, maxBytes)
		}
		respondError(c, domain.Invalid("请求 JSON 必须只包含一个对象"))
		return false
	}
	if err := validateJSONRequest(raw, reflect.TypeOf(target)); err != nil {
		return rejectJSONRequest(c, err, maxBytes)
	}
	decoder = json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return rejectJSONRequest(c, err, maxBytes)
	}
	if err := binding.Validator.ValidateStruct(target); err != nil {
		respondError(c, domain.Invalid("请求 JSON 不符合契约"))
		return false
	}
	if raw, ok := c.Get("work_input"); ok {
		if refs, ok := raw.(map[string]any); ok {
			encoded, _ := json.Marshal(target)
			var values map[string]any
			if json.Unmarshal(encoded, &values) == nil {
				for key, value := range workMetadata(values) {
					refs[key] = value
				}
			}
		}
	}
	return true
}

func rejectJSONRequest(c *gin.Context, err error, maxBytes int64) bool {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		respondError(c, &domain.Error{
			Code: "request_too_large", Message: "请求数据超过安全传输上限",
			HTTPStatus: http.StatusRequestEntityTooLarge,
			Details:    map[string]any{"max_bytes": maxBytes},
		})
	} else {
		respondError(c, domain.Invalid("请求 JSON 不符合契约"))
	}
	return false
}

var (
	errJSONRequestContract = errors.New("JSON request requires non-null contract values")
	jsonUnmarshalerType    = reflect.TypeFor[json.Unmarshaler]()
	jsonRequestFieldsCache sync.Map // map[reflect.Type][]jsonRequestField
)

// encoding/json accepts null for strings, numbers, structs and optional pointers,
// silently treating it as a zero value or an omitted update. Request contracts do
// not: nullable fields must explicitly opt in with jsonnullable:"true". Inspect
// the bounded, single JSON object before decoding or any handler-side mutation.
func validateJSONRequest(raw json.RawMessage, target reflect.Type) error {
	// encoding/json otherwise silently replaces malformed string/key bytes with
	// U+FFFD. Inspect the original bounded bytes before Token/Decode can rewrite
	// them, including inside free JSON/RawMessage payloads.
	if !utf8.Valid(raw) {
		return errJSONRequestContract
	}
	if raw = bytes.TrimSpace(raw); len(raw) == 0 || raw[0] != '{' {
		return errJSONRequestContract
	}
	return validateJSONRequestValue(json.NewDecoder(bytes.NewReader(raw)), target, false)
}

func validateJSONRequestValue(decoder *json.Decoder, target reflect.Type, nullable bool) error {
	for target != nil && target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	// Unknown fields are rejected by the subsequent strict decoder. Interfaces
	// are intentionally unconstrained JSON values, including null. RawMessage
	// and custom unmarshallers own their internal schema; only their outer null
	// is checked here, leaving arbitrary business payloads untouched.
	if target == nil || target.Kind() == reflect.Interface {
		var raw json.RawMessage
		return decoder.Decode(&raw)
	}
	if target == rawMessageType || reflect.PointerTo(target).Implements(jsonUnmarshalerType) {
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return err
		}
		if !nullable && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return errJSONRequestContract
		}
		return nil
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token == nil && !nullable {
		return errJSONRequestContract
	}
	switch token {
	case json.Delim('{'):
		var fields []jsonRequestField
		if target.Kind() == reflect.Struct {
			fields = jsonRequestFields(target)
		}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			var fieldType reflect.Type
			var fieldNullable bool
			if target.Kind() == reflect.Map {
				fieldType = target.Elem()
				fieldNullable = freeJSONRequestElement(fieldType)
			} else if field, ok := findJSONRequestField(fields, key.(string)); ok {
				fieldType, fieldNullable = field.typeInfo, field.nullable
			}
			if err := validateJSONRequestValue(decoder, fieldType, fieldNullable); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
	case json.Delim('['):
		var itemType reflect.Type
		if target.Kind() == reflect.Slice || target.Kind() == reflect.Array {
			itemType = target.Elem()
		}
		for decoder.More() {
			if err := validateJSONRequestValue(decoder, itemType, freeJSONRequestElement(itemType)); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
	}
	return err
}

// RawMessage/interface collection elements are arbitrary JSON values, not
// declared object fields. Keep null inside those payloads; the collection field
// itself still requires its normal non-null object/array contract.
func freeJSONRequestElement(target reflect.Type) bool {
	for target != nil && target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	return target == nil || target == rawMessageType || target.Kind() == reflect.Interface
}

type jsonRequestField struct {
	name     string
	typeInfo reflect.Type
	index    []int
	tagged   bool
	nullable bool
}

// Match encoding/json's promoted-field and tag dominance rules. In particular,
// embedding a patch struct must not turn its nullable pointers into a bypass.
func jsonRequestFields(target reflect.Type) []jsonRequestField {
	if cached, ok := jsonRequestFieldsCache.Load(target); ok {
		return cached.([]jsonRequestField)
	}
	var candidates []jsonRequestField
	var collect func(reflect.Type, []int, map[reflect.Type]bool)
	collect = func(current reflect.Type, index []int, ancestors map[reflect.Type]bool) {
		if ancestors[current] {
			return
		}
		ancestors[current] = true
		defer delete(ancestors, current)
		for i := 0; i < current.NumField(); i++ {
			field := current.Field(i)
			fieldType := field.Type
			if fieldType.Kind() == reflect.Pointer {
				fieldType = fieldType.Elem()
			}
			if field.PkgPath != "" && (!field.Anonymous || fieldType.Kind() != reflect.Struct) {
				continue
			}
			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if name == "-" {
				continue
			}
			if !validJSONRequestTag(name) {
				name = ""
			}
			fieldIndex := append(append([]int(nil), index...), i)
			if name == "" && field.Anonymous && fieldType.Kind() == reflect.Struct {
				collect(fieldType, fieldIndex, ancestors)
				continue
			}
			tagged := name != ""
			if name == "" {
				name = field.Name
			}
			candidates = append(candidates, jsonRequestField{
				name: name, typeInfo: field.Type, index: fieldIndex, tagged: tagged,
				nullable: field.Tag.Get("jsonnullable") == "true",
			})
		}
	}
	collect(target, nil, make(map[reflect.Type]bool))
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.name != b.name {
			return a.name < b.name
		}
		if len(a.index) != len(b.index) {
			return len(a.index) < len(b.index)
		}
		if a.tagged != b.tagged {
			return a.tagged
		}
		return compareJSONRequestFieldIndex(a.index, b.index)
	})
	fields := make([]jsonRequestField, 0, len(candidates))
	for i := 0; i < len(candidates); {
		first := candidates[i]
		next := i + 1
		for next < len(candidates) && candidates[next].name == first.name {
			next++
		}
		if next == i+1 || len(candidates[i+1].index) != len(first.index) || candidates[i+1].tagged != first.tagged {
			fields = append(fields, first)
		}
		i = next
	}
	sort.Slice(fields, func(i, j int) bool {
		return compareJSONRequestFieldIndex(fields[i].index, fields[j].index)
	})
	jsonRequestFieldsCache.Store(target, fields)
	return fields
}

func compareJSONRequestFieldIndex(a, b []int) bool {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return len(a) < len(b)
}

func validJSONRequestTag(name string) bool {
	if name == "" {
		return false
	}
	for _, char := range name {
		if !strings.ContainsRune("!#$%&()*+-./:;<=>?@[]^_{|}~ ", char) && !unicode.IsLetter(char) && !unicode.IsDigit(char) {
			return false
		}
	}
	return true
}

func findJSONRequestField(fields []jsonRequestField, name string) (jsonRequestField, bool) {
	for _, field := range fields {
		if field.name == name {
			return field, true
		}
	}
	// Preserve the standard decoder's existing case-insensitive key matching;
	// likewise, non-null duplicate fields retain its existing ordered semantics.
	for _, field := range fields {
		if strings.EqualFold(field.name, name) {
			return field, true
		}
	}
	return jsonRequestField{}, false
}
