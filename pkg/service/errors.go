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

// ErrValidation indicates a request-level validation failure (HTTP 400).
var ErrValidation = errors.New("validation")

// ErrNotFound indicates a resource was not found (HTTP 404).
var ErrNotFound = errors.New("not found")

// ErrConflict indicates a duplicate/conflict state (HTTP 409).
var ErrConflict = errors.New("conflict")

// AmbiguousCandidate is one entry in the disambiguation list returned when
// a bare slug matches multiple skills across namespaces.
type AmbiguousCandidate struct {
	Namespace   string `json:"namespace"`
	Slug        string `json:"slug"`
	OwnerHandle string `json:"ownerHandle"`
	SkillID     string `json:"skillId"`
}

// AmbiguousSlugError is returned when a bare slug (without namespace qualifier)
// matches multiple skills across different namespaces. The caller should
// present the candidates and ask the user to qualify the slug with @namespace/.
type AmbiguousSlugError struct {
	Slug       string
	Candidates []AmbiguousCandidate
}

func (e *AmbiguousSlugError) Error() string {
	return "ambiguous slug '" + e.Slug + "': exists in multiple namespaces"
}

// Is returns true for ErrConflict so writeServiceError maps this to HTTP 409.
func (e *AmbiguousSlugError) Is(target error) bool {
	return target == ErrConflict
}
