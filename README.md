# Reflectclient

This is an experimental and incomplete HTTP client for Go.


#### Example
```go
// Declare your service
type UserId struct {
    Id int64 `feature:"path" name:"id"`
}
type Service struct {
    // Index based description.
    User func(int) (User, error) `method:"GET" path:"/user/{0}"`

    // Feature based description.
    UserByStruct func(*UserId) (User, error) `method:"GET" path:"/user/{id}"`

    // Websocket support.
    UserSocket func(int) (*websocket.Conn, error) `method:"GET" path:"/usersocket/{0}"`
}

// Build your client
client, err := reflectclient.NewBuilder().
        BaseUrl("https://api.somesite.com").
		SetUnmarshaler(&reflectclient.JsonUnmarshaler{}).
        Build()

// Initialize your service
service := new(Service)
client.Init(service)

// Call stuff
user, err := service.User(15)
user, err := service.UserByStruct(&UserId{Id: 15})
conn, err := service.UserSocket(15)
```
