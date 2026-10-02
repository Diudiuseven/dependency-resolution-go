package resolver

import (
	"fmt"
	"strings"
)

// ResolutionError 表示求解过程中的错误。
type ResolutionError struct {
	ErrKind ErrorKind
	Message string
	Cause   error
}

// ErrorKind 表示错误类型。
type ErrorKind int

const (
	ErrUnknown ErrorKind = iota
	ErrConflict
	ErrCycle
	ErrMissingDep
	ErrInvalidRange
	ErrRegistry
	ErrLockMismatch
	ErrCancelled
)

func (e *ResolutionError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Cause)
	}
	return e.Message
}

func (e *ResolutionError) Unwrap() error {
	return e.Cause
}

// Kind 返回错误类型。
func (e *ResolutionError) Kind() ErrorKind { return e.ErrKind }

// ConflictError 表示约束冲突，包含矛盾来源。
type ConflictError struct {
	Package     string
	Constraints []ConstraintSource
	Message     string
}

// ConstraintSource 表示一个约束及其来源。
type ConstraintSource struct {
	Constraint Constraint
	Source     Source
}

func (e *ConflictError) Error() string {
	var b strings.Builder
	b.WriteString("依赖冲突: ")
	b.WriteString(e.Package)
	b.WriteString("\n")
	for _, cs := range e.Constraints {
		b.WriteString("  - ")
		b.WriteString(cs.Source.String())
		b.WriteString(" 声明 ")
		b.WriteString(cs.Constraint.String())
		b.WriteString("\n")
	}
	if e.Message != "" {
		b.WriteString("  ")
		b.WriteString(e.Message)
	}
	return b.String()
}

// CycleError 表示依赖环。
type CycleError struct {
	Path []string
}

func (e *CycleError) Error() string {
	return fmt.Sprintf("检测到依赖环: %s", strings.Join(e.Path, " -> "))
}

// MissingDepError 表示依赖缺失。
type MissingDepError struct {
	Name   string
	Source Source
}

func (e *MissingDepError) Error() string {
	return fmt.Sprintf("依赖缺失: %s (来源: %s)", e.Name, e.Source.String())
}

// InvalidRangeError 表示非法版本范围。
type InvalidRangeError struct {
	Range  string
	Source Source
}

func (e *InvalidRangeError) Error() string {
	return fmt.Sprintf("非法版本范围: %q (来源: %s)", e.Range, e.Source.String())
}
