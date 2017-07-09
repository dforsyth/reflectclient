package reflectclient

import (
	"github.com/stretchr/testify/assert"
	"golang.org/x/net/websocket"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestNonFunctionField(t *testing.T) {
	type TestService struct {
		Id   int
		Call func() (interface{}, error) `rc_method:"GET" rc_path:"/test"`
	}
	client, _ := NewBuilder().Build()
	service := new(TestService)
	err := client.Init(service)
	assert.Nil(t, err)
}

func TestReturnValueCount(t *testing.T) {
	type TestService1 struct {
		NoReturnArgs func() `rc_method:"GET"`
	}
	type TestService2 struct {
		OneReturnArg func() int `rc_method:"GET"`
	}
	type TestService3 struct {
		TwoReturnArgs func() (int, error) `rc_method:"GET"`
	}
	type TestService4 struct {
		ThreeReturnArgs func() (int, int, error) `rc_method:"GET"`
	}

	client, _ := NewBuilder().BaseUrl("http://localhost").Build()

	service1 := new(TestService1)
	err := client.Init(service1)
	assert.NotNil(t, err)

	service2 := new(TestService2)
	err = client.Init(service2)
	assert.NotNil(t, err)

	service3 := new(TestService3)
	err = client.Init(service3)
	assert.Nil(t, err)

	service4 := new(TestService4)
	err = client.Init(service4)
	assert.NotNil(t, err)
}

func TestMissingErrorReturn(t *testing.T) {
	type TestService struct {
		Call func() (interface{}, interface{}) `rc_method:"GET"`
	}
	service := new(TestService)
	client, _ := NewBuilder().Build()
	err := client.Init(service)
	assert.True(t, strings.HasPrefix(err.Error(), "Second return value must be an error."))
}

func TestUnsupportedMethod(t *testing.T) {
	type TestService struct {
		Method func() (interface{}, error) `rc_method:"BOGUS"`
	}
	service := new(TestService)
	client, _ := NewBuilder().Build()
	err := client.Init(service)
	assert.True(t, strings.HasPrefix(err.Error(), "Unsupported method: "))
}

func TestStructArgs(t *testing.T) {
	type TestArg struct {
		Id int64 `rc_feature:"path" rc_name:"id"`
	}
	type TestService struct {
		Path func(*TestArg) (interface{}, error) `rc_method:"GET" rc_path:"/{id}"`
	}
	service := new(TestService)
	client, _ := NewBuilder().Build()
	client.Init(service)
}

func TestApplyPathFields(t *testing.T) {
	type TestArg struct {
		Id int64 `rc_feature:"path" rc_name:"id"`
	}

	arg := TestArg{
		Id: 1234,
	}

	value := reflect.ValueOf(arg)

	sm, _ := processStructArg(value.Type())
	path := "/pre/{id}/post"

	path = applyPathFields(value, path, sm.pathFields)
	assert.Equal(t, path, "/pre/1234/post")
}

func TestApplyAdderFields(t *testing.T) {
	type TestArg struct {
		Id int64 `rc_feature:"query" rc_name:"id"`
	}

	arg := TestArg{
		Id: 1234,
	}

	value := reflect.ValueOf(arg)

	sm, _ := processStructArg(value.Type())
	v := url.Values{}

	applyAdderFields(value, v, sm.queryFields)
	assert.Equal(t, v.Get("id"), "1234")
}

func TestApplyPathIndex(t *testing.T) {
	path := "/{0}/{2}/{1}"
	path = applyPathIndex(reflect.ValueOf("a"), path, 0)
	path = applyPathIndex(reflect.ValueOf("b"), path, 1)
	path = applyPathIndex(reflect.ValueOf("c"), path, 2)

	assert.Equal(t, path, "/a/c/b")
}

func TestProcessStructArg(t *testing.T) {
	type TestArgs struct {
		Field  int    `rc_feature:"field" rc_name:"field1"`
		Body   []byte `rc_feature:"body"`
		Query  string `rc_feature:"query" rc_name:"query1"`
		Header string `rc_feature:"header" rc_name:"header1"`
		Path   string `rc_feature:"path" rc_name:"path1"`
	}

	args := &TestArgs{}
	argsType := reflect.TypeOf(args).Elem()

	sm, _ := processStructArg(argsType)
	assert.Equal(t, sm.pathFields["Path"].Name, "path1")
	assert.Equal(t, sm.formFields["Field"].Name, "field1")
	assert.Equal(t, sm.queryFields["Query"].Name, "query1")
	assert.Equal(t, sm.headerFields["Header"].Name, "header1")
	assert.Equal(t, sm.bodyField.Name, "Body")
}

func TestBodyAndFieldArgs(t *testing.T) {
	type FieldArg struct {
		Field string `rc_feature:"field" rc_name:"field"`
	}
	type BodyArg struct {
		Body string `rc_feature:"body"`
	}
	type TestService struct {
		Call func(fieldArg *FieldArg, bodyArg *BodyArg) ([]byte, error) `rc_method:"POST"`
	}

	client, _ := NewBuilder().Build()
	err := client.Init(&TestService{})
	assert.True(t, strings.HasPrefix(err.Error(),
		"Requests cannot have form fields and an explicit body."))
}

func TestMultipleBodySameArg(t *testing.T) {
	type BodyArg struct {
		Body  []byte `rc_feature:"body"`
		Body1 []byte `rc_feature:"body"`
	}
	type TestService struct {
		Call func(bodyArg *BodyArg) ([]byte, error) `rc_method:"POST"`
	}

	client, _ := NewBuilder().Build()
	err := client.Init(&TestService{})
	assert.True(t, strings.HasPrefix(err.Error(),
		"Only one body per request is supported."))
}

func TestMultipleBodyDiffArgs(t *testing.T) {
	type BodyArg struct {
		Body []byte `rc_feature:"body"`
	}
	type TestService struct {
		Call func(arg0, arg1 *BodyArg) ([]byte, error) `rc_method:"POST"`
	}

	client, _ := NewBuilder().Build()
	err := client.Init(&TestService{})
	assert.True(t, strings.HasPrefix(err.Error(),
		"Only one body per request is supported."))
}

func TestProcessStructArgNoName(t *testing.T) {
	type TestArgs struct {
		Field int `rc_feature:"field"`
	}
	args := &TestArgs{}
	argsType := reflect.TypeOf(args).Elem()

	sm, _ := processStructArg(argsType)
	arg := sm.formFields["Field"]
	assert.Equal(t, arg.Name, "Field")
}

func TestApplyRequestTransformers(t *testing.T) {
	client, _ := NewBuilder().
		AddRequestTransformer(func(r *http.Request) *http.Request {
			q := r.URL.Query()
			q.Add("one", "1")
			r.URL.RawQuery = q.Encode()
			return r
		}).
		Build()

	req, _ := http.NewRequest("GET", "http://someurl", nil)
	req = client.applyRequestTransformers(req)

	q := req.URL.Query()
	assert.Equal(t, q.Get("one"), "1")
}

func TestWebSocketInit(t *testing.T) {
	type WebSocketStruct struct {
		WSRequest func() (*websocket.Conn, error) `rc_method:"GET" origin:"https://www.websocket.org" path:"/echo"`
	}

	client, _ := NewBuilder().Build()
	service := &WebSocketStruct{}

	err := client.Init(service)
	assert.Nil(t, err)
}

func TestWebSocketConnect(t *testing.T) {
	/*
		type Args struct {
			P string `rc_name:"p" rc_feature:"path"`
			Q string `rc_name:"q" rc_feature:"query"`
		}
		type WebSocketStruct struct {
			WSRequest func(*Args) (*websocket.Conn, error) `method:"GET" origin:"https://www.websocket.org" path:"/{p}"`
		}

		client, _ := NewBuilder().BaseUrl("ws://localhost:1234").Build()
		service := &WebSocketStruct{}

		err := client.Init(service)
		if err != nil {
			t.Errorf(err.Error())
		}

		conn, wserr := service.WSRequest(&Args{P: "echo", Q: "wut"})
		if wserr != nil {
			t.Error(wserr.Error())
		}

		if _, err := conn.Write([]byte("test")); err != nil {
			t.Error(err.Error())
		}

		var msg = make([]byte, 512)
		var n int
		if n, err = conn.Read(msg); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Received: %s.\n", msg[:n])

		// scnr := bufio.NewScanner(conn)
		// text := scnr.Text()

		// t.Error(errors.New("read: " + text))
	*/
}
