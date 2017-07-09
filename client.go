package reflectclient

import (
	"bytes"
	"errors"
	"fmt"
	"golang.org/x/net/websocket"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"reflect"
	"strings"
)

type Service interface{}

type FieldAdder interface {
	Add(string, string)
}

type Client struct {
	baseUrl             string
	retryHandler        RetryHandler
	unmarshaler         Unmarshaler
	requestTransformers []RequestTransformer
	httpClient          *http.Client
}

type Builder struct {
	baseUrl             string
	retryHandler        RetryHandler
	httpClient          *http.Client
	requestTransformers []RequestTransformer
	unmarshaler         Unmarshaler
}

type Arg struct {
	Name      string
	OmitEmpty bool
}

func NewBuilder() *Builder {
	return &Builder{
		requestTransformers: make([]RequestTransformer, 0),
	}
}

func (b *Builder) BaseUrl(baseUrl string) *Builder {
	b.baseUrl = baseUrl
	return b
}

func (b *Builder) AddRequestTransformer(transformer RequestTransformer) *Builder {
	b.requestTransformers = append(b.requestTransformers, transformer)
	return b
}

func (b *Builder) SetUnmarshaler(unmarshaler Unmarshaler) *Builder {
	b.unmarshaler = unmarshaler
	return b
}

func (b *Builder) SetRetryHandler(r RetryHandler) *Builder {
	b.retryHandler = r
	return b
}

func (b *Builder) SetHttpClient(c *http.Client) *Builder {
	b.httpClient = c
	return b
}

func (b *Builder) Build() (*Client, error) {
	return &Client{
		b.baseUrl,
		b.retryHandler,
		b.unmarshaler,
		b.requestTransformers,
		http.DefaultClient,
	}, nil
}

// For validation
var HttpMethods = []string{
	"GET",
	"POST",
	"PUT",
	"DELETE",
}

type MethodMeta struct {
	returnType reflect.Type
	methodArgs []MethodArg
	hasBody    bool
	webSocket  bool
	path       string
	method     string
	origin     string
}

func (m *MethodMeta) hasFields() bool {
	for _, arg := range m.methodArgs {
		if arg.isStruct {
			if len(arg.structMeta.formFields) > 0 {
				return true
			}
		}
	}
	return false
}

type MethodArg struct {
	isStruct   bool
	structMeta *StructMeta
}

type StructMeta struct {
	pathFields   map[string]*Arg
	formFields   map[string]*Arg
	queryFields  map[string]*Arg
	headerFields map[string]*Arg
	bodyField    *Arg
}

type RequestMeta struct {
	path    string
	method  string
	query   url.Values
	fields  url.Values
	headers http.Header
	body    []byte
}

const (
	TagMethod       = "rc_method"
	TagPath         = "rc_path"
	TagFeature      = "rc_feature"
	TagName         = "rc_name"
	TagOrigin       = "rc_origin"
	TagOptions      = "rc_options"
	FeaturePath     = "path"
	FeatureField    = "field"
	FeatureQuery    = "query"
	FeatureHeader   = "header"
	FeatureBody     = "body"
	OptionOmitEmpty = "omitempty"
)

func (c *Client) applyRequestTransformers(req *http.Request) *http.Request {
	for _, t := range c.requestTransformers {
		req = t(req)
	}
	return req
}

// Initialize the target service
func (c *Client) Init(service Service) error {
	serviceValue := reflect.ValueOf(service).Elem()
	serviceType := serviceValue.Type()

	for fieldIdx := 0; fieldIdx < serviceType.NumField(); fieldIdx++ {
		fieldValue := serviceValue.Field(fieldIdx)
		fieldStruct := serviceType.Field(fieldIdx)
		fieldType := fieldStruct.Type

		// If field isn't a Func, ignore it. We can do better checks in the future.
		if fieldType.Kind() != reflect.Func {
			continue
		}

		// Construct the MethodMeta
		meta := &MethodMeta{
			methodArgs: make([]MethodArg, fieldType.NumIn()),
		}

		if fieldType.NumOut() != 2 {
			return errors.New("Functions must return two values")
		}

		meta.returnType = fieldType.Out(0)
		if meta.returnType == reflect.TypeOf((**websocket.Conn)(nil)).Elem() {
			meta.webSocket = true
			meta.origin = fieldStruct.Tag.Get(TagOrigin)
		}

		if fieldType.Out(1) != reflect.TypeOf((*error)(nil)).Elem() {
			return errors.New("Second return value must be an error.")
		}

		meta.method = fieldStruct.Tag.Get(TagMethod)
		if !in(meta.method, HttpMethods) {
			return errors.New("Unsupported method: " + meta.method)
		}
		// TODO(dforsyth): Warn for WebSockets if method is not GET? Or make WebSocket a method?

		meta.path = fieldStruct.Tag.Get(TagPath)

		for argIdx := 0; argIdx < fieldType.NumIn(); argIdx++ {
			argType := fieldType.In(argIdx)
			argValue := elementType(argType)

			// TODO: make sure we only accept certain Kinds here. No Methods, etc.
			if argValue.Kind() == reflect.Struct {
				meta.methodArgs[argIdx].isStruct = true
				sm, err := processStructArg(argValue)
				if err != nil {
					return err
				}
				if sm.bodyField != nil {
					if meta.hasBody {
						return errors.New("Only one body per request is supported.")
					}
					meta.hasBody = true
				}
				meta.methodArgs[argIdx].structMeta = sm
			} else {
				meta.methodArgs[argIdx].isStruct = false
			}
		}

		// Check for issues with body and form fields
		if meta.hasBody && meta.hasFields() {
			return errors.New("Requests cannot have form fields and an explicit body.")
		}

		if !meta.webSocket {
			fieldValue.Set(c.makeRequestFunc(fieldType, meta))
		} else {
			fieldValue.Set(c.makeWebSocketFunc(fieldType, meta))
		}
	}

	return nil
}

func isEmptyValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool:
		return !v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	case reflect.Interface, reflect.Ptr:
		return v.IsNil()
	}
	return false
}

func applyPathFields(value reflect.Value, path string, nameMap map[string]*Arg) string {
	for fn, n := range nameMap {
		if !value.IsValid() || n.OmitEmpty && isEmptyValue(value) {
			continue
		}
		path = strings.Replace(path, fmt.Sprintf("{%s}", n.Name), extractFieldValue(value, fn), -1)
	}
	return path
}

func applyPathIndex(value reflect.Value, path string, index int) string {
	return strings.Replace(path, fmt.Sprintf("{%d}", index), fmt.Sprint(value.Interface()), -1)
}

func applyAdderFields(value reflect.Value, adder FieldAdder, nameMap map[string]*Arg) {
	for fn, n := range nameMap {
		if !value.IsValid() || n.OmitEmpty && isEmptyValue(value.FieldByName(fn)) {
			continue
		}
		adder.Add(n.Name, extractFieldValue(value, fn))
	}
}

// Unmarshal an HTTP response and return it. If an erro is found, return that instead.
func (c *Client) handleResponse(meta *MethodMeta, resp *http.Response, err error) []reflect.Value {
	rvals := []reflect.Value{
		reflect.Zero(meta.returnType),
		reflect.Zero(reflect.TypeOf((*error)(nil)).Elem()),
	}

	if err != nil {
		rvals[1] = reflect.ValueOf(&err).Elem()
	} else if resp != nil {
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			rvals[1] = reflect.ValueOf(&err).Elem()
		} else {
			if c.unmarshaler == nil {
				rvals[0] = reflect.ValueOf(body)
			} else {
				instance := reflect.New(meta.returnType)
				if err := c.unmarshaler.Unmarshal(body, instance.Interface()); err != nil {
					rvals[1] = reflect.ValueOf(&err).Elem()
				} else {
					rvals[0] = instance.Elem()
				}
			}
		}
	}

	return rvals
}

// Handle the tagged fields of a struct and put them into a StructMeta.
func processStructArg(argType reflect.Type) (*StructMeta, error) {
	structMeta := &StructMeta{
		pathFields:   make(map[string]*Arg),
		formFields:   make(map[string]*Arg),
		queryFields:  make(map[string]*Arg),
		headerFields: make(map[string]*Arg),
	}

	for i := 0; i < argType.NumField(); i++ {
		field := argType.Field(i)
		// TODO: Validate only simple Kinds -- no funcs or structs (or maps, for now).
		if field.Type.Kind() == reflect.Func {
			continue
		}

		// Only process the field is we find a feature Tag
		feature := field.Tag.Get(TagFeature)
		if feature == "" {
			continue
		}

		// If we don't find a name, use the Field name
		name := field.Tag.Get(TagName)
		if name == "" {
			name = field.Name
		}

		arg := &Arg{Name: name}

		switch feature {
		case FeaturePath:
			structMeta.pathFields[field.Name] = arg
		case FeatureField:
			structMeta.formFields[field.Name] = arg
		case FeatureQuery:
			structMeta.queryFields[field.Name] = arg
		case FeatureHeader:
			structMeta.headerFields[field.Name] = arg
		case FeatureBody:
			if structMeta.bodyField != nil {
				return nil, errors.New("Only one body per request is supported.")
			}
			structMeta.bodyField = arg
		default:
			println(feature)
			continue
		}

		optTag := field.Tag.Get(TagOptions)
		opts := strings.Split(optTag, ",")
		for _, opt := range opts {
			switch opt {
			case OptionOmitEmpty:
				arg.OmitEmpty = true
			default:
				continue
			}
		}
	}

	return structMeta, nil
}

// Go through meta and args to build out request info.
func buildRequestMeta(meta *MethodMeta, args []reflect.Value) (*RequestMeta, error) {

	rm := &RequestMeta{
		path:    meta.path,
		method:  meta.method,
		query:   url.Values{},
		fields:  url.Values{},
		headers: http.Header{},
	}

	// Walk arguments, using collected information to build our request
	for argIdx, arg := range args {
		methodArg := meta.methodArgs[argIdx]
		// If we don't have a struct, do a path replace for the index
		if !methodArg.isStruct {
			rm.path = applyPathIndex(arg, rm.path, argIdx)
		} else {
			structMeta := methodArg.structMeta
			argValue := elementValue(arg)

			// update path
			rm.path = applyPathFields(argValue, rm.path, structMeta.pathFields)

			// collect query values
			applyAdderFields(argValue, rm.query, structMeta.queryFields)

			// collect form values
			applyAdderFields(argValue, rm.fields, structMeta.formFields)

			// collect header values
			applyAdderFields(argValue, rm.headers, structMeta.headerFields)

			// handle a body if the argument provides one
			if structMeta.bodyField != nil {
				val := argValue.FieldByName(structMeta.bodyField.Name)
				if val.IsValid() && !(structMeta.bodyField.OmitEmpty && isEmptyValue(val)) {
					rm.body = val.Bytes()
				}
			}
		}
	}

	if len(rm.fields) > 0 {
		if rm.body != nil {
			return nil, errors.New("Body and fields are incompatible.")
		}
		rm.body = []byte(rm.fields.Encode())
	}

	return rm, nil
}

// Build a function that makes an HTTP request and returns a given type, decoded from
// the body of the response.
func (c *Client) makeRequestFunc(typ reflect.Type, meta *MethodMeta) reflect.Value {
	return reflect.MakeFunc(typ, func(args []reflect.Value) []reflect.Value {
		rm, err := buildRequestMeta(meta, args)
		if err != nil {
			return c.handleResponse(meta, nil, err)
		}

		var bodyReader io.Reader
		if rm.body != nil {
			bodyReader = bytes.NewBuffer(rm.body)
		}

		// Once we have the base path and the bodyReader, we can generate the request and update the rest of it.
		req, err := http.NewRequest(rm.method, c.baseUrl+rm.path, bodyReader)
		if err != nil {
			c.handleResponse(meta, nil, err)
		}

		qu := req.URL.Query()
		for qn, ql := range rm.query {
			for _, q := range ql {
				qu.Add(qn, q)
			}
		}
		req.URL.RawQuery = qu.Encode()

		for hn, hl := range rm.headers {
			for _, h := range hl {
				req.Header.Add(hn, h)
			}
		}

		req = c.applyRequestTransformers(req)

		client := c.httpClient
		// Make the request
		for {
			resp, err := client.Do(req)
			if err != nil && c.retryHandler != nil {
				if err = c.retryHandler.Retry(err); err == nil {
					continue
				}
			}

			return c.handleResponse(meta, resp, err)
		}

		panic("Not reached.")
	})
}

// Build a function that connects to a WebSocket and returns a conneciton.
func (c *Client) makeWebSocketFunc(typ reflect.Type, meta *MethodMeta) reflect.Value {
	return reflect.MakeFunc(typ, func(args []reflect.Value) []reflect.Value {
		// We don't expect the body error.
		rm, _ := buildRequestMeta(meta, args)

		rvals := []reflect.Value{
			reflect.Zero(meta.returnType),
			reflect.Zero(reflect.TypeOf((*error)(nil)).Elem()),
		}

		config, err := websocket.NewConfig(c.baseUrl+rm.path, meta.origin)
		if err != nil {
			rvals[1] = reflect.ValueOf(&err).Elem()
			return rvals
		}

		qu := config.Location.Query()
		for qn, ql := range rm.query {
			for _, q := range ql {
				qu.Add(qn, q)
			}
		}
		config.Location.RawQuery = qu.Encode()

		for hn, hl := range rm.headers {
			for _, h := range hl {
				config.Header.Add(hn, h)
			}
		}

		conn, err := websocket.DialConfig(config)
		if err != nil {
			rvals[1] = reflect.ValueOf(&err).Elem()
			return rvals
		}

		rvals[0] = reflect.ValueOf(&conn).Elem()

		return rvals
	})
}
