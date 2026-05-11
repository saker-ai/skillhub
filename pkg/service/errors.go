package service

import "errors"

// ErrForbidden 是 service 层的"鉴权失败"哨兵错误。
//
// 用法:
//
//	return fmt.Errorf("%w: only owner can delete a namespace", service.ErrForbidden)
//
// handler 通过 errors.Is(err, service.ErrForbidden) 判断是否回 HTTP 403。
//
// 之前 handler/errors.go 用 strings.HasPrefix(msg, "forbidden:") 字符串前缀
// 检测,任何人改 service 文案漏掉前缀,403 就会静默退化成 400 —— P3-12
// 修过的语义就这么没了。换成 sentinel + errors.Is 后,文案怎么改都不影响
// HTTP 状态码映射。
//
// 注:errors.Is 沿 wrapped chain 走,所以多层 fmt.Errorf("%w: ..", err) 也能识别。
var ErrForbidden = errors.New("forbidden")
