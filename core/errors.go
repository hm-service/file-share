package core

// Error 是业务层抛给 HTTP 层的错误：只带状态码与文案键，语言由 HTTP 层按请求的
// Accept-Language 决定 —— 业务层不碰具体请求，也就不该知道该用哪种语言。
type Error struct {
	Status int
	Key    string
}

func (e *Error) Error() string { return e.Key }

// Fail 造一个业务错误。参数是 HTTP 状态码与 i18n 文案键。
func Fail(status int, key string) *Error { return &Error{Status: status, Key: key} }
